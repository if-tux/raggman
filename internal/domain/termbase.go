package domain

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TermKind says how strictly a Term's rendering is enforced.
type TermKind string

const (
	// TermName is a proper name: its rendering must appear exactly.
	TermName TermKind = "name"
	// TermTerm is any other fixed expression: its rendering may inflect.
	TermTerm TermKind = "term"
)

// Term is one entry of a Termbase: a source expression and its fixed rendering.
type Term struct {
	Source    string
	Rendering string
	Kind      TermKind
	// Occurrences is how often Source occurs in the Book's text.
	Occurrences int
}

// maxTermRunes keeps a model from passing a whole sentence off as a Term.
const maxTermRunes = 80

// ValidateTerms checks Terms a user submitted, and trims them.
func ValidateTerms(terms []Term) error {
	seen := make(map[string]bool, len(terms))
	for i := range terms {
		t := &terms[i]
		t.Source, t.Rendering = strings.TrimSpace(t.Source), strings.TrimSpace(t.Rendering)
		switch {
		case t.Source == "" || t.Rendering == "":
			return errors.New("every Term needs a source and a rendering")
		case utf8.RuneCountInString(t.Source) > maxTermRunes || utf8.RuneCountInString(t.Rendering) > maxTermRunes:
			return fmt.Errorf("the Term %q is longer than %d characters", t.Source, maxTermRunes)
		case t.Kind != TermName && t.Kind != TermTerm:
			return fmt.Errorf("the Term %q needs a type of name or term", t.Source)
		case seen[t.Source]:
			return fmt.Errorf("the Term %q is listed twice", t.Source)
		}
		seen[t.Source] = true
	}
	return nil
}

// Candidate is a Term as the model proposes it while reading the Book.
type Candidate struct {
	Source    string `json:"source"`
	Rendering string `json:"rendering"`
	// Type is "person", "place" or "term".
	Type string `json:"type"`
}

// ParseCandidates reads the model's reply to a Termbase extraction prompt:
// a JSON object {"terms": [...]}, possibly wrapped in prose or a code fence.
func ParseCandidates(reply string) ([]Candidate, error) {
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 || end < start {
		return nil, errors.New("the reply holds no JSON object")
	}
	var parsed struct {
		Terms *[]Candidate `json:"terms"`
	}
	if err := json.Unmarshal([]byte(reply[start:end+1]), &parsed); err != nil {
		return nil, fmt.Errorf("the reply is not valid JSON: %w", err)
	}
	if parsed.Terms == nil {
		return nil, errors.New(`the reply has no "terms" list`)
	}
	return *parsed.Terms, nil
}

// BuildTerms turns the candidates gathered across a Book into its Terms,
// most frequent first. texts is the Book's text, one string per Block.
//
// The model is not trusted with facts it can get wrong: a candidate that
// never occurs in the Book is dropped, occurrences are counted here, and a
// person's name keeps its source spelling whatever the model suggested.
func BuildTerms(candidates []Candidate, texts []string) []Term {
	seen := make(map[string]bool, len(candidates))
	var terms []Term
	for _, c := range candidates {
		source := strings.Join(strings.Fields(c.Source), " ")
		rendering := strings.Join(strings.Fields(c.Rendering), " ")
		if source == "" || seen[source] || utf8.RuneCountInString(source) > maxTermRunes ||
			utf8.RuneCountInString(rendering) > maxTermRunes {
			continue
		}
		seen[source] = true
		term := Term{Source: source, Rendering: rendering, Kind: TermTerm, Occurrences: CountOccurrences(texts, source)}
		switch strings.ToLower(strings.TrimSpace(c.Type)) {
		case "person":
			term.Kind, term.Rendering = TermName, source
		case "place":
			term.Kind = TermName
		}
		if term.Rendering == "" {
			term.Rendering = source
		}
		if term.Occurrences > 0 {
			terms = append(terms, term)
		}
	}
	slices.SortStableFunc(terms, func(a, b Term) int {
		return cmp.Or(cmp.Compare(b.Occurrences, a.Occurrences), cmp.Compare(a.Source, b.Source))
	})
	return terms
}

// CountOccurrences counts how often expr occurs in texts as whole words, so
// "Ann" is not counted inside "Anna".
func CountOccurrences(texts []string, expr string) int {
	if expr == "" {
		return 0
	}
	n := 0
	for _, text := range texts {
		for from := 0; ; {
			i := strings.Index(text[from:], expr)
			if i < 0 {
				break
			}
			start, end := from+i, from+i+len(expr)
			before, _ := utf8.DecodeLastRuneInString(text[:start])
			after, _ := utf8.DecodeRuneInString(text[end:])
			if !isWordRune(before) && !isWordRune(after) {
				n++
			}
			from = end
		}
	}
	return n
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

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
