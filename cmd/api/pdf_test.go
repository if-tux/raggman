package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// makePDF builds a minimal valid PDF with one Page per argument; an empty
// string makes a blank Page.
func makePDF(t *testing.T, pages ...string) []byte {
	t.Helper()

	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"", // Pages, filled below once Page object numbers are known
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var kids []string
	for i, text := range pages {
		page, contents := 4+2*i, 5+2*i
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
		stream := ""
		if text != "" {
			escaped := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(text)
			stream = fmt.Sprintf("BT /F1 10 Tf 40 800 Td (%s) Tj ET", escaped)
		}
		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contents),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}
