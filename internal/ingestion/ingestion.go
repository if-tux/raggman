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
	"strings"
	"sync"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/documents"
	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/llm"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/pdftext"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

const (
	// ponytail: fixed worker count; local inference is the bottleneck, so more
	// workers only queue up at Ollama. Make it configurable for cloud setups.
	workerCount  = 2
	pollInterval = time.Second
	// shutdownGrace is how long an in-flight Job may keep running after
	// shutdown starts; below the app's 30s shutdown timeout so workers stop first.
	shutdownGrace     = 25 * time.Second
	heartbeatInterval = 30 * time.Second
	embedBatchSize    = 32
)

type Workers struct {
	pool     *pgxpool.Pool
	queries  *sqlc.Queries
	embedder *llm.Client
	logger   *slog.Logger
}

func New(pool *pgxpool.Pool, env *environment.Environment, logger *slog.Logger) *Workers {
	return &Workers{
		pool:     pool,
		queries:  sqlc.New(pool),
		embedder: llm.New(strings.TrimSuffix(env.OllamaHost, "/")+"/v1", ""),
		logger:   logger,
	}
}

// Run works through Ingestion Jobs until ctx is cancelled. Workers then stop
// claiming Jobs, give an in-flight Job up to shutdownGrace to finish, and Run
// returns once every worker has stopped.
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
	log.InfoContext(ctx, "Ingestion started")

	// The Job outlives ctx by shutdownGrace, so shutdown does not abort it at once.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	stopGrace := context.AfterFunc(ctx, func() { time.AfterFunc(shutdownGrace, cancel) })
	defer stopGrace()

	stopHeartbeat := w.heartbeat(jobCtx, job.ID, workerID)
	err := w.ingest(jobCtx, job)
	stopHeartbeat()

	if err == nil {
		log.InfoContext(ctx, "Ingestion finished")
		return
	}
	if jobCtx.Err() != nil {
		// Grace ran out during shutdown: the lock goes stale and the Job is
		// reclaimed later.
		return
	}
	log.ErrorContext(ctx, "Ingestion failed", "error", err)
	if err := w.queries.FailedJob(jobCtx, job.ID); err != nil {
		log.ErrorContext(ctx, "Marking Ingestion Job failed", "error", err)
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
	pages, err := pdftext.Pages(ctx, content)
	if err != nil {
		return err
	}
	chunks := domain.ChunkPages(pages)
	if len(chunks) == 0 {
		return errors.New("no extractable text")
	}
	vectors, err := w.embed(ctx, chunks)
	if err != nil {
		return err
	}

	return w.store(ctx, job, doc, len(pages), chunks, vectors)
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
