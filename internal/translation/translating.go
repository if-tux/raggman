package translation

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// attempts and from this one: what to skip. reference holds those fit to
	// be shown to the model as context, which leaves Flagged Blocks out: a
	// translation that failed its checks, or a source kept untranslated,
	// would teach the model the very mistake.
	translated := make(map[int]bool, len(blocks))
	reference := make(map[int]string, len(blocks))
	for _, d := range done {
		translated[int(d.Position)] = true
		if !d.Flagged {
			reference[int(d.Position)] = d.Translation
		}
	}

	prompt := translationPrompt(translation.Language, domain.Level(translation.Level))
	for _, window := range domain.Windows(blocks, windowChars) {
		var todo []domain.Block
		for _, b := range window {
			if !translated[b.Position] {
				todo = append(todo, b)
			}
		}
		if len(todo) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		outcomes, err := t.translateWindow(ctx, prompt, termbase.Terms, todo, blocks, reference)
		if err != nil {
			return err
		}
		if err := t.storeWindow(ctx, job, translation, todo, outcomes); err != nil {
			return err
		}
		for i, b := range todo {
			translated[b.Position] = true
			if outcomes[i].flag == "" {
				reference[b.Position] = outcomes[i].translation
			}
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

// outcome is what a Block ends up with: its translation, why it is Flagged
// if it is, and any warnings.
type outcome struct {
	translation string
	// flag is the check the translation still failed after every retry; ""
	// when it passed.
	flag     string
	warnings []string
}

// translateWindow returns the outcome of each Block in todo, in order. A
// Block the model cannot get right is Flagged, never an error: only a model
// that cannot be reached fails the Job (ADR-0007).
func (t *Translating) translateWindow(ctx context.Context, prompt string, terms []domain.Term, todo, blocks []domain.Block, reference map[int]string) ([]outcome, error) {
	outcomes := make([]outcome, len(todo))
	var forModel []int // indexes into todo
	for i, b := range todo {
		if copied, ok := domain.WithoutModel(b.Source, terms); ok {
			outcomes[i] = outcome{translation: copied}
		} else {
			forModel = append(forModel, i)
		}
	}
	if len(forModel) == 0 {
		return outcomes, nil
	}
	before := contextBefore(blocks, reference, todo[0].Position)

	// A Block over the ceiling is alone in its Window. It goes to the model
	// part by part, each part seeing the ones before it and climbing the
	// retry ladder on its own, and the parts are joined back into one Block.
	if only := todo[forModel[0]]; len(forModel) == 1 && utf8.RuneCountInString(only.Source) > blockCeilingChars {
		var joined []string
		var flag string
		for n, part := range domain.SplitSentences(only.Source, windowChars) {
			part = strings.TrimSpace(part)
			out, err := t.ladder(ctx, prompt, terms, before, []string{part})
			if err != nil {
				return nil, err
			}
			joined = append(joined, out[0].translation)
			if out[0].flag != "" && flag == "" {
				flag = fmt.Sprintf("in part %d of this long Block: %s", n+1, out[0].flag)
			}
			before = append(before, passage{source: part, translation: out[0].translation})
			before = before[max(0, len(before)-contextBlocks):]
		}
		translation := strings.Join(joined, " ")
		if flag == "" {
			// The parts passed one by one; the Block as a whole must too. A
			// name cut in two by a part boundary is only seen here.
			flag = domain.Verify(only.Source, translation, terms, domain.DefaultLimits)
		}
		outcomes[forModel[0]] = outcome{translation: translation, flag: flag, warnings: domain.Warnings(only.Source, translation, terms)}
		return outcomes, nil
	}

	sources := make([]string, len(forModel))
	for i, at := range forModel {
		sources[i] = todo[at].Source
	}
	out, err := t.ladder(ctx, prompt, terms, before, sources)
	if err != nil {
		return nil, err
	}
	for i, at := range forModel {
		out[i].warnings = domain.Warnings(sources[i], out[i].translation, terms)
		outcomes[at] = out[i]
	}
	return outcomes, nil
}

// ladder translates sources as one Window and climbs the retry ladder for
// whatever fails a check (ADR-0007):
//
//  1. the Window as it is;
//  2. the same Window again, with the failed checks described to the model;
//  3. each Block that still fails, alone;
//  4. a Block that fails even alone is Flagged, keeping its best attempt.
//
// A Block that passes at any rung keeps that translation. For a Block that
// never passes, the best attempt is the last translation the model returned
// for it, made after it was told what was wrong, and the reason kept is the
// check that translation failed. A Block the model never returned at all
// keeps its source text, so the Book stays whole.
func (t *Translating) ladder(ctx context.Context, prompt string, terms []domain.Term, before []passage, sources []string) ([]outcome, error) {
	kept := make([]outcome, len(sources))
	returned := make([]bool, len(sources)) // the model has returned this Block at least once
	passed := make([]bool, len(sources))
	// record keeps what one attempt made of source i. translation is "" when
	// the reply could not be read as numbered blocks; the Block then keeps
	// what it had, with the reason that goes with it.
	record := func(i int, translation, reason string) {
		switch {
		case passed[i]:
		case translation != "":
			kept[i] = outcome{translation: translation, flag: reason}
			returned[i], passed[i] = true, reason == ""
		case !returned[i]:
			kept[i].flag = reason
		}
	}
	allPassed := func() bool { return !slices.Contains(passed, false) }
	reasons := func() []string {
		r := make([]string, len(kept))
		for i := range kept {
			r[i] = kept[i].flag
		}
		return r
	}
	try := func(at []int, again string) (broken string, err error) {
		asked := make([]string, len(at))
		for k, i := range at {
			asked[k] = sources[i]
		}
		out, why, broken, err := t.attempt(ctx, prompt, terms, before, asked, again)
		if err != nil {
			return "", err
		}
		for k, i := range at {
			if broken != "" {
				record(i, "", broken)
			} else {
				record(i, out[k], why[k])
			}
		}
		return broken, nil
	}

	all := make([]int, len(sources))
	for i := range all {
		all[i] = i
	}
	broken, err := try(all, "")
	if err != nil {
		return nil, err
	}
	if !allPassed() {
		if broken, err = try(all, feedback(broken, reasons())); err != nil {
			return nil, err
		}
	}
	if !allPassed() && len(sources) > 1 {
		for i := range sources {
			if passed[i] {
				continue
			}
			if _, err := try([]int{i}, feedback(broken, []string{kept[i].flag})); err != nil {
				return nil, err
			}
		}
	}
	for i, source := range sources {
		if !returned[i] {
			kept[i].translation = source
		}
	}
	return kept, nil
}

// feedback tells the model why its previous answer was not accepted. broken
// is what was wrong with the answer as a whole ("" if it could be read);
// reasons holds, per block, the check it failed ("" if it passed).
func feedback(broken string, reasons []string) string {
	var b strings.Builder
	b.WriteString("\nYour previous answer was rejected:\n")
	if broken != "" {
		fmt.Fprintf(&b, "- %s\n", broken)
	}
	for i, reason := range reasons {
		if reason != "" && reason != broken {
			fmt.Fprintf(&b, "- block %d: %s\n", i+1, reason)
		}
	}
	b.WriteString("Translate the blocks again, correcting this.\n")
	return b.String()
}

// passage is a stretch of the Book with its translation, shown to the model
// as context.
type passage struct{ source, translation string }

// contextBefore returns the last Blocks before position that have a
// translation fit for reference, as many as fit contextChars. They may belong to the previous chapter: a chapter
// opening still follows from what was just read.
//
// ponytail: scans the Blocks from the end for every Window. Keep an index of
// the last translated position if a Book is large enough for that to show.
func contextBefore(blocks []domain.Block, reference map[int]string, position int) []passage {
	var before []passage
	size := 0
	for i := len(blocks) - 1; i >= 0 && len(before) < contextBlocks; i-- {
		b := blocks[i]
		if b.Position >= position {
			continue
		}
		translation, ok := reference[b.Position]
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

// attempt sends sources to the model once, as numbered blocks, and checks
// the reply without a model. It returns the translations, one per source,
// and for each the check it failed, "" if none. When the reply does not hold
// each numbered block exactly once there are no translations to return, and
// broken says what was wrong with it. An error means the model could not be
// reached. again, if set, is appended to the request: the feedback on an
// answer that was not accepted.
func (t *Translating) attempt(ctx context.Context, prompt string, terms []domain.Term, before []passage, sources []string, again string) (out, reasons []string, broken string, err error) {
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
	user.WriteString(again)
	messages := []llm.Message{{Role: "system", Content: prompt}, {Role: "user", Content: user.String()}}

	reply, err := t.chat.ChatText(ctx, t.model, messages, translationTemperature)
	if err != nil {
		return nil, nil, "", fmt.Errorf("translating: %w", err)
	}
	out, err = domain.ParseBlocks(reply, len(sources))
	if err != nil {
		return nil, nil, err.Error(), nil
	}
	reasons = make([]string, len(sources))
	for i, source := range sources {
		reasons[i] = domain.Verify(source, out[i], terms, domain.DefaultLimits)
	}
	return out, reasons, "", nil
}

// storeWindow commits a Window's translations, unless this worker lost the Job.
func (t *Translating) storeWindow(ctx context.Context, job sqlc.JobQueue, translation sqlc.Translation, todo []domain.Block, outcomes []outcome) error {
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
		warnings := outcomes[i].warnings
		if warnings == nil {
			warnings = []string{}
		}
		err := q.CreateTranslatedBlock(ctx, sqlc.CreateTranslatedBlockParams{
			TranslationID: translation.ID,
			Position:      int32(b.Position),
			Translation:   outcomes[i].translation,
			FlagReason:    reason(outcomes[i].flag),
			Warnings:      warnings,
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
