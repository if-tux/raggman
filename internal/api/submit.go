package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
)

// maxSubmissionBytes caps the size of a submitted Document or Book.
const maxSubmissionBytes = 50 << 20

// readUpload reads the uploaded file of a submission, answering the request
// itself when there is none to use. format names what is expected, for the
// error message.
func readUpload(w http.ResponseWriter, r *http.Request, format string) (filename string, content []byte, ok bool) {
	// Leave room for multipart headers around the file itself.
	r.Body = http.MaxBytesReader(w, r.Body, maxSubmissionBytes+1<<20)

	filename, content, err := readFilePart(r)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge) || errors.Is(err, errTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "the "+format+" exceeds the 50 MB limit")
	case errors.Is(err, errMissingFile), errors.Is(err, errMissingFilename):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		writeError(w, http.StatusBadRequest, "malformed multipart/form-data request")
	}
	return filename, content, err == nil
}

func (h *handler) submitDocument(w http.ResponseWriter, r *http.Request) {
	filename, content, ok := readUpload(w, r, "PDF")
	if !ok {
		return
	}

	if !isPDF(content) {
		writeError(w, http.StatusUnsupportedMediaType, "only PDF files are accepted")
		return
	}

	doc, created, err := h.documents.Submit(r.Context(), filename, content)
	if err != nil {
		internalError(w, r, err)
		return
	}

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toResponse(doc))
}

var (
	errTooLarge        = errors.New("file too large")
	errMissingFile     = errors.New(`expected a multipart/form-data request with a "file" field`)
	errMissingFilename = errors.New(`the "file" field needs a filename`)
)

// readFilePart streams the multipart body and reads the "file" field.
func readFilePart(r *http.Request) (string, []byte, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return "", nil, errMissingFile
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return "", nil, errMissingFile
		}
		if err != nil {
			return "", nil, err
		}
		if part.FormName() != "file" {
			continue
		}
		if part.FileName() == "" {
			return "", nil, errMissingFilename
		}

		content, err := io.ReadAll(io.LimitReader(part, maxSubmissionBytes+1))
		if err != nil {
			return "", nil, err
		}
		if len(content) > maxSubmissionBytes {
			return "", nil, errTooLarge
		}
		return part.FileName(), content, nil
	}
}

// isPDF checks the PDF signature by content, not by extension. Like most
// readers, it tolerates junk before the header within the first 1024 bytes.
func isPDF(content []byte) bool {
	return bytes.Contains(content[:min(len(content), 1024)], []byte("%PDF-"))
}
