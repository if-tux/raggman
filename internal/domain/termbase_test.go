package domain

import (
	"errors"
	"slices"
	"testing"
)

func TestParseCandidatesReadsJSONWrappedInProseOrFences(t *testing.T) {
	want := []Candidate{{Source: "Mara", Rendering: "Mara", Type: "person"}}
	for name, reply := range map[string]string{
		"bare":   `{"terms":[{"source":"Mara","type":"person","rendering":"Mara"}]}`,
		"fenced": "```json\n{\"terms\":[{\"source\":\"Mara\",\"type\":\"person\",\"rendering\":\"Mara\"}]}\n```",
		"prose":  `Here is the list: {"terms":[{"source":"Mara","type":"person","rendering":"Mara"}]} Done.`,
	} {
		got, err := ParseCandidates(reply)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: got %+v, %v; want %+v", name, got, err, want)
		}
	}
	if got, err := ParseCandidates(`{"terms":[]}`); err != nil || len(got) != 0 {
		t.Errorf("empty list: got %+v, %v; want none", got, err)
	}
}

func TestParseCandidatesRejectsRepliesWithoutATermsList(t *testing.T) {
	for _, reply := range []string{"", "I cannot help with that.", `{"terms": "none"}`, `{"names":[]}`, `{"terms":[`} {
		if _, err := ParseCandidates(reply); err == nil {
			t.Errorf("ParseCandidates(%q) = nil error, want one", reply)
		}
	}
}

func TestBuildTermsCountsOrdersAndDistrustsTheModel(t *testing.T) {
	texts := []string{"Mara met Annabel in Port Varen.", "Mara left Port Varen. Mara kept the glimmerstone."}
	got := BuildTerms([]Candidate{
		{Source: "glimmerstone", Type: "term", Rendering: "pedra-lume"},
		{Source: "Port Varen", Type: "place", Rendering: "Porto Varen"},
		{Source: "Mara", Type: "person", Rendering: "Mára"},       // a person keeps the source spelling
		{Source: "Mara", Type: "place", Rendering: "Mara"},        // a repeat is ignored
		{Source: "Zorblat", Type: "person", Rendering: "Zorblat"}, // never occurs in the Book
		{Source: "Ann", Type: "person", Rendering: "Ann"},         // only inside "Annabel"
		{Source: "  Annabel ", Type: "PERSON", Rendering: ""},     // trimmed, type in any case
		{Source: "", Type: "term", Rendering: "nothing"},          // no source
		{Source: "varen", Type: "term", Rendering: "varen"},       // case matters
	}, texts)

	want := []Term{
		{Source: "Mara", Rendering: "Mara", Kind: TermName, Occurrences: 3},
		{Source: "Port Varen", Rendering: "Porto Varen", Kind: TermName, Occurrences: 2},
		{Source: "Annabel", Rendering: "Annabel", Kind: TermName, Occurrences: 1},
		{Source: "glimmerstone", Rendering: "pedra-lume", Kind: TermTerm, Occurrences: 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("BuildTerms =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestCountOccurrencesMatchesWholeWords(t *testing.T) {
	texts := []string{"Élan saw Élan's hat. Élanor did not.", "élan"}
	if got := CountOccurrences(texts, "Élan"); got != 2 {
		t.Fatalf("CountOccurrences = %d, want 2", got)
	}
}

func TestPlainTextDropsMarkers(t *testing.T) {
	got := PlainText("She said ⟦1⟧never⟦/1⟧.⟦2/⟧Then ⟦12⟧silence⟦/12⟧.")
	if want := "She said never.Then silence."; got != want {
		t.Fatalf("PlainText = %q, want %q", got, want)
	}
}

func TestWindowsRespectBudgetAndChapters(t *testing.T) {
	block := func(chapter int, source string) Block { return Block{Chapter: chapter, Source: source} }
	got := Windows([]Block{
		block(1, "aaaa"), block(1, "bbbb"), block(1, "cccc"), // 4 + 4 fit a budget of 10; the third does not
		block(1, "a block far over the budget"), // alone
		block(1, "dd"),
		block(2, "ee"), // a new chapter starts a new Window
	}, 10)

	want := [][]string{{"aaaa", "bbbb"}, {"cccc"}, {"a block far over the budget"}, {"dd"}, {"ee"}}
	if len(got) != len(want) {
		t.Fatalf("Windows = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range got {
		sources := make([]string, len(w))
		for j, b := range w {
			sources[j] = b.Source
		}
		if !slices.Equal(sources, want[i]) {
			t.Fatalf("Window %d = %q, want %q", i, sources, want[i])
		}
	}
}

func TestValidateTermsTrimsThemAndRejectsUnusableOnes(t *testing.T) {
	ok := []Term{{Source: " Mara ", Rendering: "Mara", Kind: TermName}, {Source: "glimmerstone", Rendering: "pedra-lume", Kind: TermTerm}}
	if err := ValidateTerms(ok); err != nil || ok[0].Source != "Mara" {
		t.Fatalf("valid Terms: err = %v, first source = %q", err, ok[0].Source)
	}
	for name, terms := range map[string][]Term{
		"no rendering": {{Source: "Mara", Rendering: " ", Kind: TermName}},
		"no source":    {{Source: "", Rendering: "Mara", Kind: TermName}},
		"bad type":     {{Source: "Mara", Rendering: "Mara", Kind: "person"}},
		"listed twice": {{Source: "Mara", Rendering: "Mara", Kind: TermName}, {Source: "Mara ", Rendering: "M", Kind: TermTerm}},
	} {
		if err := ValidateTerms(terms); err == nil {
			t.Errorf("%s: err = nil, want one", name)
		}
	}
}

func TestTargetLanguageIsCanonicalAndNeverTheBooksOwn(t *testing.T) {
	for _, c := range []struct {
		err                  error
		source, target, want string
	}{
		{nil, "en", "pt-BR", "pt-BR"},
		{nil, "en", "pt-br", "pt-BR"},
		{nil, "en-US", "fr", "fr"},
		{nil, "", "en", "en"},            // the Book declares no language
		{nil, "pt-PT", "pt-BR", "pt-BR"}, // regional variants differ
		{ErrSameLanguage, "en", "en", ""},
		{ErrSameLanguage, "en", "en-GB", ""},
		{ErrSameLanguage, "en-US", "en", ""},
		{ErrInvalidLanguage, "en", "brazilian", ""},
		{ErrInvalidLanguage, "en", "", ""},
		{ErrInvalidLanguage, "en", "und", ""},
	} {
		got, err := TargetLanguage(c.source, c.target)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("TargetLanguage(%q, %q) = %q, %v; want %q, %v", c.source, c.target, got, err, c.want, c.err)
		}
	}
}

func TestLanguageNameSpellsOutTheTag(t *testing.T) {
	if got := LanguageName("pt-BR"); got != "Brazilian Portuguese (pt-BR)" {
		t.Fatalf("LanguageName = %q", got)
	}
}

func TestParseLevelAcceptsOnlyTheThreeLevels(t *testing.T) {
	if l, err := ParseLevel("children"); err != nil || l != LevelChildren {
		t.Fatalf("ParseLevel(children) = %q, %v", l, err)
	}
	if _, err := ParseLevel("poetic"); !errors.Is(err, ErrUnknownLevel) {
		t.Fatalf("ParseLevel(poetic) err = %v, want ErrUnknownLevel", err)
	}
}
