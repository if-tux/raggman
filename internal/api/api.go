// Package api is Raggman's HTTP surface.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/documents"
	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type handler struct {
	pool      *pgxpool.Pool
	documents *documents.Service
}

func NewHandler(pool *pgxpool.Pool, docs *documents.Service) http.Handler {
	h := &handler{pool: pool, documents: docs}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("POST /documents", h.submitDocument)
	mux.HandleFunc("GET /documents", h.listDocuments)
	mux.HandleFunc("GET /documents/{id}", h.getDocument)
	mux.HandleFunc("DELETE /documents/{id}", h.removeDocument)

	return mux
}

func (h *handler) healthz(w http.ResponseWriter, r *http.Request) {
	if err := h.pool.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	_, _ = w.Write([]byte("ok"))
}

type documentResponse struct {
	CreatedAt     time.Time     `json:"created_at"`
	Filename      string        `json:"filename"`
	Status        domain.Status `json:"status"`
	FailureReason string        `json:"failure_reason,omitempty"`
	ID            uuid.UUID     `json:"id"`
}

func toResponse(d domain.Document) documentResponse {
	return documentResponse{
		ID:            d.ID,
		Filename:      d.Filename,
		Status:        d.Status,
		FailureReason: d.FailureReason,
		CreatedAt:     d.CreatedAt,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// documentID parses the {id} path value; malformed ids are treated as unknown.
func documentID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "document not found")
		return uuid.UUID{}, false
	}
	return id, true
}

func (h *handler) listDocuments(w http.ResponseWriter, r *http.Request) {
	docs, err := h.documents.List(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	resp := make([]documentResponse, len(docs))
	for i, d := range docs {
		resp[i] = toResponse(d)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) getDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := documentID(w, r)
	if !ok {
		return
	}
	doc, err := h.documents.Get(r.Context(), id)
	if errors.Is(err, documents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(doc))
}

func (h *handler) removeDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := documentID(w, r)
	if !ok {
		return
	}
	err := h.documents.Remove(r.Context(), id)
	if errors.Is(err, documents.ErrNotFound) {
		writeError(w, http.StatusNotFound, "document not found")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
