package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSplitSentencesBreaksOnlyBetweenSentences(t *testing.T) {
	source := `Mara lit the lamp. "Who is there?" she asked. Nobody answered… The sea was calm.`
	got := SplitSentences(source, 30)

	want := []string{`Mara lit the lamp. `, `"Who is there?" she asked. `, `Nobody answered… `, `The sea was calm.`}
	if !slices.Equal(got, want) {
		t.Fatalf("SplitSentences =\n  %q\nwant\n  %q", got, want)
	}
	if strings.Join(got, "") != source {
		t.Fatalf("joined parts differ from the source")
	}
}

func TestSplitSentencesPacksSentencesUpToTheBudget(t *testing.T) {
	got := SplitSentences("One. Two. Three. Four.", 11)

	if want := []string{"One. Two. ", "Three. ", "Four."}; !slices.Equal(got, want) {
		t.Fatalf("SplitSentences = %q, want %q", got, want)
	}
}

func TestSplitSentencesKeepsMarkersAndASentenceOverTheBudgetWhole(t *testing.T) {
	source := "She said ⟦1⟧never.⟦/1⟧ A sentence far longer than the budget allows stays in one part. End."
	got := SplitSentences(source, 20)

	want := []string{"She said ⟦1⟧never.⟦/1⟧ ", "A sentence far longer than the budget allows stays in one part. ", "End."}
	if !slices.Equal(got, want) {
		t.Fatalf("SplitSentences =\n  %q\nwant\n  %q", got, want)
	}
}

func TestParseBlocksReadsWhatFormatBlocksWrites(t *testing.T) {
	sources := []string{"First ⟦1⟧block⟦/1⟧.", "Second block."}
	if got, err := ParseBlocks(FormatBlocks(sources), 2); err != nil || !slices.Equal(got, sources) {
		t.Fatalf("round trip = %q, %v; want %q", got, err, sources)
	}

	// Out of order, with line breaks inside a block. A non-breaking space
	// before the question mark is typography and stays.
	reply := "\n<block 2>\nSegundo\n bloco\u00a0?\n</block>\n\n<block 1>Primeiro.</block>\n"
	got, err := ParseBlocks(reply, 2)
	if want := []string{"Primeiro.", "Segundo bloco\u00a0?"}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("ParseBlocks = %q, %v; want %q", got, err, want)
	}
}

func TestParseBlocksRejectsDroppedMergedRepeatedOrEmptyBlocks(t *testing.T) {
	for name, reply := range map[string]string{
		"dropped":      "<block 1>Um.</block>",
		"extra":        "<block 1>Um.</block><block 2>Dois.</block><block 3>Três.</block>",
		"repeated":     "<block 1>Um.</block><block 1>Dois.</block>",
		"out of range": "<block 1>Um.</block><block 5>Dois.</block>",
		"empty":        "<block 1>Um.</block><block 2>  </block>",
		"no blocks":    "Um. Dois.",
		"text after":   "<block 1>Um.</block><block 2>Dois.</block> e o resto do texto",
		"text before":  "Aqui está: <block 1>Um.</block><block 2>Dois.</block>",
		"stray close":  "<block 1>Um </block> resto.</block><block 2>Dois.</block>",
	} {
		if _, err := ParseBlocks(reply, 2); !errors.Is(err, ErrNumbering) {
			t.Errorf("%s: err = %v, want ErrNumbering", name, err)
		}
	}
}

func TestTermsInKeepsOnlyTermsTheSourcesMention(t *testing.T) {
	terms := []Term{
		{Source: "Mara", Rendering: "Mara", Kind: TermName},
		{Source: "Port Varen", Rendering: "Porto Varen", Kind: TermName},
		{Source: "glimmerstone", Rendering: "pedra-lume", Kind: TermTerm},
		{Source: "Ann", Rendering: "Ana", Kind: TermName},
	}
	got := TermsIn(terms, []string{"⟦1⟧Mara⟦/1⟧ met Annabel.", "They left Port ⟦1⟧Varen⟦/1⟧."})

	if want := []Term{terms[0], terms[1]}; !slices.Equal(got, want) {
		t.Fatalf("TermsIn = %+v, want %+v", got, want)
	}
}

func TestWithoutModelCopiesBlocksThatHaveNothingToTranslate(t *testing.T) {
	terms := []Term{
		{Source: "Mara", Rendering: "Mara", Kind: TermName},
		{Source: "Port", Rendering: "Porta", Kind: TermName}, // shares its start with "Port Varen"
		{Source: "Varen", Rendering: "Vareno", Kind: TermName},
		{Source: "Port Varen", Rendering: "Porto Varen", Kind: TermName},
		{Source: "lamp", Rendering: "lampião", Kind: TermTerm},
	}
	for source, want := range map[string]string{
		"***":                   "***",
		"1848":                  "1848",
		"⟦1⟧* * *⟦/1⟧":          "⟦1⟧* * *⟦/1⟧",
		"Mara":                  "Mara",
		"⟦1⟧Port Varen⟦/1⟧":     "⟦1⟧Porto Varen⟦/1⟧",
		"Mara — Port Varen, 12": "Mara — Porto Varen, 12",
		"Varen":                 "Vareno",
		"Port Varen / Varen":    "Porto Varen / Vareno",
	} {
		if got, ok := WithoutModel(source, terms); !ok || got != want {
			t.Errorf("WithoutModel(%q) = %q, %v; want %q, true", source, got, ok, want)
		}
	}
	for _, source := range []string{
		"Mara left.",  // a word besides the name
		"lamp",        // a term may inflect: the model renders it
		"Maraton",     // a name inside a longer word
		"Mara2",       // a name run together with a digit is not that name
		"Ma⟦1⟧ra⟦/1⟧", // a marker inside the name
		"Chapter 1",
	} {
		if got, ok := WithoutModel(source, terms); ok {
			t.Errorf("WithoutModel(%q) = %q, true; want it left to the model", source, got)
		}
	}
}
