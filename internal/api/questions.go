package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/BrunoPereiradosSantos/raggman/internal/infra/llm"
	"github.com/google/uuid"
)

// maxQuestionChars bounds a Question; longer input is almost surely a paste
// of a Document, which belongs in POST /documents.
// ponytail: fixed limit; make it configurable if real Questions hit it.
const maxQuestionChars = 2000

type citationResponse struct {
	Filename   string    `json:"filename"`
	Marker     int       `json:"marker"`
	Page       int       `json:"page"`
	ChunkID    uuid.UUID `json:"chunk_id"`
	DocumentID uuid.UUID `json:"document_id"`
}

// askQuestion streams the Answer as Server-Sent Events: an optional
// "thinking" event while the model reasons, "token" events carrying Answer
// text, then one "citations" event listing the Citations used. If generation
// fails after streaming started, an "error" event replaces "citations";
// before that, the response is a plain 502.
func (h *handler) askQuestion(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, `expected JSON like {"question": "..."}`)
		return
	}
	question := strings.TrimSpace(req.Question)
	if question == "" {
		writeError(w, http.StatusBadRequest, "question must not be empty")
		return
	}
	if utf8.RuneCountInString(question) > maxQuestionChars {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("question exceeds %d characters", maxQuestionChars))
		return
	}

	flusher := http.NewResponseController(w)
	streaming := false
	// send starts the event stream on first use, so failures before any event
	// (embedding, search) can still answer with an error status.
	send := func(event string, payload any) error {
		if !streaming {
			streaming = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		return flusher.Flush()
	}

	citations, err := h.answering.Answer(r.Context(), question, llm.Stream{
		Thinking: func() error { return send("thinking", struct{}{}) },
		Token:    func(token string) error { return send("token", map[string]string{"text": token}) },
	})
	if err != nil {
		if r.Context().Err() != nil {
			// The client left, or shutdown ran out of time; nothing to report.
			slog.InfoContext(r.Context(), "Answering stopped", "reason", context.Cause(r.Context()))
		} else {
			slog.ErrorContext(r.Context(), "Answering failed", "error", err)
		}
		if !streaming {
			writeError(w, http.StatusBadGateway, "the Answer could not be generated")
			return
		}
		_ = send("error", map[string]string{"error": "the Answer could not be generated"})
		return
	}

	resp := make([]citationResponse, len(citations))
	for i, c := range citations {
		resp[i] = citationResponse{
			Marker: c.Marker, ChunkID: c.ChunkID, DocumentID: c.DocumentID, Filename: c.Filename, Page: c.Page,
		}
	}
	_ = send("citations", map[string]any{"citations": resp})
}
