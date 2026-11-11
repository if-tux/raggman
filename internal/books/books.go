// Package books manages Books: submitting an EPUB and reading it into Blocks,
// listing and removing them. Books never join the Corpus (ADR-0005).
package books

import (
	"context"
	"errors"
	"fmt"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/epub"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("book not found")

type Service struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, queries: sqlc.New(pool)}
}

// Submit reads content as an EPUB and stores it as a new Book with its
// Blocks, atomically. If a Book with the same content exists, it is returned
// instead (created == false). An EPUB that cannot become a Book fails with an
// error wrapping epub.ErrNotEPUB, epub.ErrDRM or epub.ErrInvalid.
func (s *Service) Submit(ctx context.Context, filename string, content []byte) (book domain.Book, created bool, err error) {
	hash := domain.ContentHash(content)
	var parsed *epub.Parsed
	// Twice, because a concurrent submission of the same content can win the
	// insert, and that Book can be removed again before it is read back.
	for range 2 {
		row, err := s.queries.GetBookByHash(ctx, hash)
		if err == nil {
			return fromRow(row), false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return domain.Book{}, false, fmt.Errorf("looking up book: %w", err)
		}
		if parsed == nil {
			p, err := epub.Parse(content)
			if err != nil {
				return domain.Book{}, false, err
			}
			parsed = &p
		}
		book, created, err = s.create(ctx, hash, filename, content, *parsed)
		if err != nil || created {
			return book, created, err
		}
	}
	return domain.Book{}, false, errors.New("book kept being created and removed during submission")
}

// create stores a new Book; created is false when the same content was
// submitted concurrently and won the insert.
func (s *Service) create(ctx context.Context, hash, filename string, content []byte, parsed epub.Parsed) (book domain.Book, created bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Book{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	row, err := q.CreateBook(ctx, sqlc.CreateBookParams{
		ID:          uuid.New(),
		FileHash:    hash,
		Filename:    filename,
		Title:       parsed.Title,
		Language:    parsed.Language,
		TotalBlocks: int32(len(parsed.Blocks)),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Book{}, false, nil
	}
	if err != nil {
		return domain.Book{}, false, fmt.Errorf("creating book: %w", err)
	}
	if err := q.CreateBookFile(ctx, sqlc.CreateBookFileParams{BookID: row.ID, Content: content}); err != nil {
		return domain.Book{}, false, fmt.Errorf("storing book content: %w", err)
	}
	blocks := make([]sqlc.CreateBlocksParams, len(parsed.Blocks))
	for i, b := range parsed.Blocks {
		blocks[i] = sqlc.CreateBlocksParams{
			BookID:   row.ID,
			Position: int32(i),
			Chapter:  int32(b.Chapter),
			Kind:     string(b.Kind),
			Source:   b.Source,
		}
	}
	if _, err := q.CreateBlocks(ctx, blocks); err != nil {
		return domain.Book{}, false, fmt.Errorf("storing blocks: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Book{}, false, err
	}
	return fromRow(row), true, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Book, error) {
	row, err := s.queries.GetBook(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Book{}, ErrNotFound
	}
	if err != nil {
		return domain.Book{}, err
	}
	return fromRow(row), nil
}

func (s *Service) List(ctx context.Context) ([]domain.Book, error) {
	rows, err := s.queries.ListBooks(ctx)
	if err != nil {
		return nil, err
	}
	books := make([]domain.Book, len(rows))
	for i, row := range rows {
		books[i] = fromRow(row)
	}
	return books, nil
}

// Remove deletes a Book, its content and its Blocks.
func (s *Service) Remove(ctx context.Context, id uuid.UUID) error {
	n, err := s.queries.DeleteBook(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func fromRow(row sqlc.Book) domain.Book {
	return domain.Book{
		ID:          row.ID,
		FileHash:    row.FileHash,
		Filename:    row.Filename,
		Title:       row.Title,
		Language:    row.Language,
		TotalBlocks: int(row.TotalBlocks),
		CreatedAt:   row.CreatedAt.Time,
	}
}
