// Package domain holds Raggman's business rules, free of transport and storage.
// Vocabulary follows GLOSSARY.md.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Status is where a Document is in its Ingestion lifecycle.
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusIngesting Status = "INGESTING"
	StatusReady     Status = "READY"
	StatusFailed    Status = "FAILED"
)

var ErrInvalidTransition = errors.New("invalid Document state transition")

// Document is a PDF submitted to Raggman, identified by its content.
type Document struct {
	CreatedAt     time.Time
	FileHash      string
	Filename      string
	Status        Status
	FailureReason string
	ID            uuid.UUID
}

// NewDocument creates a Pending Document whose identity is the SHA-256 of content.
func NewDocument(filename string, content []byte) Document {
	sum := sha256.Sum256(content)
	return Document{
		ID:       uuid.New(),
		FileHash: hex.EncodeToString(sum[:]),
		Filename: filename,
		Status:   StatusPending,
	}
}

// StartIngestion moves a Pending Document to Ingesting.
func (d *Document) StartIngestion() error {
	return d.transition(StatusPending, StatusIngesting)
}

// MarkReady moves an Ingesting Document into the Corpus.
func (d *Document) MarkReady() error {
	return d.transition(StatusIngesting, StatusReady)
}

// MarkFailed ends Ingestion with a reason the user can read.
func (d *Document) MarkFailed(reason string) error {
	if err := d.transition(StatusIngesting, StatusFailed); err != nil {
		return err
	}
	d.FailureReason = reason
	return nil
}

// Resubmit handles the same content being submitted again. Only a Failed
// Document restarts Ingestion; any other state is left as it is. It reports
// whether Ingestion must be restarted.
func (d *Document) Resubmit() bool {
	if d.Status != StatusFailed {
		return false
	}
	d.Status = StatusPending
	d.FailureReason = ""
	return true
}

func (d *Document) transition(from, to Status) error {
	if d.Status != from {
		return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, d.Status, to)
	}
	d.Status = to
	return nil
}
