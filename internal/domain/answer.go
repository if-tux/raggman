package domain

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// NotFoundAnswer is the Answer when the Corpus holds nothing relevant.
const NotFoundAnswer = "I could not find anything about this in the Corpus."

// markerPattern matches [1], [1, 3] and the full-width 【1】 some models emit.
var markerPattern = regexp.MustCompile(`[\[【](\d+(?:\s*,\s*\d+)*)[\]】]`)

// CitedMarkers returns the marker numbers (1-based) an Answer uses, like [1]
// or [1, 3], in first-cited order. Numbers outside 1..chunks are ignored, so a
// model cannot cite a Chunk it was not given.
// ponytail: a bracketed number in the Answer text, like a year [2], counts as
// a marker when in range; ranges like [1-3] are not recognised.
func CitedMarkers(answer string, chunks int) []int {
	var cited []int
	for _, m := range markerPattern.FindAllStringSubmatch(answer, -1) {
		for field := range strings.SplitSeq(m[1], ",") {
			n, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil || n < 1 || n > chunks || slices.Contains(cited, n) {
				continue
			}
			cited = append(cited, n)
		}
	}
	return cited
}

// Citation is a reference from an Answer to the Chunk behind one of its
// markers, locating that Chunk's Document and Page.
type Citation struct {
	Filename   string
	Marker     int
	Page       int
	ChunkID    uuid.UUID
	DocumentID uuid.UUID
}
