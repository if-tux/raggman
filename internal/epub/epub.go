// Package epub reads an EPUB into the Blocks of a Book (ADR-0005).
package epub

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
)

var (
	// ErrNotEPUB means the content is not an EPUB at all.
	ErrNotEPUB = errors.New("not an EPUB")
	// ErrDRM means the EPUB's text is encrypted. Raggman never removes DRM.
	ErrDRM = errors.New("the EPUB is protected by DRM")
	// ErrInvalid means the content is an EPUB that cannot be read; errors
	// wrapping it say why.
	ErrInvalid = errors.New("the EPUB could not be read")
)

// An EPUB is a zip, so its size on disk says little about its size in memory.
// These caps stop a small upload from expanding without limit.
const (
	maxEntryBytes = 16 << 20
	maxTotalBytes = 128 << 20
)

// Parsed is what Raggman reads from an EPUB.
type Parsed struct {
	Title string
	// Language is the declared source language; empty if none is declared.
	Language string
	Blocks   []domain.Block
}

// Parse reads an EPUB into Blocks, in reading order: the title, the table of
// contents, then every chapter of the spine. The same bytes always yield the
// same Blocks in the same order.
func Parse(content []byte) (Parsed, error) {
	zr, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return Parsed{}, ErrNotEPUB
	}
	a := &archive{files: make(map[string]*zip.File, len(zr.File))}
	for _, f := range zr.File {
		a.files[f.Name] = f
	}
	mimetype, err := a.read("mimetype")
	if err != nil || strings.TrimSpace(string(mimetype)) != "application/epub+zip" {
		return Parsed{}, ErrNotEPUB
	}
	if err := a.loadEncryption(); err != nil {
		return Parsed{}, err
	}

	var container struct {
		Rootfiles []struct {
			FullPath string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := a.readXML("META-INF/container.xml", &container); err != nil {
		return Parsed{}, err
	}
	if len(container.Rootfiles) == 0 {
		return Parsed{}, invalid("container.xml names no package document")
	}
	opfPath := container.Rootfiles[0].FullPath

	var opf struct {
		Titles    []string `xml:"metadata>title"`
		Languages []string `xml:"metadata>language"`
		Manifest  []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := a.readXML(opfPath, &opf); err != nil {
		return Parsed{}, err
	}

	book := Parsed{}
	if len(opf.Titles) > 0 {
		book.Title = strings.Join(strings.Fields(opf.Titles[0]), " ")
	}
	if len(opf.Languages) > 0 {
		book.Language = strings.TrimSpace(opf.Languages[0])
	}
	if book.Title != "" {
		book.Blocks = append(book.Blocks, domain.Block{Kind: domain.BlockTitle, Source: book.Title})
	}

	// Manifest hrefs are URL-encoded and relative to the package document.
	resolve := func(href string) string {
		href, _, _ = strings.Cut(href, "#")
		if unescaped, err := url.PathUnescape(href); err == nil {
			href = unescaped
		}
		return path.Join(path.Dir(opfPath), href)
	}
	// An EPUB 3 usually carries both a navigation file and an NCX for older
	// readers. Both are read: they are separate entries of the zip, and each
	// has to come out translated. The navigation file's page list and
	// landmarks are part of it and are read too.
	chapters := make(map[string]string, len(opf.Manifest)) // manifest id → path
	var navPath string
	for _, item := range opf.Manifest {
		switch item.MediaType {
		case "application/x-dtbncx+xml":
			ncx, err := a.read(resolve(item.Href))
			if err != nil {
				return Parsed{}, err
			}
			entries, err := ncxEntries(ncx)
			if err != nil {
				return Parsed{}, invalid("%s: %v", item.Href, err)
			}
			book.Blocks = append(book.Blocks, entries...)
		// EPUB 2 Books made by old tools declare their chapters as text/html.
		case "application/xhtml+xml", "text/html":
			chapters[item.ID] = resolve(item.Href)
			if strings.Contains(" "+item.Properties+" ", " nav ") {
				navPath = chapters[item.ID]
			}
		}
	}
	// The navigation file is the table of contents wherever it sits, so it is
	// read once, as such, and skipped in the spine.
	if navPath != "" {
		entries, err := a.blocks(navPath, 0, domain.BlockTOC)
		if err != nil {
			return Parsed{}, err
		}
		book.Blocks = append(book.Blocks, entries...)
	}

	text := false
	for i, ref := range opf.Spine {
		name, ok := chapters[ref.IDRef]
		if !ok || name == navPath {
			continue
		}
		blocks, err := a.blocks(name, i+1, domain.BlockText)
		if err != nil {
			return Parsed{}, err
		}
		// A Book of pictures and their descriptions has nothing to translate.
		text = text || slices.ContainsFunc(blocks, func(b domain.Block) bool { return b.Kind == domain.BlockText })
		book.Blocks = append(book.Blocks, blocks...)
	}
	if !text {
		return Parsed{}, invalid("it has no text")
	}
	return book, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

type archive struct {
	files     map[string]*zip.File
	encrypted map[string]bool
	total     int
}

// read returns one entry of the zip. Anything Raggman needs to read is text
// it would translate, so an encrypted entry means DRM; obfuscated fonts are
// never read and so never refused.
func (a *archive) read(name string) ([]byte, error) {
	if a.encrypted[name] {
		return nil, ErrDRM
	}
	f, ok := a.files[name]
	if !ok {
		return nil, invalid("%s is missing", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, invalid("%s: %v", name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
	if err != nil {
		return nil, invalid("%s: %v", name, err)
	}
	a.total += len(data)
	if len(data) > maxEntryBytes || a.total > maxTotalBytes {
		return nil, invalid("it is too large once unpacked")
	}
	return data, nil
}

func (a *archive) readXML(name string, v any) error {
	data, err := a.read(name)
	if err != nil {
		return err
	}
	if err := newDecoder(data).Decode(v); err != nil {
		return invalid("%s: %v", name, err)
	}
	return nil
}

// loadEncryption records which entries META-INF/encryption.xml encrypts.
func (a *archive) loadEncryption() error {
	const name = "META-INF/encryption.xml"
	if _, ok := a.files[name]; !ok {
		return nil
	}
	var encryption struct {
		Data []struct {
			Reference struct {
				URI string `xml:"URI,attr"`
			} `xml:"CipherData>CipherReference"`
		} `xml:"EncryptedData"`
	}
	if err := a.readXML(name, &encryption); err != nil {
		return err
	}
	a.encrypted = make(map[string]bool, len(encryption.Data))
	for _, d := range encryption.Data {
		uri := d.Reference.URI
		if unescaped, err := url.PathUnescape(uri); err == nil {
			uri = unescaped
		}
		// Entries are named from the root of the zip, with or without a slash.
		a.encrypted[strings.TrimPrefix(path.Clean(uri), "/")] = true
	}
	return nil
}

func (a *archive) blocks(name string, chapter int, kind domain.BlockKind) ([]domain.Block, error) {
	doc, err := a.read(name)
	if err != nil {
		return nil, err
	}
	blocks, err := xhtmlBlocks(doc, chapter, kind)
	if err != nil {
		return nil, invalid("%s: %v", name, err)
	}
	return blocks, nil
}

// inlineElements live inside a Block and become markers. Every other element
// is a boundary between Blocks, so an element this list does not know splits
// text rather than gluing two paragraphs together.
var inlineElements = set("a", "abbr", "acronym", "b", "bdi", "bdo", "big", "br", "cite", "code", "data", "del", "dfn",
	"em", "font", "i", "img", "ins", "kbd", "label", "mark", "math", "nobr", "output", "q", "ruby", "rp", "rt", "s",
	"samp", "small", "span", "strike", "strong", "sub", "sup", "svg", "time", "tt", "u", "var", "wbr")

// emptyElements are inline elements with no text to translate: they become a
// single marker, and whatever they contain is left alone.
var emptyElements = set("br", "img", "math", "svg", "wbr")

// skippedElements hold nothing a reader sees as text.
var skippedElements = set("head", "script", "style", "template")

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// newDecoder reads the XML entries of an EPUB as they are written in
// practice: some start with a byte order mark, and HTML entities such as
// &nbsp; turn up in every kind of entry.
//
// ponytail: only UTF-8 is read. Add a CharsetReader if UTF-16 Books show up.
func newDecoder(data []byte) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\uFEFF"))))
	d.Entity = xml.HTMLEntity
	return d
}

// xhtmlBlocks splits one XHTML entry into Blocks. A Block is a run of
// text and inline elements that no other element interrupts, so a paragraph is
// one Block and so is the text of a list item that also holds a nested list.
//
// ponytail: whitespace is collapsed everywhere, including inside <pre>. Keep
// it verbatim there if Books with code or verse laid out by spaces matter.
//
// ponytail: text that already contains ⟦1⟧ cannot be told from a marker.
// Escape it if a Book ever uses those brackets.
func xhtmlBlocks(doc []byte, chapter int, kind domain.BlockKind) ([]domain.Block, error) {
	// EPUB content is XHTML, so it is read as XML; an HTML parser would
	// swallow the rest of an entry after a self-closing <title/> or <script/>.
	//
	// Unclosed tags are tolerated here and only here: applied to the package
	// file, HTML's rules would close its <meta> elements early and lose the
	// manifest.
	// ponytail: mis-nested tags (<b><i></b></i>) still refuse the Book. Use a
	// forgiving HTML parser if such Books show up.
	d := newDecoder(doc)
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose

	type openMarker struct{ depth, n int }
	var (
		blocks  []domain.Block
		run     strings.Builder
		open    []openMarker // inline elements opened in the current run
		alts    []string
		depth   int
		skipAt  int // depth of the skipped element being read, 0 if none
		markers int
		hasText bool
	)
	flush := func() {
		text := run.String()
		// An inline element still open here wraps more than this Block, so it
		// cannot be a marker inside it.
		for _, m := range open {
			text = strings.Replace(text, fmt.Sprintf("⟦%d⟧", m.n), "", 1)
		}
		if hasText {
			blocks = append(blocks, domain.Block{Chapter: chapter, Kind: kind, Source: strings.Join(strings.Fields(text), " ")})
		}
		for _, alt := range alts {
			blocks = append(blocks, domain.Block{Chapter: chapter, Kind: domain.BlockAlt, Source: alt})
		}
		run.Reset()
		open, alts, markers, hasText = nil, nil, 0, false
	}

	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			name := strings.ToLower(t.Name.Local)
			switch {
			case skipAt != 0:
			case skippedElements[name]:
				skipAt = depth
			case !inlineElements[name]:
				flush()
			case emptyElements[name]:
				markers++
				fmt.Fprintf(&run, "⟦%d/⟧", markers)
				if name == "math" || name == "svg" {
					skipAt = depth
				}
				if name == "img" {
					for _, attr := range t.Attr {
						if alt := strings.Join(strings.Fields(attr.Value), " "); attr.Name.Local == "alt" && alt != "" {
							alts = append(alts, alt)
						}
					}
				}
			default:
				markers++
				open = append(open, openMarker{depth: depth, n: markers})
				fmt.Fprintf(&run, "⟦%d⟧", markers)
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			switch {
			case skipAt != 0:
				if depth == skipAt {
					skipAt = 0
				}
			case !inlineElements[name]:
				flush()
			case len(open) > 0 && open[len(open)-1].depth == depth:
				fmt.Fprintf(&run, "⟦/%d⟧", open[len(open)-1].n)
				open = open[:len(open)-1]
			}
			depth--
		case xml.CharData:
			if skipAt == 0 {
				run.Write(t)
				hasText = hasText || len(bytes.TrimSpace(t)) > 0
			}
		}
	}
	flush()
	return blocks, nil
}

// ncxEntries reads the labels of an EPUB 2 table of contents.
func ncxEntries(ncx []byte) ([]domain.Block, error) {
	d := newDecoder(ncx)
	var (
		blocks  []domain.Block
		label   strings.Builder
		inMap   bool
		inLabel bool
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return blocks, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "navMap":
				inMap = true
			case "text":
				inLabel = inMap
				label.Reset()
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "navMap":
				inMap = false
			case "text":
				if text := strings.Join(strings.Fields(label.String()), " "); inLabel && text != "" {
					blocks = append(blocks, domain.Block{Kind: domain.BlockTOC, Source: text})
				}
				inLabel = false
			}
		case xml.CharData:
			if inLabel {
				label.Write(t)
			}
		}
	}
}
