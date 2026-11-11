package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/BrunoPereiradosSantos/raggman/internal/books"
	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/epub"
	"github.com/google/uuid"
)

type bookResponse struct {
	CreatedAt time.Time `json:"created_at"`
	Filename  string    `json:"filename"`
	Title     string    `json:"title"`
	Language  string    `json:"language,omitempty"`
	Blocks    int       `json:"blocks"`
	ID        uuid.UUID `json:"id"`
}

func toBookResponse(b domain.Book) bookResponse {
	return bookResponse{
		ID:        b.ID,
		Filename:  b.Filename,
		Title:     b.Title,
		Language:  b.Language,
		Blocks:    b.TotalBlocks,
		CreatedAt: b.CreatedAt,
	}
}

func (h *handler) submitBook(w http.ResponseWriter, r *http.Request) {
	filename, content, ok := readUpload(w, r, "EPUB")
	if !ok {
		return
	}

	book, created, err := h.books.Submit(r.Context(), filename, content)
	switch {
	case errors.Is(err, epub.ErrNotEPUB):
		writeError(w, http.StatusUnsupportedMediaType, "only EPUB files are accepted")
		return
	case errors.Is(err, epub.ErrDRM):
		writeError(w, http.StatusUnprocessableEntity,
			"this EPUB is protected by DRM: Raggman cannot read it and does not remove DRM")
		return
	case errors.Is(err, epub.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		internalError(w, r, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toBookResponse(book))
}

func (h *handler) listBooks(w http.ResponseWriter, r *http.Request) {
	list, err := h.books.List(r.Context())
	if err != nil {
		internalError(w, r, err)
		return
	}
	resp := make([]bookResponse, len(list))
	for i, b := range list {
		resp[i] = toBookResponse(b)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) getBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "book")
	if !ok {
		return
	}
	book, err := h.books.Get(r.Context(), id)
	if errors.Is(err, books.ErrNotFound) {
		writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toBookResponse(book))
}

func (h *handler) removeBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "book")
	if !ok {
		return
	}
	err := h.books.Remove(r.Context(), id)
	if errors.Is(err, books.ErrNotFound) {
		writeError(w, http.StatusNotFound, "book not found")
		return
	}
	if err != nil {
		internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
