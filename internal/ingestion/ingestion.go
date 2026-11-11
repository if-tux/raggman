// Package ingestion turns Documents into searchable Chunks: for each Ingestion
// Job it extracts each Page's text, chunks and embeds it, and marks the
// Document Ready. The queue package runs the Jobs (ADR-0003).
package ingestion

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/BrunoPereiradosSantos/raggman/internal/documents"
	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/llm"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/pdftext"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/telemetry"
	"github.com/BrunoPereiradosSantos/raggman/internal/queue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const embedBatchSize = 32

var errNoText = errors.New("no extractable text")

// Handler runs Ingestion Jobs.
type Handler struct {
	tracer   trace.Tracer
	pool     *pgxpool.Pool
	queries  *sqlc.Queries
	embedder *llm.Client
}

func New(pool *pgxpool.Pool, env *environment.Environment, tp *sdktrace.TracerProvider) *Handler {
	return &Handler{
		tracer:   tp.Tracer("raggman/ingestion"),
		pool:     pool,
		queries:  sqlc.New(pool),
		embedder: llm.New(env.EmbeddingsBaseURL(), ""),
	}
}

func (w *Handler) Name() string { return "Ingestion" }

func (w *Handler) TargetKey() string { return "document_id" }

// TargetGone reports whether the Job's Document was removed.
func (w *Handler) TargetGone(ctx context.Context, job sqlc.JobQueue) (bool, error) {
	_, err := w.queries.GetDocument(ctx, job.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// MarkFailed moves the Job's Document to Failed.
func (w *Handler) MarkFailed(ctx context.Context, q *sqlc.Queries, job sqlc.JobQueue, reason string) error {
	row, err := q.GetDocument(ctx, job.TargetID)
	if err != nil {
		return err
	}
	doc := documents.FromRow(row)
	if doc.Status == domain.StatusPending {
		// An earlier attempt failed before Ingestion was recorded as started.
		if err := doc.StartIngestion(); err != nil {
			return err
		}
	}
	if err := doc.MarkFailed(reason); err != nil {
		return err
	}
	return q.UpdateDocumentStatus(ctx, documents.StatusParams(doc))
}

// Work ingests the Job's Document.
func (w *Handler) Work(ctx context.Context, job sqlc.JobQueue) error {
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
		return queue.Complete(ctx, w.queries, job)
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
		// Retrying cannot make text appear.
		return queue.Permanent(errNoText)
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

func (w *Handler) embed(ctx context.Context, chunks []domain.Chunk) ([][]float32, error) {
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
func (w *Handler) store(ctx context.Context, job sqlc.JobQueue, doc domain.Document, totalPages int, chunks []domain.Chunk, vectors [][]float32) error {
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
	if err := queue.Complete(ctx, q, job); err != nil {
		return err // rolls back: a worker that lost its lock writes nothing
	}
	return tx.Commit(ctx)
}
