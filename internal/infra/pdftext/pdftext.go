// Package pdftext extracts text from PDFs with poppler's pdftotext.
package pdftext

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Pages returns the text of each Page; pages[i] is Page i+1.
func Pages(ctx context.Context, content []byte) ([]string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pdftotext", "-enc", "UTF-8", "-", "-")
	cmd.Stdin = bytes.NewReader(content)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("pdftotext: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	// pdftotext ends every Page with a form feed, leaving an empty tail.
	pages := strings.Split(stdout.String(), "\f")
	if pages[len(pages)-1] == "" {
		pages = pages[:len(pages)-1]
	}
	return pages, nil
}
