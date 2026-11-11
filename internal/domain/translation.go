package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// Level is the register a Translation is written in. A Level changes
// wording only, never content.
type Level string

const (
	LevelFormal   Level = "formal"
	LevelInformal Level = "informal"
	LevelChildren Level = "children"
)

var ErrUnknownLevel = errors.New("the level must be formal, informal or children")

func ParseLevel(s string) (Level, error) {
	switch l := Level(s); l {
	case LevelFormal, LevelInformal, LevelChildren:
		return l, nil
	}
	return "", ErrUnknownLevel
}

// TranslationStatus is where a Translation is in its lifecycle.
type TranslationStatus string

const (
	// TranslationExtracting: its Termbase is being built from the Book.
	TranslationExtracting TranslationStatus = "EXTRACTING"
	// TranslationInReview: waiting for the user to approve the Termbase.
	TranslationInReview   TranslationStatus = "IN_REVIEW"
	TranslationInProgress TranslationStatus = "TRANSLATING"
	TranslationComplete   TranslationStatus = "COMPLETE"
	TranslationFailed     TranslationStatus = "FAILED"
	TranslationCancelled  TranslationStatus = "CANCELLED"
)

// TermbaseStatus is where a Termbase is on its way to being locked (ADR-0006).
type TermbaseStatus string

const (
	TermbaseExtracting TermbaseStatus = "EXTRACTING"
	TermbaseInReview   TermbaseStatus = "IN_REVIEW"
	// TermbaseLocked: approved; it never changes again.
	TermbaseLocked TermbaseStatus = "LOCKED"
	TermbaseFailed TermbaseStatus = "FAILED"
)

// StartingStatus is the status of a Translation that starts against a
// Termbase in status s: it waits for whatever the Termbase still needs.
func (s TermbaseStatus) StartingStatus() TranslationStatus {
	switch s {
	case TermbaseInReview:
		return TranslationInReview
	case TermbaseLocked:
		return TranslationInProgress
	default:
		return TranslationExtracting
	}
}

// Translation is one Book rendered into one target language at one Level.
type Translation struct {
	CreatedAt     time.Time
	Language      string
	Level         Level
	Status        TranslationStatus
	FailureReason string
	ID            uuid.UUID
	BookID        uuid.UUID
	TermbaseID    uuid.UUID
}

var (
	ErrInvalidLanguage = errors.New("the target language must be a language tag such as pt-BR")
	ErrSameLanguage    = errors.New("the target language is the Book's own language")
)

// TargetLanguage validates target as a BCP 47 tag and returns its canonical
// form. source is the language the Book declares, possibly empty. A target
// in the Book's own language is refused; two regional variants of one
// language (pt-PT into pt-BR) are different languages here.
func TargetLanguage(source, target string) (string, error) {
	tag, err := language.Parse(target)
	if err != nil || tag == language.Und {
		return "", ErrInvalidLanguage
	}
	if src, err := language.Parse(source); err == nil && src != language.Und {
		srcBase, _ := src.Base()
		base, _ := tag.Base()
		srcRegion, srcConf := src.Region()
		region, conf := tag.Region()
		sameRegion := srcConf != language.Exact || conf != language.Exact || srcRegion == region
		if srcBase == base && sameRegion {
			return "", ErrSameLanguage
		}
	}
	return tag.String(), nil
}

// LanguageName describes a language tag for a prompt, e.g. "Brazilian
// Portuguese (pt-BR)": a model follows a name more reliably than a tag.
func LanguageName(tag string) string {
	t, err := language.Parse(tag)
	if err != nil {
		return tag
	}
	return fmt.Sprintf("%s (%s)", display.English.Tags().Name(t), tag)
}
