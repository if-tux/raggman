// Package documents manages Documents: submitting, listing and removing them,
// and enqueuing their Ingestion.
package documents

import (
	"context"
	"errors"
	"fmt"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// JobTypeIngestion marks queue Jobs that ingest a Document.
const JobTypeIngestion = "INGESTION"

var ErrNotFound = errors.New("document not found")

// errVanished means a conflicting Document was removed mid-submission.
var errVanished = errors.New("existing document removed during submission")

type Service struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, queries: sqlc.New(pool)}
}

// Submit stores content as a new Pending Document and enqueues its Ingestion,
// atomically. If a Document with the same content exists, it is returned
// instead (created == false), restarting its Ingestion only if it had Failed.
func (s *Service) Submit(ctx context.Context, filename string, content []byte) (doc domain.Document, created bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Document{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	newDoc := domain.NewDocument(filename, content)
	doc, created, err = submit(ctx, q, newDoc, content)
	if errors.Is(err, errVanished) {
		// The existing Document was removed between our insert and our read;
		// the content is free again, so this submission creates it.
		doc, created, err = submit(ctx, q, newDoc, content)
	}
	if err != nil {
		return domain.Document{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Document{}, false, err
	}
	return doc, created, nil
}

func submit(ctx context.Context, q *sqlc.Queries, doc domain.Document, content []byte) (domain.Document, bool, error) {
	row, err := q.CreateDocument(ctx, sqlc.CreateDocumentParams{
		ID:       doc.ID,
		FileHash: doc.FileHash,
		Filename: doc.Filename,
		Status:   string(doc.Status),
	})
	switch {
	case err == nil:
		if err := q.CreateDocumentFile(ctx, sqlc.CreateDocumentFileParams{DocumentID: row.ID, Content: content}); err != nil {
			return domain.Document{}, false, fmt.Errorf("storing document content: %w", err)
		}
		if err := enqueueIngestion(ctx, q, row.ID); err != nil {
			return domain.Document{}, false, err
		}
		return toDomain(row), true, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return domain.Document{}, false, fmt.Errorf("creating document: %w", err)
	}

	// Same content already submitted. Lock it so concurrent resubmits of a
	// Failed Document enqueue only one Ingestion.
	row, err = q.GetDocumentByHashForUpdate(ctx, doc.FileHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Document{}, false, errVanished
	}
	if err != nil {
		return domain.Document{}, false, fmt.Errorf("loading existing document: %w", err)
	}
	existing := toDomain(row)
	if existing.Resubmit() {
		if err := q.UpdateDocumentStatus(ctx, statusParams(existing)); err != nil {
			return domain.Document{}, false, fmt.Errorf("resubmitting document: %w", err)
		}
		if err := enqueueIngestion(ctx, q, existing.ID); err != nil {
			return domain.Document{}, false, err
		}
	}
	return existing, false, nil
}

func enqueueIngestion(ctx context.Context, q *sqlc.Queries, documentID uuid.UUID) error {
	_, err := q.CreateJob(ctx, sqlc.CreateJobParams{
		ID:       uuid.New(),
		TargetID: documentID,
		JobType:  JobTypeIngestion,
		Payload:  []byte("{}"),
	})
	if err != nil {
		return fmt.Errorf("enqueuing ingestion: %w", err)
	}
	return nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Document, error) {
	row, err := s.queries.GetDocument(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Document{}, ErrNotFound
	}
	if err != nil {
		return domain.Document{}, err
	}
	return toDomain(row), nil
}

func (s *Service) List(ctx context.Context) ([]domain.Document, error) {
	rows, err := s.queries.ListDocuments(ctx)
	if err != nil {
		return nil, err
	}
	docs := make([]domain.Document, len(rows))
	for i, row := range rows {
		docs[i] = toDomain(row)
	}
	return docs, nil
}

// Remove deletes a Document, its content and its Chunks.
func (s *Service) Remove(ctx context.Context, id uuid.UUID) error {
	n, err := s.queries.DeleteDocument(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func toDomain(row sqlc.Document) domain.Document {
	return domain.Document{
		ID:            row.ID,
		FileHash:      row.FileHash,
		Filename:      row.Filename,
		Status:        domain.Status(row.Status),
		FailureReason: row.FailureReason.String,
		CreatedAt:     row.CreatedAt.Time,
	}
}

func statusParams(d domain.Document) sqlc.UpdateDocumentStatusParams {
	return sqlc.UpdateDocumentStatusParams{
		ID:            d.ID,
		Status:        string(d.Status),
		FailureReason: pgtype.Text{String: d.FailureReason, Valid: d.FailureReason != ""},
	}
}
