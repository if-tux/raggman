package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits are the thresholds of the checks a translated Block must pass.
type Limits struct {
	// MinRatio and MaxRatio bound the length of a translation relative to its
	// source, both without markers.
	MinRatio, MaxRatio float64
	// RatioMinRunes is the source length below which the ratio is not
	// checked: "No." into "Não." is a ratio of 1.3 and means nothing.
	RatioMinRunes int
	// EchoMinWords is the number of words from which a translation identical
	// to its source counts as untranslated. Below it the two may rightly be
	// equal: "Mr. Smith".
	EchoMinWords int
}

// ponytail: starting points, generous on purpose. English into Brazilian
// Portuguese measured 0.96 to 1.05 on a few Windows; set these from the
// measurement over a whole novel in #29. They suit languages of similar
// density only: English into Chinese would fall under the minimum. Make them
// depend on the language pair when a second pair is validated.
var DefaultLimits = Limits{MinRatio: 0.5, MaxRatio: 2.0, RatioMinRunes: 40, EchoMinWords: 3}

// Verify checks one translated Block without a model (ADR-0007). It returns
// the reason the translation must be rejected, or "" when it passes. The
// reason is written for the model as much as for the user: it is sent back
// with the retry.
func Verify(source, translation string, terms []Term, limits Limits) string {
	plainSource, plain := PlainText(source), PlainText(translation)

	if reason := verifyMarkers(source, translation); reason != "" {
		return reason
	}
	// ponytail: a name passes when its rendering appears once, however often
	// the source uses it, because a translation may rightly put a pronoun for
	// a repeat. So "Mara ... Maria" in one Block passes. Count occurrences if
	// the measurement in #29 shows names drifting inside a Block.
	//
	// ponytail: the rendering must match to the letter, case included. A
	// rendering that opens with a lower-case article fails at the start of a
	// sentence; compare without case there if a Termbase needs it.
	for _, t := range namesIn(plainSource, terms) {
		if CountOccurrences([]string{plain}, t.Rendering) == 0 {
			return fmt.Sprintf("the name %q must be written %q", t.Source, t.Rendering)
		}
	}
	if len(strings.Fields(plainSource)) >= limits.EchoMinWords && strings.TrimSpace(plain) == strings.TrimSpace(plainSource) {
		return "the block was not translated: it is identical to the source"
	}
	if n := utf8.RuneCountInString(plainSource); n >= limits.RatioMinRunes {
		ratio := float64(utf8.RuneCountInString(plain)) / float64(n)
		switch {
		case ratio < limits.MinRatio:
			return fmt.Sprintf("the translation is too short for its source (%.0f%% of its length): text is missing", ratio*100)
		case ratio > limits.MaxRatio:
			return fmt.Sprintf("the translation is too long for its source (%.0f%% of its length): text was added", ratio*100)
		}
	}
	return ""
}

// namesIn returns the name Terms that text mentions. A name inside a longer
// name does not count: "He left New York" mentions "New York", not "York".
func namesIn(text string, terms []Term) []Term {
	names := make([]Term, 0, len(terms))
	for _, t := range terms {
		if t.Kind == TermName && t.Source != "" {
			names = append(names, t)
		}
	}
	slices.SortStableFunc(names, func(a, b Term) int { return len(b.Source) - len(a.Source) })
	var found []Term
	for _, name := range names {
		if CountOccurrences([]string{text}, name.Source) == 0 {
			continue
		}
		found = append(found, name)
		// Taken: a shorter name is not looked for inside this one.
		text = strings.ReplaceAll(text, name.Source, " ")
	}
	return found
}

// verifyMarkers requires the translation to carry exactly the source's
// formatting markers, each pair opening before it closes.
func verifyMarkers(source, translation string) string {
	want, got := blockMarkerPattern.FindAllString(source, -1), blockMarkerPattern.FindAllString(translation, -1)
	for _, marker := range want {
		if !slices.Contains(got, marker) {
			return fmt.Sprintf("the formatting mark %s is missing", marker)
		}
	}
	slices.Sort(want)
	sorted := slices.Clone(got)
	slices.Sort(sorted)
	if !slices.Equal(want, sorted) {
		for _, marker := range got {
			if !slices.Contains(want, marker) || count(got, marker) > count(want, marker) {
				return fmt.Sprintf("the formatting mark %s appears more often than in the source", marker)
			}
		}
		return "the formatting marks differ from the source"
	}
	// Pairs must nest, as elements do: ⟦1⟧⟦2⟧…⟦/2⟧⟦/1⟧, never crossed.
	var open []string
	for _, marker := range got {
		switch {
		case strings.HasSuffix(marker, "/⟧"):
			// An element without content, such as ⟦1/⟧, neither opens nor closes.
		case strings.HasPrefix(marker, "⟦/"):
			opening := "⟦" + strings.TrimPrefix(marker, "⟦/")
			if len(open) == 0 || open[len(open)-1] != opening {
				return fmt.Sprintf("the formatting marks %s and %s are out of order", opening, marker)
			}
			open = open[:len(open)-1]
		default:
			open = append(open, marker)
		}
	}
	return ""
}

func count(list []string, item string) int {
	n := 0
	for _, s := range list {
		if s == item {
			n++
		}
	}
	return n
}

// Warnings lists what looks doubtful in an accepted translation but must not
// reject it (ADR-0006, ADR-0007): a Term of kind term whose rendering is
// absent, since it may have inflected, and a capitalised word from the middle
// of a source sentence that is gone, since it may be a name the Termbase
// lacks, or just "Monday".
func Warnings(source, translation string, terms []Term) []string {
	plainSource, plain := PlainText(source), PlainText(translation)
	var warnings []string
	for _, t := range terms {
		if t.Kind != TermTerm || CountOccurrences([]string{plainSource}, t.Source) == 0 {
			continue
		}
		// An inflected form still starts like the rendering ("reis" for
		// "rei"), so the rendering must open a word: "sereia" does not count.
		// ponytail: another word that starts the same way ("reino") still
		// passes. It only silences a warning; stem the words if that matters.
		if !startsAWord(strings.ToLower(plain), strings.ToLower(t.Rendering)) {
			warnings = append(warnings, fmt.Sprintf("the Term %q was not rendered as %q", t.Source, t.Rendering))
		}
	}
	for _, word := range capitalisedMidSentence(plainSource) {
		inTermbase := slices.ContainsFunc(terms, func(t Term) bool { return CountOccurrences([]string{t.Source}, word) > 0 })
		if !inTermbase && CountOccurrences([]string{plain}, word) == 0 {
			warnings = append(warnings, fmt.Sprintf("%q is not in the translation; if it is a name, add it to the Termbase", word))
		}
	}
	return warnings
}

// startsAWord reports whether prefix occurs in text at the start of a word.
func startsAWord(text, prefix string) bool {
	for from := 0; ; {
		i := strings.Index(text[from:], prefix)
		if i < 0 {
			return false
		}
		before, _ := utf8.DecodeLastRuneInString(text[:from+i])
		if !isWordRune(before) {
			return true
		}
		from += i + len(prefix)
	}
}

// word is a run of letters and digits, with hyphens or apostrophes inside:
// "R2D2", "Jean-Paul", "O'Brien". Anything else, an em dash included, parts
// two words.
var word = regexp.MustCompile(`[\p{L}\p{N}]+(?:[-'’][\p{L}\p{N}]+)*`)

// capitalisedMidSentence returns the distinct capitalised words of text that
// do not open a sentence, without a possessive ending ("Beaufort's" is
// "Beaufort"). It returns none for a title-cased Block ("The Lighthouse
// Keeper"), where capitals say nothing.
func capitalisedMidSentence(text string) []string {
	var found []string
	capitalised := 0
	matches := word.FindAllStringIndex(text, -1)
	end := 0
	for n, m := range matches {
		// A word opens a sentence when the Block starts with it, or when
		// sentence punctuation or an opening quote stands between it and the
		// word before: She said, “The door is open.”
		opensSentence := n == 0 || strings.ContainsAny(text[end:m[0]], ".!?…:“\"«‘")
		end = m[1]
		w := text[m[0]:m[1]]
		w = strings.TrimSuffix(strings.TrimSuffix(w, "'s"), "’s")
		first, _ := utf8.DecodeRuneInString(w)
		if !unicode.IsUpper(first) {
			continue
		}
		capitalised++
		if !opensSentence && utf8.RuneCountInString(w) > 1 && !slices.Contains(found, w) {
			found = append(found, w)
		}
	}
	if capitalised*2 > len(matches) {
		return nil
	}
	return found
}
