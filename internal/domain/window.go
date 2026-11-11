package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// sentenceEnd finds where a sentence stops: closing punctuation, any closing
// quotes or brackets (and inline markers) after it, then a space.
var sentenceEnd = regexp.MustCompile(`[.!?…]+(?:["'”’»)\]]|⟦/?\d+/?⟧)*\s+`)

var blockMarkerPattern = regexp.MustCompile(`⟦/?\d+/?⟧`)

// PlainText is a Block's source without its inline markers.
func PlainText(source string) string {
	return blockMarkerPattern.ReplaceAllString(source, "")
}

// Windows groups consecutive Blocks of one chapter into Windows of at most
// budget characters of source. A Window never crosses a chapter, and a Block
// larger than the budget gets a Window of its own.
func Windows(blocks []Block, budget int) [][]Block {
	var windows [][]Block
	size := 0
	for _, b := range blocks {
		n := utf8.RuneCountInString(b.Source)
		last := len(windows) - 1
		if last < 0 || windows[last][0].Chapter != b.Chapter || size+n > budget {
			windows = append(windows, nil)
			last, size = last+1, 0
		}
		windows[last] = append(windows[last], b)
		size += n
	}
	return windows
}

// SplitSentences cuts a Block's source into parts of at most budget
// characters, breaking only between sentences, so a Block too long for one
// model call can still be translated in full. Joining the parts gives the
// source back.
//
// ponytail: text with no sentence punctuation followed by a space (one very
// long sentence, verse held together by line breaks, Chinese or Japanese)
// stays one part, however long. Split at clause or line boundaries if a Book
// has such Blocks and the model drops text from them.
//
// ponytail: a marker pair that opens in one part and closes in another
// (a long Block set wholly in italics) reaches the model unbalanced in each
// part. The joined Block is what the marker check of #25 verifies.
func SplitSentences(source string, budget int) []string {
	var parts []string
	var part strings.Builder
	size := 0
	add := func(sentence string) {
		n := utf8.RuneCountInString(sentence)
		if size > 0 && size+n > budget {
			parts = append(parts, part.String())
			part.Reset()
			size = 0
		}
		part.WriteString(sentence)
		size += n
	}
	rest := source
	for {
		loc := sentenceEnd.FindStringIndex(rest)
		if loc == nil {
			break
		}
		add(rest[:loc[1]])
		rest = rest[loc[1]:]
	}
	if rest != "" {
		add(rest)
	}
	if size > 0 {
		parts = append(parts, part.String())
	}
	return parts
}

// FormatBlocks writes sources as the numbered blocks a translation prompt
// carries; the model must answer in the same form (ADR-0005).
func FormatBlocks(sources []string) string {
	var b strings.Builder
	for i, s := range sources {
		fmt.Fprintf(&b, "<block %d>\n%s\n</block>\n", i+1, s)
	}
	return b.String()
}

var (
	numberedBlock = regexp.MustCompile(`(?s)<block (\d+)>(.*?)</block>`)
	// Spaces and line breaks the model lays a reply out with. A non-breaking
	// space is typography, not layout, and is kept.
	layoutSpace = regexp.MustCompile(`[ \t\r\n]+`)
)

// ErrNumbering means the model did not return the blocks it was given, one
// for one: a block is missing, repeated, never asked for, or text sits
// outside the blocks.
var ErrNumbering = errors.New("the reply does not hold each numbered block exactly once")

// ParseBlocks reads the model's reply to n numbered blocks. A dropped or
// merged Block shows up here as a structural error, without reading the text
// (ADR-0005). So does anything written outside the blocks: it may be part of
// a translation that fell out of its block, and is never thrown away quietly.
//
// ponytail: a Block whose own text holds "</block>" or "<before>" breaks the
// prompt. The reply then fails here and is never stored wrong; escape such
// text if a Book (a manual about markup, say) ever needs it.
func ParseBlocks(reply string, n int) ([]string, error) {
	matches := numberedBlock.FindAllStringSubmatch(reply, -1)
	if len(matches) != n {
		return nil, fmt.Errorf("%w: %d blocks for %d sent", ErrNumbering, len(matches), n)
	}
	if outside := strings.TrimSpace(numberedBlock.ReplaceAllString(reply, "")); outside != "" {
		return nil, fmt.Errorf("%w: text outside the blocks", ErrNumbering)
	}
	blocks := make([]string, n)
	seen := make([]bool, n)
	for _, m := range matches {
		number, err := strconv.Atoi(m[1])
		if err != nil || number < 1 || number > n || seen[number-1] {
			return nil, fmt.Errorf("%w: unexpected block %s", ErrNumbering, m[1])
		}
		seen[number-1] = true
		blocks[number-1] = strings.Trim(layoutSpace.ReplaceAllString(m[2], " "), " ")
		if blocks[number-1] == "" {
			return nil, fmt.Errorf("%w: block %d is empty", ErrNumbering, number)
		}
	}
	return blocks, nil
}

// TermsIn returns the Terms whose source occurs in any of the sources, so a
// prompt carries only the Terms its Window needs.
func TermsIn(terms []Term, sources []string) []Term {
	texts := make([]string, len(sources))
	for i, s := range sources {
		texts[i] = PlainText(s)
	}
	var found []Term
	for _, t := range terms {
		if CountOccurrences(texts, t.Source) > 0 {
			found = append(found, t)
		}
	}
	return found
}

// WithoutModel returns the translation of a Block that needs no model: one
// with no letters (a number, a scene break), or one made only of names from
// the Termbase, each replaced by its rendering. ok is false for any other
// Block, including one where a marker splits a name: only the model can
// place the marker in the rendering.
func WithoutModel(source string, terms []Term) (translation string, ok bool) {
	names := make([]Term, 0, len(terms))
	for _, t := range terms {
		if t.Kind == TermName && t.Source != "" {
			names = append(names, t)
		}
	}
	// Longest first, so "Port Varen" is taken before "Varen".
	slices.SortStableFunc(names, func(a, b Term) int { return len(b.Source) - len(a.Source) })

	// One pass over the source: a rendering already written is never read
	// again, so one name cannot be replaced inside another's rendering.
	var out, rest strings.Builder
	for i := 0; i < len(source); {
		name := slices.IndexFunc(names, func(t Term) bool {
			return strings.HasPrefix(source[i:], t.Source) && isWholeWord(source, i, i+len(t.Source))
		})
		if name >= 0 {
			out.WriteString(names[name].Rendering)
			i += len(names[name].Source)
			continue
		}
		r, size := utf8.DecodeRuneInString(source[i:])
		out.WriteRune(r)
		rest.WriteRune(r)
		i += size
	}
	if strings.ContainsFunc(PlainText(rest.String()), unicode.IsLetter) {
		return "", false
	}
	return out.String(), true
}
