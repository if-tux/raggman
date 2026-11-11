package domain

import (
	"errors"
	"testing"
)

func TestNewDocumentIsPending(t *testing.T) {
	d := NewDocument("report.pdf", []byte("%PDF-1.7 content"))

	if d.Status != StatusPending {
		t.Fatalf("status = %s, want PENDING", d.Status)
	}
	// SHA-256 of "%PDF-1.7 content", computed independently with shasum -a 256.
	const want = "d274f10f823f4da5c383bedc6bf03b4aed26b05f8306cf082b8402ae78a456a5"
	if d.FileHash != want {
		t.Fatalf("file hash = %q, want %q", d.FileHash, want)
	}
}

func TestDocumentLifecycle(t *testing.T) {
	d := NewDocument("a.pdf", []byte("%PDF-x"))

	if err := d.StartIngestion(); err != nil {
		t.Fatalf("PENDING → INGESTING: %v", err)
	}
	if err := d.MarkReady(); err != nil {
		t.Fatalf("INGESTING → READY: %v", err)
	}
	if d.Status != StatusReady {
		t.Fatalf("status = %s, want READY", d.Status)
	}
}

func TestFailedDocumentCarriesReason(t *testing.T) {
	d := NewDocument("a.pdf", []byte("%PDF-x"))
	_ = d.StartIngestion()

	if err := d.MarkFailed("no extractable text"); err != nil {
		t.Fatalf("INGESTING → FAILED: %v", err)
	}
	if d.Status != StatusFailed || d.FailureReason != "no extractable text" {
		t.Fatalf("got %s %q, want FAILED with reason", d.Status, d.FailureReason)
	}
}

func TestInvalidTransitionsAreRejected(t *testing.T) {
	d := NewDocument("a.pdf", []byte("%PDF-x"))

	if err := d.MarkReady(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("PENDING → READY: err = %v, want ErrInvalidTransition", err)
	}
	if err := d.MarkFailed("x"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("PENDING → FAILED: err = %v, want ErrInvalidTransition", err)
	}
	if d.Status != StatusPending {
		t.Fatalf("status changed to %s on rejected transition", d.Status)
	}
}

func TestResubmitRestartsOnlyFailedDocuments(t *testing.T) {
	tests := []struct {
		arrange     func(*Document)
		name        string
		wantStatus  Status
		wantRestart bool
	}{
		{name: "pending stays pending", arrange: func(*Document) {}, wantRestart: false, wantStatus: StatusPending},
		{name: "ingesting stays ingesting", arrange: func(d *Document) { _ = d.StartIngestion() }, wantRestart: false, wantStatus: StatusIngesting},
		{name: "ready stays ready", arrange: func(d *Document) { _ = d.StartIngestion(); _ = d.MarkReady() }, wantRestart: false, wantStatus: StatusReady},
		{name: "failed goes back to pending", arrange: func(d *Document) { _ = d.StartIngestion(); _ = d.MarkFailed("timeout") }, wantRestart: true, wantStatus: StatusPending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDocument("a.pdf", []byte("%PDF-x"))
			tt.arrange(&d)

			if got := d.Resubmit(); got != tt.wantRestart {
				t.Fatalf("Resubmit() = %v, want %v", got, tt.wantRestart)
			}
			if d.Status != tt.wantStatus {
				t.Fatalf("status = %s, want %s", d.Status, tt.wantStatus)
			}
			if tt.wantRestart && d.FailureReason != "" {
				t.Fatalf("failure reason %q not cleared", d.FailureReason)
			}
		})
	}
}
