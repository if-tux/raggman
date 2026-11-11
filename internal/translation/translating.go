package translation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/environment"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/llm"
	"github.com/BrunoPereiradosSantos/raggman/internal/infra/sqlc"
	"github.com/BrunoPereiradosSantos/raggman/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ponytail: these are starting points, to be set from the measurement in #29.
const (
	// windowChars is the source a Window carries: about 600 tokens at 4
	// characters each, since there is no reliable Go tokenizer for Qwen.
	windowChars = 2400
	// blockCeilingChars is the size above which a Block is not sent whole:
	// long outputs are where a model drops text.
	blockCeilingChars = 2 * windowChars
	// contextBlocks is how many translated Blocks before a Window the model
	// sees, for continuity of tone and pronouns. Together they hold at most
	// contextChars of source, so a few long Blocks cannot crowd the Window
	// out of a local model's short context.
	contextBlocks = 3
	contextChars  = windowChars
	// translationTemperature is low and fixed: a translation should not vary
	// between runs.
	translationTemperature = 0.2
	// numberingTries is how often a Window is asked again when the reply does
	// not hold each numbered block once.
	// ponytail: after that the whole Translation fails, which ADR-0007 rules
	// out ("a Translation never fails because of one bad Block"). The retry
	// ladder of #25 replaces this: retry with the error, split, then Flag.
	numberingTries = 3
)

// Translating runs translation Jobs: it walks the Book Window by Window, in
// order, and stores each Window's Blocks as it goes (ADR-0005).
type Translating struct {
	pool    *pgxpool.Pool
	queries *sqlc.Queries
	chat    *llm.Client
	model   string
}

func NewTranslating(pool *pgxpool.Pool, env *environment.Environment) *Translating {
	return &Translating{
		pool:    pool,
		queries: sqlc.New(pool),
		chat:    llm.NewChat(env.LLMBaseURL, env.LLMAPIKey, env.LLMThinking),
		model:   env.LLMModel,
	}
}

func (t *Translating) Name() string { return "Translation" }

func (t *Translating) TargetKey() string { return "translation_id" }

// TargetGone reports whether the Translation went away with its Book.
func (t *Translating) TargetGone(ctx context.Context, job sqlc.JobQueue) (bool, error) {
	_, err := t.queries.GetTranslation(ctx, job.TargetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// MarkFailed fails the Translation. The Blocks it has translated stay.
func (t *Translating) MarkFailed(ctx context.Context, q *sqlc.Queries, job sqlc.JobQueue, why string) error {
	_, err := q.UpdateTranslationStatus(ctx, sqlc.UpdateTranslationStatusParams{
		ID:            job.TargetID,
		FromStatus:    string(domain.TranslationInProgress),
		ToStatus:      string(domain.TranslationFailed),
		FailureReason: reason(why),
	})
	return err
}

// Work translates every Block of the Job's Translation that has no
// translation yet, then marks the Translation Complete. Windows run strictly
// in sequence, because each one reads the translation of the Blocks before
// it, and each is committed when done: an interrupted Job loses at most the
// Window it was on.
func (t *Translating) Work(ctx context.Context, job sqlc.JobQueue) error {
	translation, err := t.queries.GetTranslation(ctx, job.TargetID)
	if err != nil {
		return fmt.Errorf("loading translation: %w", err)
	}
	if domain.TranslationStatus(translation.Status) != domain.TranslationInProgress {
		// Finished by an earlier attempt, or no longer wanted.
		return queue.Complete(ctx, t.queries, job)
	}
	termbaseRow, err := t.queries.GetTermbase(ctx, translation.TermbaseID)
	if err != nil {
		return fmt.Errorf("loading termbase: %w", err)
	}
	termbase, err := loadTermbase(ctx, t.queries, termbaseRow)
	if err != nil {
		return err
	}
	rows, err := t.queries.ListBlocks(ctx, translation.BookID)
	if err != nil {
		return fmt.Errorf("loading blocks: %w", err)
	}
	blocks := make([]domain.Block, len(rows))
	for i, r := range rows {
		blocks[i] = domain.Block{Position: int(r.Position), Chapter: int(r.Chapter), Kind: domain.BlockKind(r.Kind), Source: r.Source}
	}
	done, err := t.queries.ListTranslatedBlocks(ctx, translation.ID)
	if err != nil {
		return fmt.Errorf("loading translated blocks: %w", err)
	}
	// translated holds every finished Block by position, from earlier
	// attempts and from this one: it is both what to skip and the context.
	translated := make(map[int]string, len(blocks))
	for _, d := range done {
		translated[int(d.Position)] = d.Translation
	}

	prompt := translationPrompt(translation.Language, domain.Level(translation.Level))
	for _, window := range domain.Windows(blocks, windowChars) {
		var todo []domain.Block
		for _, b := range window {
			if _, ok := translated[b.Position]; !ok {
				todo = append(todo, b)
			}
		}
		if len(todo) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		results, err := t.translateWindow(ctx, prompt, termbase.Terms, todo, blocks, translated)
		if err != nil {
			return err
		}
		if err := t.storeWindow(ctx, job, translation, todo, results); err != nil {
			return err
		}
		for i, b := range todo {
			translated[b.Position] = results[i]
		}
	}

	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := t.queries.WithTx(tx)
	// A Translation that left TRANSLATING meanwhile keeps its status.
	_, err = q.UpdateTranslationStatus(ctx, sqlc.UpdateTranslationStatusParams{
		ID:         translation.ID,
		FromStatus: string(domain.TranslationInProgress),
		ToStatus:   string(domain.TranslationComplete),
	})
	if err != nil {
		return fmt.Errorf("completing translation: %w", err)
	}
	if err := queue.Complete(ctx, q, job); err != nil {
		return err // rolls back: a worker that lost its lock writes nothing
	}
	return tx.Commit(ctx)
}

// translateWindow returns the translation of each Block in todo, in order.
func (t *Translating) translateWindow(ctx context.Context, prompt string, terms []domain.Term, todo, blocks []domain.Block, translated map[int]string) ([]string, error) {
	results := make([]string, len(todo))
	var forModel []int // indexes into todo
	for i, b := range todo {
		if copied, ok := domain.WithoutModel(b.Source, terms); ok {
			results[i] = copied
		} else {
			forModel = append(forModel, i)
		}
	}
	if len(forModel) == 0 {
		return results, nil
	}
	before := contextBefore(blocks, translated, todo[0].Position)

	// A Block over the ceiling is alone in its Window. It goes to the model
	// part by part, each part seeing the ones before it, and the parts are
	// joined back into one Block.
	if only := todo[forModel[0]]; len(forModel) == 1 && utf8.RuneCountInString(only.Source) > blockCeilingChars {
		var joined []string
		for _, part := range domain.SplitSentences(only.Source, windowChars) {
			out, err := t.ask(ctx, prompt, terms, before, []string{strings.TrimSpace(part)})
			if err != nil {
				return nil, err
			}
			joined = append(joined, out[0])
			before = append(before, passage{source: strings.TrimSpace(part), translation: out[0]})
			before = before[max(0, len(before)-contextBlocks):]
		}
		results[forModel[0]] = strings.Join(joined, " ")
		return results, nil
	}

	sources := make([]string, len(forModel))
	for i, at := range forModel {
		sources[i] = todo[at].Source
	}
	out, err := t.ask(ctx, prompt, terms, before, sources)
	if err != nil {
		return nil, err
	}
	for i, at := range forModel {
		results[at] = out[i]
	}
	return results, nil
}

// passage is a stretch of the Book with its translation, shown to the model
// as context.
type passage struct{ source, translation string }

// contextBefore returns the last translated Blocks before position, as many
// as fit contextChars. They may belong to the previous chapter: a chapter
// opening still follows from what was just read.
//
// ponytail: scans the Blocks from the end for every Window. Keep an index of
// the last translated position if a Book is large enough for that to show.
func contextBefore(blocks []domain.Block, translated map[int]string, position int) []passage {
	var before []passage
	size := 0
	for i := len(blocks) - 1; i >= 0 && len(before) < contextBlocks; i-- {
		b := blocks[i]
		if b.Position >= position {
			continue
		}
		translation, ok := translated[b.Position]
		if !ok {
			continue
		}
		if size += utf8.RuneCountInString(b.Source); size > contextChars {
			break
		}
		before = append([]passage{{source: b.Source, translation: translation}}, before...)
	}
	return before
}

// ask sends sources to the model as numbered blocks and returns their
// translations, one for one. A reply that breaks the numbering is asked for
// again; one that keeps breaking it is an error.
func (t *Translating) ask(ctx context.Context, prompt string, terms []domain.Term, before []passage, sources []string) ([]string, error) {
	var user strings.Builder
	// Names are fixed to the letter; other Terms are fixed in meaning and may
	// inflect, as the target language requires (ADR-0006).
	used := domain.TermsIn(terms, sources)
	for _, list := range []struct {
		kind    domain.TermKind
		heading string
	}{
		{domain.TermName, "Names (write each one exactly as given, every time):"},
		{domain.TermTerm, "Terms (always use this rendering; it may inflect to fit the sentence):"},
	} {
		wrote := false
		for _, term := range used {
			if term.Kind != list.kind {
				continue
			}
			if !wrote {
				user.WriteString(list.heading + "\n")
				wrote = true
			}
			fmt.Fprintf(&user, "- %s => %s\n", term.Source, term.Rendering)
		}
		if wrote {
			user.WriteString("\n")
		}
	}
	if len(before) > 0 {
		user.WriteString("What comes just before, already translated. It is context only: do not translate it again.\n")
		for _, p := range before {
			fmt.Fprintf(&user, "<before>\n<source>%s</source>\n<translation>%s</translation>\n</before>\n", p.source, p.translation)
		}
		user.WriteString("\n")
	}
	user.WriteString("Translate these blocks:\n")
	user.WriteString(domain.FormatBlocks(sources))
	messages := []llm.Message{{Role: "system", Content: prompt}, {Role: "user", Content: user.String()}}

	var err error
	for range numberingTries {
		var reply string
		reply, err = t.chat.ChatText(ctx, t.model, messages, translationTemperature)
		if err != nil {
			return nil, fmt.Errorf("translating: %w", err)
		}
		var out []string
		if out, err = domain.ParseBlocks(reply, len(sources)); err == nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("translating: %w", err)
}

// storeWindow commits a Window's translations, unless this worker lost the Job.
func (t *Translating) storeWindow(ctx context.Context, job sqlc.JobQueue, translation sqlc.Translation, todo []domain.Block, results []string) error {
	tx, err := t.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := t.queries.WithTx(tx)

	if err := queue.Hold(ctx, q, job); err != nil {
		return err
	}
	for i, b := range todo {
		err := q.CreateTranslatedBlock(ctx, sqlc.CreateTranslatedBlockParams{
			TranslationID: translation.ID, Position: int32(b.Position), Translation: results[i],
		})
		if err != nil {
			return fmt.Errorf("storing translated block: %w", err)
		}
	}
	return tx.Commit(ctx)
}

var levelInstructions = map[domain.Level]string{
	domain.LevelFormal:   "Write in a formal register: complete sentences, no slang, formal forms of address.",
	domain.LevelInformal: "Write in an informal, conversational register, the way people speak every day.",
	domain.LevelChildren: "Write for children: short sentences and simple, common words. Keep every event and every line of dialogue.",
}

func translationPrompt(language string, level domain.Level) string {
	return `You are a literary translator. Translate the numbered blocks the user sends into ` + domain.LanguageName(language) + `.

Register: ` + levelInstructions[level] + `

Rules:
- Translate everything. Never add, remove or summarise content, whatever the register.
- Each block is one paragraph, heading or other piece of the book. Translate each block on its own; never merge or split blocks.
- Marks such as ⟦1⟧, ⟦/1⟧ and ⟦2/⟧ stand for formatting. Keep every one of them, unchanged, around the same words.
- When the user lists Names, write each one exactly as given, every time it appears.
- When the user lists Terms, always use the rendering given; it may inflect to fit the sentence.
- Text inside <before> is context that is already translated. Never translate it again.

Answer with the translated blocks only, numbered as you received them:
<block 1>
translation of block 1
</block>
<block 2>
translation of block 2
</block>`
}
