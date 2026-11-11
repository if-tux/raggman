package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/translation"
	"github.com/google/uuid"
)

type translationResponse struct {
	CreatedAt      time.Time                `json:"created_at"`
	TargetLanguage string                   `json:"target_language"`
	Level          domain.Level             `json:"level"`
	Status         domain.TranslationStatus `json:"status"`
	FailureReason  string                   `json:"failure_reason,omitempty"`
	ID             uuid.UUID                `json:"id"`
	BookID         uuid.UUID                `json:"book_id"`
}

func toTranslationResponse(t domain.Translation) translationResponse {
	return translationResponse{
		ID:             t.ID,
		BookID:         t.BookID,
		TargetLanguage: t.Language,
		Level:          t.Level,
		Status:         t.Status,
		FailureReason:  t.FailureReason,
		CreatedAt:      t.CreatedAt,
	}
}

func toTranslationResponses(translations []domain.Translation) []translationResponse {
	resp := make([]translationResponse, len(translations))
	for i, t := range translations {
		resp[i] = toTranslationResponse(t)
	}
	return resp
}

type termJSON struct {
	Source    string          `json:"source"`
	Rendering string          `json:"rendering"`
	Type      domain.TermKind `json:"type"`
	// Occurrences is counted by Raggman; a value sent by a client is ignored.
	Occurrences int `json:"occurrences"`
}

type termbaseResponse struct {
	TargetLanguage string                `json:"target_language"`
	Status         domain.TermbaseStatus `json:"status"`
	Terms          []termJSON            `json:"terms"`
}

func toTermbaseResponse(t translation.Termbase) termbaseResponse {
	terms := make([]termJSON, len(t.Terms))
	for i, term := range t.Terms {
		terms[i] = termJSON{Source: term.Source, Rendering: term.Rendering, Type: term.Kind, Occurrences: term.Occurrences}
	}
	return termbaseResponse{TargetLanguage: t.Language, Status: t.Status, Terms: terms}
}

// bookTranslations returns the response for a Book with its Translations.
func (h *handler) bookTranslations(ctx context.Context, book domain.Book) (bookResponse, error) {
	translations, err := h.translations.ListByBook(ctx, book.ID)
	if err != nil {
		return bookResponse{}, err
	}
	return toBookResponse(book, translations), nil
}

func (h *handler) startTranslation(w http.ResponseWriter, r *http.Request) {
	bookID, ok := pathID(w, r, "book")
	if !ok {
		return
	}
	var req struct {
		TargetLanguage string `json:"target_language"`
		Level          string `json:"level"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `expected a JSON body with "target_language" and "level"`)
		return
	}
	level, err := domain.ParseLevel(req.Level)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	t, err := h.translations.Start(r.Context(), bookID, req.TargetLanguage, level)
	switch {
	case errors.Is(err, translation.ErrBookNotFound):
		writeError(w, http.StatusNotFound, "book not found")
	case errors.Is(err, domain.ErrInvalidLanguage), errors.Is(err, domain.ErrSameLanguage):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		internalError(w, r, err)
	default:
		writeJSON(w, http.StatusCreated, toTranslationResponse(t))
	}
}

func (h *handler) getTranslation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "translation")
	if !ok {
		return
	}
	t, err := h.translations.Get(r.Context(), id)
	if h.translationError(w, r, err) {
		return
	}
	progress, err := h.translations.Progress(r.Context(), id)
	if h.translationError(w, r, err) {
		return
	}
	// Progress is reported here only; lists of Translations leave it out.
	writeJSON(w, http.StatusOK, struct {
		translationResponse
		BlocksTranslated int `json:"blocks_translated"`
		BlocksFlagged    int `json:"blocks_flagged"`
		BlocksTotal      int `json:"blocks_total"`
	}{toTranslationResponse(t), progress.Translated, progress.Flagged, progress.Total})
}

// reportedBlock is a Block in a Translation's report. Position is its place
// in the Book, Chapter its chapter (0 for the title and table of contents).
type reportedBlock struct {
	Source      string `json:"source"`
	Translation string `json:"translation"`
	Reason      string `json:"reason,omitempty"`
	Position    int    `json:"position"`
	Chapter     int    `json:"chapter"`
}

// groupedWarning is one warning with every Block it was raised for. The same
// word tends to be reported Block after Block ("France" in every chapter set
// there), so the report has one line per warning, not one per Block.
type groupedWarning struct {
	Warning string `json:"warning"`
	// Positions are the Blocks the warning was raised for, in reading order.
	Positions []int `json:"positions"`
	// Example is the first Block the warning was raised for.
	Example reportedBlock `json:"example"`
	// Blocks is how many Blocks that is.
	Blocks int `json:"blocks"`
}

// getReport lists what the user should check in a Translation: the Flagged
// Blocks, each with the reason, and the warnings, grouped so that each one
// appears once, the most frequent first.
func (h *handler) getReport(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "translation")
	if !ok {
		return
	}
	blocks, err := h.translations.Report(r.Context(), id)
	if h.translationError(w, r, err) {
		return
	}
	report := struct {
		Flagged  []reportedBlock  `json:"flagged"`
		Warnings []groupedWarning `json:"warnings"`
	}{Flagged: []reportedBlock{}, Warnings: []groupedWarning{}}
	group := map[string]int{} // warning text → index in report.Warnings
	for _, b := range blocks {
		entry := reportedBlock{Position: b.Position, Chapter: b.Chapter, Source: b.Source, Translation: b.Translation}
		if b.FlagReason != "" {
			flagged := entry
			flagged.Reason = b.FlagReason
			report.Flagged = append(report.Flagged, flagged)
		}
		for _, warning := range b.Warnings {
			at, seen := group[warning]
			if !seen {
				at = len(report.Warnings)
				group[warning] = at
				report.Warnings = append(report.Warnings, groupedWarning{Warning: warning, Example: entry})
			}
			report.Warnings[at].Positions = append(report.Warnings[at].Positions, b.Position)
			report.Warnings[at].Blocks++
		}
	}
	// Most Blocks first; among equals, the one met first in the Book.
	slices.SortStableFunc(report.Warnings, func(a, b groupedWarning) int { return b.Blocks - a.Blocks })
	writeJSON(w, http.StatusOK, report)
}

func (h *handler) getTermbase(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "translation")
	if !ok {
		return
	}
	termbase, err := h.translations.Termbase(r.Context(), id)
	if h.translationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toTermbaseResponse(termbase))
}

func (h *handler) replaceTerms(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "translation")
	if !ok {
		return
	}
	var req struct {
		Terms *[]termJSON `json:"terms"`
	}
	// A Termbase is a few hundred short Terms; 4 MB is far above any real one.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil || req.Terms == nil {
		writeError(w, http.StatusBadRequest, `expected a JSON body with a "terms" list`)
		return
	}
	terms := make([]domain.Term, len(*req.Terms))
	for i, t := range *req.Terms {
		terms[i] = domain.Term{Source: t.Source, Rendering: t.Rendering, Kind: t.Type}
	}

	termbase, err := h.translations.ReplaceTerms(r.Context(), id, terms)
	if translation.IsInvalidTerms(err) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.translationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toTermbaseResponse(termbase))
}

func (h *handler) approveTermbase(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "translation")
	if !ok {
		return
	}
	t, err := h.translations.Approve(r.Context(), id)
	if h.translationError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, toTranslationResponse(t))
}

// translationError answers the request when err is not nil, and reports
// whether it did.
func (h *handler) translationError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, translation.ErrNotFound):
		writeError(w, http.StatusNotFound, "translation not found")
	case errors.Is(err, translation.ErrNotInReview):
		writeError(w, http.StatusConflict, "the Termbase is not in review: it is still being extracted, or already approved")
	default:
		internalError(w, r, err)
	}
	return true
}
