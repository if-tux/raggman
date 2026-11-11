// Package ingestion turns Documents into searchable Chunks: workers claim
// Ingestion Jobs from the Postgres queue (ADR-0003), extract each Page's text,
// chunk and embed it, and mark the Document Ready.
package ingestion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/documents"
	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/llm"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/pdftext"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// ponytail: fixed backoff after each failed attempt; make it configurable if
// real outages last longer. Its length is maxAttempts-1.
var backoffs = []time.Duration{10 * time.Second, 40 * time.Second}

const (
	// ponytail: fixed worker count; local inference is the bottleneck, so more
	// workers only queue up at Ollama. Make it configurable for cloud setups.
	workerCount  = 2
	pollInterval = time.Second
	// defaultShutdownGrace is how long an in-flight Job may keep running after
	// shutdown starts. Grace plus settleTimeout stays under the app's 30s
	// shutdown timeout, so a Job is released before the pool closes.
	defaultShutdownGrace = 20 * time.Second
	settleTimeout        = 5 * time.Second
	// maxAttempts is how many times Ingestion is tried before the Document Fails.
	maxAttempts       = 3
	heartbeatInterval = 30 * time.Second
	embedBatchSize    = 32
)

// Workers run Ingestion Jobs. ShutdownGrace is how long an in-flight Job may
// finish after shutdown starts; tests lower it.
type Workers struct {
	tracer        trace.Tracer
	pool          *pgxpool.Pool
	queries       *sqlc.Queries
	embedder      *llm.Client
	logger        *slog.Logger
	ShutdownGrace time.Duration
}

func New(pool *pgxpool.Pool, env *environment.Environment, logger *slog.Logger, tp *sdktrace.TracerProvider) *Workers {
	return &Workers{
		tracer:   tp.Tracer("raggman/ingestion"),
		pool:     pool,
		queries:  sqlc.New(pool),
		embedder: llm.New(env.EmbeddingsBaseURL(), ""),
		logger:   logger,

		ShutdownGrace: defaultShutdownGrace,
	}
}

// Run works through Ingestion Jobs until ctx is cancelled. Workers then stop
// claiming Jobs, give an in-flight Job up to ShutdownGrace to finish (then
// hand it back to the queue), and Run returns once every worker has stopped.
func (w *Workers) Run(ctx context.Context) {
	host, _ := os.Hostname()
	var wg sync.WaitGroup
	for i := range workerCount {
		id := fmt.Sprintf("%s-%d", host, i)
		wg.Go(func() { w.loop(ctx, id) })
	}
	wg.Wait()
}

func (w *Workers) loop(ctx context.Context, workerID string) {
	for {
		job, err := w.queries.DequeueJob(ctx, pgtype.Text{String: workerID, Valid: true})
		if err == nil {
			w.ingestJob(ctx, workerID, job)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			w.logger.ErrorContext(ctx, "Claiming Ingestion Job failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

func (w *Workers) ingestJob(ctx context.Context, workerID string, job sqlc.JobQueue) {
	log := w.logger.With("job_id", job.ID, "document_id", job.TargetID, "worker", workerID)

	// The Ingestion continues the trace of the request that submitted the
	// Document; each attempt is its own span in that trace.
	spanCtx, span := w.tracer.Start(telemetry.FromCarrier(context.WithoutCancel(ctx), job.Payload), "ingestion",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("document.id", job.TargetID.String()),
			attribute.Int("job.attempt", int(job.Attempts)+1),
		))
	defer span.End()
	log.InfoContext(spanCtx, "Ingestion started")

	// The Job outlives ctx by ShutdownGrace, so shutdown does not abort it at once.
	jobCtx, cancel := context.WithCancel(spanCtx)
	defer cancel()
	grace := w.ShutdownGrace
	stopGrace := context.AfterFunc(ctx, func() { time.AfterFunc(grace, cancel) })
	defer stopGrace()

	stopHeartbeat := w.heartbeat(jobCtx, job.ID, workerID)
	err := w.ingest(jobCtx, job)
	stopHeartbeat()
	telemetry.RecordError(jobCtx, span, err)

	// Settle the Job even when jobCtx is cancelled, but never hang on it.
	settleCtx, cancelSettle := context.WithTimeout(spanCtx, settleTimeout)
	defer cancelSettle()

	switch {
	case err == nil:
		log.InfoContext(spanCtx, "Ingestion finished")
	case errors.Is(err, errLostLock):
		log.WarnContext(spanCtx, "Ingestion Job was reclaimed by another worker; dropping this attempt")
	case jobCtx.Err() != nil:
		log.InfoContext(spanCtx, "Shutdown grace ran out; releasing Ingestion Job")
		w.settle(settleCtx, log, "releasing", func() (int64, error) {
			return w.queries.ReleaseJob(settleCtx, sqlc.ReleaseJobParams{ID: job.ID, LockedBy: job.LockedBy})
		})
	default:
		w.fail(settleCtx, log, job, err)
	}
}

// errNoText is permanent: retrying cannot make text appear.
var errNoText = errors.New("no extractable text")

// fail decides what a failed attempt means: abort if the Document is gone,
// retry with backoff, or mark the Document Failed after the last attempt.
func (w *Workers) fail(ctx context.Context, log *slog.Logger, job sqlc.JobQueue, cause error) {
	row, err := w.queries.GetDocument(ctx, job.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		log.InfoContext(ctx, "Document was removed; aborting Ingestion")
		w.settle(ctx, log, "aborting", func() (int64, error) {
			return w.queries.CompleteJob(ctx, sqlc.CompleteJobParams{ID: job.ID, LockedBy: job.LockedBy})
		})
		return
	}
	if err != nil {
		log.ErrorContext(ctx, "Loading Document after a failed Ingestion", "error", err)
		return // the lock goes stale and the Job is reclaimed later
	}

	// ponytail: an attempt counts only when it fails cleanly; a crash mid-Job is
	// reclaimed without counting, so a PDF that crashes the process retries
	// forever. Count reclaims too if that ever happens.
	attempt := job.Attempts + 1
	reason := errNoText.Error()
	switch {
	case errors.Is(cause, errNoText):
		// Permanent: fail now.
	case attempt < maxAttempts:
		backoff := backoffs[attempt-1]
		log.WarnContext(ctx, "Ingestion attempt failed; retrying", "attempt", attempt, "backoff", backoff.String(), "error", cause)
		w.settle(ctx, log, "re-queuing", func() (int64, error) {
			return w.queries.RetryJob(ctx, sqlc.RetryJobParams{ID: job.ID, LockedBy: job.LockedBy, BackoffSeconds: backoff.Seconds()})
		})
		return
	default:
		reason = fmt.Sprintf("Ingestion failed after %d attempts: %v", attempt, cause)
	}
	log.ErrorContext(ctx, "Ingestion failed; marking Document Failed", "attempt", attempt, "reason", reason)
	if err := w.markFailed(ctx, job, documents.FromRow(row), reason); err != nil {
		log.ErrorContext(ctx, "Marking Document Failed", "error", err)
	}
}

// markFailed ends the Job and Fails the Document in one transaction. The Job
// row stays as FAILED for inspection.
// ponytail: FAILED Job rows are never cleaned up; purge old ones if the
// table grows.
func (w *Workers) markFailed(ctx context.Context, job sqlc.JobQueue, doc domain.Document, reason string) error {
	if doc.Status == domain.StatusPending {
		// An earlier attempt failed before Ingestion was recorded as started.
		if err := doc.StartIngestion(); err != nil {
			return err
		}
	}
	if err := doc.MarkFailed(reason); err != nil {
		return err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := w.queries.WithTx(tx)

	n, err := q.FailJob(ctx, sqlc.FailJobParams{ID: job.ID, LockedBy: job.LockedBy})
	if err != nil {
		return err
	}
	if n == 0 {
		return errLostLock
	}
	if err := q.UpdateDocumentStatus(ctx, documents.StatusParams(doc)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// settle runs a lock-guarded Job update and logs when it did not apply.
func (w *Workers) settle(ctx context.Context, log *slog.Logger, action string, update func() (int64, error)) {
	n, err := update()
	switch {
	case err != nil:
		log.ErrorContext(ctx, "Settling Ingestion Job failed", "action", action, "error", err)
	case n == 0:
		log.WarnContext(ctx, "Ingestion Job was reclaimed by another worker", "action", action)
	}
}

// heartbeat keeps the Job's lock fresh so it is not reclaimed mid-Ingestion.
func (w *Workers) heartbeat(ctx context.Context, jobID uuid.UUID, workerID string) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				err := w.queries.UpdateJobHeartBeat(ctx, sqlc.UpdateJobHeartBeatParams{
					ID: jobID, LockedBy: pgtype.Text{String: workerID, Valid: true},
				})
				if err != nil && ctx.Err() == nil {
					w.logger.WarnContext(ctx, "Ingestion heartbeat failed", "job_id", jobID, "error", err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}

func (w *Workers) ingest(ctx context.Context, job sqlc.JobQueue) error {
	row, err := w.queries.GetDocument(ctx, job.TargetID)
	if err != nil {
		return fmt.Errorf("loading document: %w", err)
	}
	doc := documents.FromRow(row)

	switch doc.Status {
	case domain.StatusPending:
		if err := doc.StartIngestion(); err != nil {
			return err
		}
		if err := w.queries.UpdateDocumentStatus(ctx, documents.StatusParams(doc)); err != nil {
			return fmt.Errorf("marking document ingesting: %w", err)
		}
	case domain.StatusIngesting:
		// A reclaimed Job resuming an interrupted Ingestion.
	default:
		// Ready or Failed: nothing left to do for this Job.
		return w.complete(ctx, w.queries, job)
	}

	content, err := w.queries.GetDocumentContent(ctx, doc.ID)
	if err != nil {
		return fmt.Errorf("loading document content: %w", err)
	}
	pages, err := telemetry.Span(ctx, w.tracer, "ingestion.extract", func(ctx context.Context) ([]string, error) {
		return pdftext.Pages(ctx, content)
	})
	if err != nil {
		return err
	}
	chunks, _ := telemetry.Span(ctx, w.tracer, "ingestion.chunk", func(context.Context) ([]domain.Chunk, error) {
		return domain.ChunkPages(pages), nil
	})
	if len(chunks) == 0 {
		return errNoText
	}
	vectors, err := telemetry.Span(ctx, w.tracer, "ingestion.embed", func(ctx context.Context) ([][]float32, error) {
		return w.embed(ctx, chunks)
	})
	if err != nil {
		return err
	}

	_, err = telemetry.Span(ctx, w.tracer, "ingestion.store", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, w.store(ctx, job, doc, len(pages), chunks, vectors)
	})
	return err
}

func (w *Workers) embed(ctx context.Context, chunks []domain.Chunk) ([][]float32, error) {
	vectors := make([][]float32, 0, len(chunks))
	for batch := range slices.Chunk(chunks, embedBatchSize) {
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Content
		}
		got, err := w.embedder.Embed(ctx, environment.EmbeddingModel, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding chunks: %w", err)
		}
		for _, v := range got {
			if len(v) != environment.EmbeddingDims {
				return nil, fmt.Errorf("embedding has %d dimensions, want %d", len(v), environment.EmbeddingDims)
			}
		}
		vectors = append(vectors, got...)
	}
	return vectors, nil
}

// store replaces the Document's Chunks, marks it Ready and completes the Job
// in one transaction, so a Document is never Ready with partial Chunks.
func (w *Workers) store(ctx context.Context, job sqlc.JobQueue, doc domain.Document, totalPages int, chunks []domain.Chunk, vectors [][]float32) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := w.queries.WithTx(tx)

	if err := q.DeleteChunksByDocument(ctx, doc.ID); err != nil {
		return fmt.Errorf("clearing previous chunks: %w", err)
	}
	for i, c := range chunks {
		_, err := q.CreateChunk(ctx, sqlc.CreateChunkParams{
			ID:         uuid.New(),
			DocumentID: doc.ID,
			PageNumber: int32(c.Page),
			Content:    c.Content,
			Embedding:  pgvector.NewVector(vectors[i]),
		})
		if err != nil {
			return fmt.Errorf("storing chunk: %w", err)
		}
	}
	err = q.SetDocumentTotalPages(ctx, sqlc.SetDocumentTotalPagesParams{
		ID: doc.ID, TotalPages: pgtype.Int4{Int32: int32(totalPages), Valid: true},
	})
	if err != nil {
		return fmt.Errorf("storing page count: %w", err)
	}
	if err := doc.MarkReady(); err != nil {
		return err
	}
	if err := q.UpdateDocumentStatus(ctx, documents.StatusParams(doc)); err != nil {
		return fmt.Errorf("marking document ready: %w", err)
	}
	if err := w.complete(ctx, q, job); err != nil {
		return err // rolls back: a worker that lost its lock writes nothing
	}
	return tx.Commit(ctx)
}

var errLostLock = errors.New("job was reclaimed by another worker")

// complete removes the Job, only if this worker still holds its lock.
func (w *Workers) complete(ctx context.Context, q *sqlc.Queries, job sqlc.JobQueue) error {
	n, err := q.CompleteJob(ctx, sqlc.CompleteJobParams{ID: job.ID, LockedBy: job.LockedBy})
	if err != nil {
		return fmt.Errorf("completing job: %w", err)
	}
	if n == 0 {
		return errLostLock
	}
	return nil
}
