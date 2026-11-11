package domain

import (
	"slices"
	"strings"
	"testing"
)

var verifyTerms = []Term{
	{Source: "Mara", Rendering: "Mara", Kind: TermName},
	{Source: "Port Varen", Rendering: "Porto Varen", Kind: TermName},
	{Source: "glimmerstone", Rendering: "pedra-lume", Kind: TermTerm},
}

const verifySource = "⟦1⟧Mara⟦/1⟧ carried the glimmerstone down to Port Varen before dawn.⟦2/⟧"

func TestVerifyAcceptsAFaithfulTranslation(t *testing.T) {
	translation := "⟦1⟧Mara⟦/1⟧ levou a pedra-lume até Porto Varen antes do amanhecer.⟦2/⟧"
	if reason := Verify(verifySource, translation, verifyTerms, DefaultLimits); reason != "" {
		t.Fatalf("Verify rejected a faithful translation: %s", reason)
	}
	// Markers may move with the words they wrap.
	moved := "Antes do amanhecer,⟦2/⟧ ⟦1⟧Mara⟦/1⟧ levou a pedra-lume até Porto Varen."
	if reason := Verify(verifySource, moved, verifyTerms, DefaultLimits); reason != "" {
		t.Fatalf("Verify rejected markers that moved with their words: %s", reason)
	}
}

func TestVerifyRejectsWhatAModelGetsWrong(t *testing.T) {
	for name, c := range map[string]struct{ translation, reason string }{
		"a name changed":      {"⟦1⟧Mara⟦/1⟧ levou a pedra-lume até Porto Vareno antes do amanhecer.⟦2/⟧", `the name "Port Varen" must be written "Porto Varen"`},
		"a name left as is":   {"⟦1⟧Mara⟦/1⟧ levou a pedra-lume até Port Varen antes do amanhecer.⟦2/⟧", `the name "Port Varen" must be written "Porto Varen"`},
		"a name translated":   {"⟦1⟧Maria⟦/1⟧ levou a pedra-lume até Porto Varen antes do amanhecer.⟦2/⟧", `the name "Mara" must be written "Mara"`},
		"a marker lost":       {"⟦1⟧Mara⟦/1⟧ levou a pedra-lume até Porto Varen antes do amanhecer.", "the formatting mark ⟦2/⟧ is missing"},
		"a pair half lost":    {"⟦1⟧Mara levou a pedra-lume até Porto Varen antes do amanhecer.⟦2/⟧", "the formatting mark ⟦/1⟧ is missing"},
		"a marker repeated":   {"⟦1⟧Mara⟦/1⟧ levou a ⟦1⟧pedra-lume até Porto Varen antes do amanhecer.⟦2/⟧", "the formatting mark ⟦1⟧ appears more often than in the source"},
		"a marker invented":   {"⟦1⟧Mara⟦/1⟧ levou a ⟦3⟧pedra-lume⟦/3⟧ até Porto Varen antes do amanhecer.⟦2/⟧", "the formatting mark ⟦3⟧ appears more often than in the source"},
		"a pair out of order": {"⟦/1⟧Mara⟦1⟧ levou a pedra-lume até Porto Varen antes do amanhecer.⟦2/⟧", "the formatting marks ⟦1⟧ and ⟦/1⟧ are out of order"},
		"text missing":        {"⟦1⟧Mara⟦/1⟧ em Porto Varen.⟦2/⟧", "too short for its source"},
		"text added":          {"⟦1⟧Mara⟦/1⟧ levou a pedra-lume até Porto Varen antes do amanhecer.⟦2/⟧" + strings.Repeat(" E depois voltou para casa.", 5), "too long for its source"},
	} {
		if reason := Verify(verifySource, c.translation, verifyTerms, DefaultLimits); !strings.Contains(reason, c.reason) {
			t.Errorf("%s: reason = %q, want it to say %q", name, reason, c.reason)
		}
	}
}

func TestVerifyRequiresPairsToNest(t *testing.T) {
	source := "She ⟦1⟧never ⟦2⟧once⟦/2⟧ looked⟦/1⟧ back at the harbour that night."
	nested := "Ela ⟦1⟧nunca ⟦2⟧uma vez⟦/2⟧ olhou⟦/1⟧ para o porto naquela noite."
	if reason := Verify(source, nested, nil, DefaultLimits); reason != "" {
		t.Fatalf("Verify rejected nested pairs: %s", reason)
	}
	crossed := "Ela ⟦1⟧nunca ⟦2⟧uma vez⟦/1⟧ olhou⟦/2⟧ para o porto naquela noite."
	if reason := Verify(source, crossed, nil, DefaultLimits); !strings.Contains(reason, "out of order") {
		t.Fatalf("reason = %q, want crossed pairs rejected as out of order", reason)
	}
}

func TestVerifyDoesNotLookForANameInsideALongerName(t *testing.T) {
	terms := []Term{
		{Source: "York", Rendering: "York", Kind: TermName},
		{Source: "New York", Rendering: "Nova Iorque", Kind: TermName},
	}
	if reason := Verify("He left New York before the winter came.", "Ele deixou Nova Iorque antes de o inverno chegar.", terms, DefaultLimits); reason != "" {
		t.Fatalf("Verify asked for the shorter name: %s", reason)
	}
	// Each name on its own is still required.
	reason := Verify("He left New York for York in the spring.", "Ele trocou Nova Iorque por Iorque na primavera.", terms, DefaultLimits)
	if !strings.Contains(reason, `the name "York" must be written "York"`) {
		t.Fatalf("reason = %q, want the shorter name required where it stands alone", reason)
	}
}

func TestVerifyRejectsABlockReturnedUntranslated(t *testing.T) {
	source := "The sea was calm that night."
	if reason := Verify(source, source, nil, DefaultLimits); !strings.Contains(reason, "not translated") {
		t.Fatalf("reason = %q, want it to say the block was not translated", reason)
	}
	// Two words may rightly stay as they are.
	if reason := Verify("Mr. Smith", "Mr. Smith", nil, DefaultLimits); reason != "" {
		t.Fatalf("Verify rejected a short Block that needs no change: %s", reason)
	}
}

func TestVerifyIgnoresTheRatioOfShortBlocks(t *testing.T) {
	if reason := Verify("No.", "De jeito nenhum.", nil, DefaultLimits); reason != "" {
		t.Fatalf("Verify rejected a short Block on its length: %s", reason)
	}
}

func TestVerifyIgnoresNamesTheSourceDoesNotMention(t *testing.T) {
	if reason := Verify("The sea was calm that night, and nothing moved.", "O mar estava calmo naquela noite, e nada se movia.", verifyTerms, DefaultLimits); reason != "" {
		t.Fatalf("Verify asked for a name the source does not mention: %s", reason)
	}
}

func TestWarningsFlagWithoutRejecting(t *testing.T) {
	source := "Mara showed the glimmerstone to Annabel on Monday. Annabel laughed."
	got := Warnings(source, "Mara mostrou a pedra brilhante para Ana na segunda-feira. Ana riu.", verifyTerms)

	want := []string{
		`the Term "glimmerstone" was not rendered as "pedra-lume"`,
		`"Annabel" is not in the translation; if it is a name, add it to the Termbase`,
		`"Monday" is not in the translation; if it is a name, add it to the Termbase`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Warnings =\n  %q\nwant\n  %q", got, want)
	}
}

func TestWarningsDoNotTakeTheMiddleOfAWordForATerm(t *testing.T) {
	terms := []Term{{Source: "king", Rendering: "rei", Kind: TermTerm}}
	// "sereia" holds "rei" in its middle and is another word altogether.
	if got := Warnings("The king slept.", "A sereia dormia.", terms); len(got) != 1 {
		t.Fatalf("Warnings = %q, want one: the Term is not in the translation", got)
	}
	// "reis" is the Term inflected.
	if got := Warnings("The king slept.", "Os reis dormiam.", terms); len(got) != 0 {
		t.Fatalf("Warnings = %q, want none for an inflected Term", got)
	}
}

func TestWarningsNameAWordApartFromThePunctuationAroundIt(t *testing.T) {
	// An em dash parts two words; a possessive ending is not part of the name.
	got := Warnings("My dear Victor,—one word from Annabel’s letter.", "Meu caro,—uma palavra da carta.", nil)

	want := []string{
		`"Victor" is not in the translation; if it is a name, add it to the Termbase`,
		`"Annabel" is not in the translation; if it is a name, add it to the Termbase`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Warnings =\n  %q\nwant\n  %q", got, want)
	}
}

func TestWarningsAreSilentWhenNothingIsDoubtful(t *testing.T) {
	for name, c := range map[string]struct{ source, translation string }{
		"a faithful translation":          {"Mara showed the glimmerstone to Annabel.", "Mara mostrou a pedra-lume para Annabel."},
		"a term that inflected":           {"Mara showed the glimmerstone.", "Mara mostrou as Pedra-Lumes."},
		"a name from the Termbase":        {"She left Port Varen. Then Mara slept.", "Ela deixou Porto Varen. Então Mara dormiu."},
		"a word that opens a phrase":      {"She slept. Tomorrow would come.", "Ela dormiu. O amanhã viria."},
		"a title-cased heading":           {"The Lighthouse Keeper", "O Faroleiro"},
		"the pronoun I":                   {"Then I left the house before the sun was up.", "Então saí de casa antes de o sol nascer."},
		"a word ending in a digit":        {"She flew the B52 home.", "Ela pilotou o B52 para casa."},
		"a word that opens quoted speech": {"She said, “The door is open, and the night is cold.”", "Ela disse: “A porta está aberta, e a noite está fria.”"},
		"a possessive of a Termbase name": {"She took Mara’s lamp and Mara's coat.", "Ela pegou o lampião e o casaco de Mara."},
	} {
		if got := Warnings(c.source, c.translation, verifyTerms); len(got) != 0 {
			t.Errorf("%s: Warnings = %q, want none", name, got)
		}
	}
}
