package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// BlockKind says where in a Book a Block's text lives.
type BlockKind string

const (
	// BlockTitle is the Book's title from its metadata.
	BlockTitle BlockKind = "title"
	// BlockTOC is a table-of-contents entry.
	BlockTOC BlockKind = "toc"
	// BlockText is a paragraph, heading, list item or table cell.
	BlockText BlockKind = "text"
	// BlockAlt is the alternative text of an image.
	BlockAlt BlockKind = "alt"
)

// Block is the atomic unit of translation (ADR-0005). Source holds the text
// with each inline element replaced by a numbered marker: ⟦1⟧…⟦/1⟧ around
// its content, or ⟦1/⟧ for an element without content such as a line break.
type Block struct {
	Source string
	Kind   BlockKind
	// Position is the Block's place in the Book's reading order, from 0. It
	// is set when Blocks are loaded from storage, not when an EPUB is read.
	Position int
	// Chapter is the 1-based position of the Block's chapter in the Book's
	// reading order; 0 for the title and the table of contents.
	Chapter int
}

// Book is an EPUB submitted for translation, identified by its content.
type Book struct {
	CreatedAt time.Time
	FileHash  string
	Filename  string
	Title     string
	// Language is the source language the Book declares; empty if it declares none.
	Language    string
	TotalBlocks int
	ID          uuid.UUID
}

// ContentHash is the identity of submitted content: its SHA-256 in hex.
func ContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
