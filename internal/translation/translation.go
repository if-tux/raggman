// Package translation manages Translations and their Termbases: starting a
// Translation of a Book, building the Termbase from the whole Book, and
// letting the user review and lock it before any Block is translated
// (ADR-0006).
package translation

import (
	"context"
	"errors"
	"fmt"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/telemetry"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// JobTypeExtraction marks queue Jobs that build a Termbase; their target
	// is the Termbase.
	JobTypeExtraction = "TERMBASE_EXTRACTION"
	// JobTypeTranslation marks queue Jobs that translate a Book; their target
	// is the Translation.
	JobTypeTranslation = "TRANSLATION"
)

var (
	ErrNotFound     = errors.New("translation not found")
	ErrBookNotFound = errors.New("book not found")
	// ErrNotInReview means the Termbase cannot be edited or approved now: it
	// is still being extracted, or it is already locked.
	ErrNotInReview = errors.New("the Termbase is not in review")
)

type Service struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, queries: sqlc.New(pool)}
}

// Termbase is a Termbase with its Terms, most frequent first.
type Termbase struct {
	Language string
	Status   domain.TermbaseStatus
	Terms    []domain.Term
}

// Start creates a Translation of a Book into language at level. The first
// Translation into a language enqueues the extraction of its Termbase; later
// ones share that Termbase and wait for whatever it still needs, so a locked
// Termbase lets a Translation start translating at once.
func (s *Service) Start(ctx context.Context, bookID uuid.UUID, language string, level domain.Level) (domain.Translation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Translation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	// Locked against removal until this Translation is attached to it.
	book, err := q.GetBookForShare(ctx, bookID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Translation{}, ErrBookNotFound
	}
	if err != nil {
		return domain.Translation{}, err
	}
	language, err = domain.TargetLanguage(book.Language, language)
	if err != nil {
		return domain.Translation{}, err
	}

	termbase, err := q.CreateTermbase(ctx, sqlc.CreateTermbaseParams{
		ID: uuid.New(), BookID: bookID, Language: language, Status: string(domain.TermbaseExtracting),
	})
	switch {
	case err == nil:
		if err := enqueue(ctx, q, JobTypeExtraction, termbase.ID); err != nil {
			return domain.Translation{}, err
		}
	case errors.Is(err, pgx.ErrNoRows):
		// The Book already has a Termbase for this language. Lock it, so its
		// status cannot move while this Translation is attached to it.
		termbase, err = q.GetTermbaseByLanguageForUpdate(ctx, sqlc.GetTermbaseByLanguageForUpdateParams{BookID: bookID, Language: language})
		if err != nil {
			return domain.Translation{}, fmt.Errorf("loading termbase: %w", err)
		}
		if domain.TermbaseStatus(termbase.Status) == domain.TermbaseFailed {
			// Asking again is how the user retries a failed extraction.
			termbase.Status = string(domain.TermbaseExtracting)
			if err := q.UpdateTermbaseStatus(ctx, sqlc.UpdateTermbaseStatusParams{ID: termbase.ID, Status: termbase.Status}); err != nil {
				return domain.Translation{}, fmt.Errorf("restarting termbase: %w", err)
			}
			if err := enqueue(ctx, q, JobTypeExtraction, termbase.ID); err != nil {
				return domain.Translation{}, err
			}
		}
	default:
		return domain.Translation{}, fmt.Errorf("creating termbase: %w", err)
	}

	status := domain.TermbaseStatus(termbase.Status).StartingStatus()
	row, err := q.CreateTranslation(ctx, sqlc.CreateTranslationParams{
		ID:         uuid.New(),
		BookID:     bookID,
		TermbaseID: termbase.ID,
		Language:   language,
		Level:      string(level),
		Status:     string(status),
	})
	if err != nil {
		return domain.Translation{}, fmt.Errorf("creating translation: %w", err)
	}
	if status == domain.TranslationInProgress {
		if err := enqueue(ctx, q, JobTypeTranslation, row.ID); err != nil {
			return domain.Translation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Translation{}, err
	}
	return fromRow(row), nil
}

func enqueue(ctx context.Context, q *sqlc.Queries, jobType string, targetID uuid.UUID) error {
	_, err := q.CreateJob(ctx, sqlc.CreateJobParams{
		ID:       uuid.New(),
		TargetID: targetID,
		JobType:  jobType,
		// The Job continues the trace of the request that enqueued it.
		Payload: telemetry.Carrier(ctx),
	})
	if err != nil {
		return fmt.Errorf("enqueuing %s job: %w", jobType, err)
	}
	return nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Translation, error) {
	row, err := s.queries.GetTranslation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Translation{}, ErrNotFound
	}
	if err != nil {
		return domain.Translation{}, err
	}
	return fromRow(row), nil
}

// Progress is how far a Translation is: Blocks translated out of the Book's
// total, and how many of those are Flagged.
type Progress struct{ Translated, Flagged, Total int }

// Progress reports how many of the Book's Blocks a Translation has finished.
func (s *Service) Progress(ctx context.Context, id uuid.UUID) (Progress, error) {
	row, err := s.queries.GetTranslationProgress(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Progress{}, ErrNotFound
	}
	if err != nil {
		return Progress{}, err
	}
	return Progress{Translated: int(row.Translated), Flagged: int(row.Flagged), Total: int(row.Total)}, nil
}

// ReportedBlock is a translated Block the user should look at: it is Flagged,
// or it carries warnings, or both.
type ReportedBlock struct {
	Source      string
	Translation string
	// FlagReason is the check the translation still failed after every
	// retry; "" if the Block is not Flagged.
	FlagReason string
	Warnings   []string
	Position   int
	Chapter    int
}

// Report lists the Blocks of a Translation that are Flagged or carry
// warnings, in reading order.
func (s *Service) Report(ctx context.Context, id uuid.UUID) ([]ReportedBlock, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.queries.ListReportedBlocks(ctx, id)
	if err != nil {
		return nil, err
	}
	blocks := make([]ReportedBlock, len(rows))
	for i, r := range rows {
		blocks[i] = ReportedBlock{
			Position:    int(r.Position),
			Chapter:     int(r.Chapter),
			Source:      r.Source,
			Translation: r.Translation,
			FlagReason:  r.FlagReason.String,
			Warnings:    r.Warnings,
		}
	}
	return blocks, nil
}

// List returns every Translation, oldest first.
func (s *Service) List(ctx context.Context) ([]domain.Translation, error) {
	rows, err := s.queries.ListTranslations(ctx)
	return fromRows(rows), err
}

// ListByBook returns a Book's Translations, oldest first.
func (s *Service) ListByBook(ctx context.Context, bookID uuid.UUID) ([]domain.Translation, error) {
	rows, err := s.queries.ListTranslationsByBook(ctx, bookID)
	return fromRows(rows), err
}

// Termbase returns the Termbase of a Translation.
func (s *Service) Termbase(ctx context.Context, translationID uuid.UUID) (Termbase, error) {
	translation, err := s.Get(ctx, translationID)
	if err != nil {
		return Termbase{}, err
	}
	row, err := s.queries.GetTermbase(ctx, translation.TermbaseID)
	if err != nil {
		return Termbase{}, fmt.Errorf("loading termbase: %w", err)
	}
	return loadTermbase(ctx, s.queries, row)
}

func loadTermbase(ctx context.Context, q *sqlc.Queries, row sqlc.Termbase) (Termbase, error) {
	rows, err := q.ListTerms(ctx, row.ID)
	if err != nil {
		return Termbase{}, fmt.Errorf("loading terms: %w", err)
	}
	terms := make([]domain.Term, len(rows))
	for i, r := range rows {
		terms[i] = domain.Term{Source: r.Source, Rendering: r.Rendering, Kind: domain.TermKind(r.Kind), Occurrences: int(r.Occurrences)}
	}
	return Termbase{Language: row.Language, Status: domain.TermbaseStatus(row.Status), Terms: terms}, nil
}

// ReplaceTerms replaces the Terms of a Translation's Termbase with terms,
// which is how the user edits, adds and removes Terms during review. It
// fails with ErrNotInReview at any other time. Occurrences are counted here,
// whatever the caller sent.
//
// ponytail: the whole Book is read and searched once per Term while the
// Termbase is locked. Fine for a novel and a few hundred Terms; count in SQL
// or cache the text if review edits get slow.
func (s *Service) ReplaceTerms(ctx context.Context, translationID uuid.UUID, terms []domain.Term) (Termbase, error) {
	if err := domain.ValidateTerms(terms); err != nil {
		return Termbase{}, invalidTermsError{err}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Termbase{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	termbase, err := termbaseInReview(ctx, q, translationID)
	if err != nil {
		return Termbase{}, err
	}
	texts, err := bookTexts(ctx, q, termbase.BookID)
	if err != nil {
		return Termbase{}, err
	}
	for i := range terms {
		terms[i].Occurrences = domain.CountOccurrences(texts, terms[i].Source)
	}
	if err := storeTerms(ctx, q, termbase.ID, terms); err != nil {
		return Termbase{}, err
	}
	result, err := loadTermbase(ctx, q, termbase)
	if err != nil {
		return Termbase{}, err
	}
	return result, tx.Commit(ctx)
}

// invalidTermsError marks Terms the user must correct.
type invalidTermsError struct{ error }

// IsInvalidTerms reports whether err says the submitted Terms are not usable.
func IsInvalidTerms(err error) bool {
	var invalid invalidTermsError
	return errors.As(err, &invalid)
}

// Approve locks the Termbase of a Translation. Every Translation waiting on
// that Termbase starts translating: its Job is enqueued in the same
// transaction, so none is left waiting on a Job that was never queued.
func (s *Service) Approve(ctx context.Context, translationID uuid.UUID) (domain.Translation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Translation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	termbase, err := termbaseInReview(ctx, q, translationID)
	if err != nil {
		return domain.Translation{}, err
	}
	if err := q.UpdateTermbaseStatus(ctx, sqlc.UpdateTermbaseStatusParams{ID: termbase.ID, Status: string(domain.TermbaseLocked)}); err != nil {
		return domain.Translation{}, fmt.Errorf("locking termbase: %w", err)
	}
	started, err := q.MoveTranslations(ctx, sqlc.MoveTranslationsParams{
		TermbaseID: termbase.ID,
		FromStatus: string(domain.TranslationInReview),
		ToStatus:   string(domain.TranslationInProgress),
	})
	if err != nil {
		return domain.Translation{}, fmt.Errorf("starting translations: %w", err)
	}
	for _, id := range started {
		if err := enqueue(ctx, q, JobTypeTranslation, id); err != nil {
			return domain.Translation{}, err
		}
	}
	row, err := q.GetTranslation(ctx, translationID)
	if err != nil {
		return domain.Translation{}, err
	}
	return fromRow(row), tx.Commit(ctx)
}

// termbaseInReview locks the Termbase of a Translation for the transaction
// and fails with ErrNotInReview unless both are in review: a Translation that
// failed or was cancelled has no say over a Termbase others still share.
func termbaseInReview(ctx context.Context, q *sqlc.Queries, translationID uuid.UUID) (sqlc.Termbase, error) {
	translation, err := q.GetTranslation(ctx, translationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Termbase{}, ErrNotFound
	}
	if err != nil {
		return sqlc.Termbase{}, err
	}
	termbase, err := q.GetTermbaseForUpdate(ctx, translation.TermbaseID)
	if err != nil {
		return sqlc.Termbase{}, fmt.Errorf("loading termbase: %w", err)
	}
	// The Translation is read again under the lock: its status only moves
	// while the Termbase is locked.
	translation, err = q.GetTranslation(ctx, translationID)
	if err != nil {
		return sqlc.Termbase{}, err
	}
	if domain.TermbaseStatus(termbase.Status) != domain.TermbaseInReview ||
		domain.TranslationStatus(translation.Status) != domain.TranslationInReview {
		return sqlc.Termbase{}, ErrNotInReview
	}
	return termbase, nil
}

// bookTexts returns the text of a Book's chapters, one string per Block,
// without inline markers.
func bookTexts(ctx context.Context, q *sqlc.Queries, bookID uuid.UUID) ([]string, error) {
	rows, err := q.ListTextBlocks(ctx, bookID)
	if err != nil {
		return nil, fmt.Errorf("loading blocks: %w", err)
	}
	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = domain.PlainText(r.Source)
	}
	return texts, nil
}

func storeTerms(ctx context.Context, q *sqlc.Queries, termbaseID uuid.UUID, terms []domain.Term) error {
	if err := q.DeleteTerms(ctx, termbaseID); err != nil {
		return fmt.Errorf("clearing terms: %w", err)
	}
	rows := make([]sqlc.CreateTermsParams, len(terms))
	for i, t := range terms {
		rows[i] = sqlc.CreateTermsParams{
			TermbaseID:  termbaseID,
			Source:      t.Source,
			Rendering:   t.Rendering,
			Kind:        string(t.Kind),
			Occurrences: int32(t.Occurrences),
		}
	}
	if _, err := q.CreateTerms(ctx, rows); err != nil {
		return fmt.Errorf("storing terms: %w", err)
	}
	return nil
}

func fromRows(rows []sqlc.Translation) []domain.Translation {
	translations := make([]domain.Translation, len(rows))
	for i, row := range rows {
		translations[i] = fromRow(row)
	}
	return translations
}

func fromRow(row sqlc.Translation) domain.Translation {
	return domain.Translation{
		ID:            row.ID,
		BookID:        row.BookID,
		TermbaseID:    row.TermbaseID,
		Language:      row.Language,
		Level:         domain.Level(row.Level),
		Status:        domain.TranslationStatus(row.Status),
		FailureReason: row.FailureReason.String,
		CreatedAt:     row.CreatedAt.Time,
	}
}

func reason(text string) pgtype.Text { return pgtype.Text{String: text, Valid: text != ""} }
