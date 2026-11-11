package translation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/llm"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/BrunoPereiradosSantos/raggman/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// ponytail: extraction reads far more text per call than translation
	// will, since its reply is a short list. The size is a guess (about 1500
	// tokens at 4 characters each); set it from the measurement in #29.
	//
	// Measured on qwen3:8b with three Windows of a real novel: with thinking
	// on, a call takes about 3 minutes whatever the Window's size (3700 to
	// 5500 characters), so the number of Windows sets the running time; with
	// thinking off it takes 9 to 19 seconds, but the list fills with ordinary
	// phrases and misses frequent names.
	extractionWindowChars = 6000
	// extractionTries is how often one Window is asked again when the reply
	// cannot be read.
	extractionTries = 3
)

// Extraction runs Termbase extraction Jobs: it reads the whole Book, Window
// by Window, and proposes its Terms for the user to review.
type Extraction struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	chat    *llm.Client
	logger  *slog.Logger
	model   string
}

func NewExtraction(pool *pgxpool.Pool, env *environment.Environment, logger *slog.Logger) *Extraction {
	return &Extraction{
		pool:    pool,
		queries: sqlc.New(pool),
		chat:    llm.NewChat(env.LLMBaseURL, env.LLMAPIKey, env.LLMThinking),
		logger:  logger,
		model:   env.LLMModel,
	}
}

func (e *Extraction) Name() string { return "Termbase extraction" }

func (e *Extraction) TargetKey() string { return "termbase_id" }

// TargetGone reports whether the Termbase went away with its Book.
func (e *Extraction) TargetGone(ctx context.Context, job sqlc.JobQueue) (bool, error) {
	_, err := e.queries.GetTermbase(ctx, job.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// MarkFailed fails the Termbase and every Translation waiting on it.
func (e *Extraction) MarkFailed(ctx context.Context, q *sqlc.Queries, job sqlc.JobQueue, why string) error {
	err := q.UpdateTermbaseStatus(ctx, sqlc.UpdateTermbaseStatusParams{
		ID: job.TargetID, Status: string(domain.TermbaseFailed), FailureReason: reason(why),
	})
	if err != nil {
		return err
	}
	_, err = q.MoveTranslations(ctx, sqlc.MoveTranslationsParams{
		TermbaseID:    job.TargetID,
		FromStatus:    string(domain.TranslationExtracting),
		ToStatus:      string(domain.TranslationFailed),
		FailureReason: reason(why),
	})
	return err
}

// Work builds the Job's Termbase and puts it, and the Translations waiting
// on it, in review.
//
// ponytail: candidates are kept in memory until the whole Book is read, so an
// extraction interrupted by a restart starts over, and on a local model it
// runs for hours. Store candidates per Window once the extraction approach is
// settled by the measurement in #29.
//
// ponytail: only the chapters' text is read, not the title, the table of
// contents or image descriptions. Names live in the text; read the rest if a
// Book proves otherwise.
func (e *Extraction) Work(ctx context.Context, job sqlc.JobQueue) error {
	termbase, err := e.queries.GetTermbase(ctx, job.TargetID)
	if err != nil {
		return fmt.Errorf("loading termbase: %w", err)
	}
	if domain.TermbaseStatus(termbase.Status) != domain.TermbaseExtracting {
		// Already extracted by an earlier attempt: nothing left for this Job.
		return queue.Complete(ctx, e.queries, job)
	}
	rows, err := e.queries.ListTextBlocks(ctx, termbase.BookID)
	if err != nil {
		return fmt.Errorf("loading blocks: %w", err)
	}
	blocks := make([]domain.Block, len(rows))
	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = domain.PlainText(r.Source)
		blocks[i] = domain.Block{Chapter: int(r.Chapter), Source: texts[i]}
	}

	windows := domain.Windows(blocks, extractionWindowChars)
	system := extractionPrompt(termbase.Language)
	var candidates []domain.Candidate
	unread := 0
	for i, window := range windows {
		found, err := e.extract(ctx, system, window)
		if err != nil {
			return err
		}
		if found == nil {
			unread++
			e.logger.WarnContext(ctx, "No readable Terms for a Window; skipping it", "window", i+1, "windows", len(windows))
		}
		candidates = append(candidates, found...)
	}
	// A Window the model cannot answer for costs a few Terms, which the user
	// can add in review. A model that answers for none is not doing the job.
	if len(windows) > 0 && unread == len(windows) {
		return errors.New("the model returned no readable list of Terms for any part of the Book")
	}
	terms := domain.BuildTerms(candidates, texts)

	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := e.queries.WithTx(tx)

	if _, err := q.GetTermbaseForUpdate(ctx, termbase.ID); err != nil {
		return fmt.Errorf("locking termbase: %w", err)
	}
	if err := storeTerms(ctx, q, termbase.ID, terms); err != nil {
		return err
	}
	if err := q.UpdateTermbaseStatus(ctx, sqlc.UpdateTermbaseStatusParams{ID: termbase.ID, Status: string(domain.TermbaseInReview)}); err != nil {
		return fmt.Errorf("putting termbase in review: %w", err)
	}
	_, err = q.MoveTranslations(ctx, sqlc.MoveTranslationsParams{
		TermbaseID: termbase.ID,
		FromStatus: string(domain.TranslationExtracting),
		ToStatus:   string(domain.TranslationInReview),
	})
	if err != nil {
		return fmt.Errorf("putting translations in review: %w", err)
	}
	if err := queue.Complete(ctx, q, job); err != nil {
		return err // rolls back: a worker that lost its lock writes nothing
	}
	return tx.Commit(ctx)
}

// extract asks the model for the candidate Terms of one Window. It returns
// nil candidates, and no error, when the model's reply could not be read
// after every try; an error means the model could not be reached.
func (e *Extraction) extract(ctx context.Context, system string, window []domain.Block) ([]domain.Candidate, error) {
	sources := make([]string, len(window))
	for i, b := range window {
		sources[i] = b.Source
	}
	messages := []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: strings.Join(sources, "\n\n")},
	}
	for range extractionTries {
		reply, err := e.chat.ChatJSON(ctx, e.model, messages)
		if err != nil {
			return nil, fmt.Errorf("extracting terms: %w", err)
		}
		candidates, err := domain.ParseCandidates(reply)
		if err == nil {
			if candidates == nil {
				candidates = []domain.Candidate{}
			}
			return candidates, nil
		}
	}
	return nil, nil
}

func extractionPrompt(language string) string {
	return `You help a translator prepare a book for translation into ` + domain.LanguageName(language) + `.

From the passage the user sends, list every proper name, and every invented or specialised term, that must be written the same way throughout the translation.

For each one give:
- "source": the expression exactly as it is written in the passage.
- "type": "person" for the name of a person or character, "place" for the name of a place, "term" for anything else (invented words, titles, organisations, objects).
- "rendering": how it must be written in the translation. Names of people keep their spelling.

Rules:
- List only what is in the passage. Never add anything from outside it.
- Do not list ordinary words.

Answer with JSON only, in this shape:
{"terms":[{"source":"...","type":"person","rendering":"..."}]}
If there is nothing to list, answer {"terms":[]}.`
}
