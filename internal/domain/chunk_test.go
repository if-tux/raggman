package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestShortPageBecomesOneChunk(t *testing.T) {
	chunks := ChunkPages([]string{"  Raggman answers Questions.\n\n"})

	want := []Chunk{{Page: 1, Content: "Raggman answers Questions."}}
	if len(chunks) != 1 || chunks[0] != want[0] {
		t.Fatalf("chunks = %+v, want %+v", chunks, want)
	}
}

func TestLongPageSplitsWithOverlap(t *testing.T) {
	page := strings.Repeat("abcdefghij", 300) // 3000 chars

	chunks := ChunkPages([]string{page})

	// Worked example: windows of 2000 chars, each starting 200 before the last ended.
	want := []Chunk{
		{Page: 1, Content: page[0:2000]},
		{Page: 1, Content: page[1800:3000]},
	}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(chunks), len(want))
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk %d = %d chars from %q…, want %d chars from %q…",
				i, len(chunks[i].Content), chunks[i].Content[:10], len(want[i].Content), want[i].Content[:10])
		}
	}
}

func TestChunksNeverSpanPages(t *testing.T) {
	chunks := ChunkPages([]string{"first page", "second page"})

	want := []Chunk{{Page: 1, Content: "first page"}, {Page: 2, Content: "second page"}}
	if len(chunks) != 2 || chunks[0] != want[0] || chunks[1] != want[1] {
		t.Fatalf("chunks = %+v, want %+v", chunks, want)
	}
}

func TestPagesWithoutTextAreSkipped(t *testing.T) {
	chunks := ChunkPages([]string{"", " \n\t ", "third page"})

	want := Chunk{Page: 3, Content: "third page"}
	if len(chunks) != 1 || chunks[0] != want {
		t.Fatalf("chunks = %+v, want only %+v", chunks, want)
	}
}

func TestChunksDoNotSplitMultibyteCharacters(t *testing.T) {
	page := strings.Repeat("ç", 2500)

	chunks := ChunkPages([]string{page})

	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c.Content) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
	}
	if n := utf8.RuneCountInString(chunks[1].Content); n != 700 {
		t.Fatalf("second chunk = %d characters, want 700 (2500 - 1800)", n)
	}
}
