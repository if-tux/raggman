package domain

import "strings"

// ponytail: sizes approximate 500/50 tokens as ~4 characters per token, since
// there is no reliable Go tokenizer for Qwen. Swap in a tokenizer if chunk
// size ever needs to be exact. Changing these means re-ingesting the Corpus.
const (
	chunkChars   = 2000
	overlapChars = 200
)

// Chunk is a passage of text from a single Page (ADR-0002).
type Chunk struct {
	Content string
	Page    int
}

// ChunkPages splits each Page's text into overlapping Chunks. pages[i] is Page
// i+1. Chunks never span Pages, and Pages without text yield no Chunks.
func ChunkPages(pages []string) []Chunk {
	var chunks []Chunk
	for i, text := range pages {
		runes := []rune(strings.TrimSpace(text))
		for start := 0; start < len(runes); start += chunkChars - overlapChars {
			end := min(start+chunkChars, len(runes))
			chunks = append(chunks, Chunk{Page: i + 1, Content: string(runes[start:end])})
			if end == len(runes) {
				break
			}
		}
	}
	return chunks
}
