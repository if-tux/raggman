package epub_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/BrunoPereiradosSantos/raggman/internal/domain"
	"github.com/BrunoPereiradosSantos/raggman/internal/epub"
	"github.com/BrunoPereiradosSantos/raggman/internal/epub/epubtest"
)

func parse(t *testing.T, content []byte) epub.Parsed {
	t.Helper()
	book, err := epub.Parse(content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return book
}

// text returns the Blocks of the chapters, leaving out title and contents.
func text(book epub.Parsed) []domain.Block {
	var blocks []domain.Block
	for _, b := range book.Blocks {
		if b.Chapter > 0 {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

func sources(blocks []domain.Block) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.Source
	}
	return out
}

func wantSources(t *testing.T, got []domain.Block, want ...string) {
	t.Helper()
	if !slices.Equal(sources(got), want) {
		t.Fatalf("Blocks =\n  %q\nwant\n  %q", sources(got), want)
	}
}

func TestReadsTitleLanguageAndBlocksInReadingOrder(t *testing.T) {
	book := parse(t, epubtest.Book("The Lighthouse Keeper", "en",
		`<h1>Chapter One</h1><p>Mara lit the lamp.</p><p>The sea was calm.</p>`,
		`<h1>Chapter Two</h1><p>A ship appeared at dawn.</p>`,
	))

	if book.Title != "The Lighthouse Keeper" || book.Language != "en" {
		t.Fatalf("title, language = %q, %q", book.Title, book.Language)
	}
	want := []domain.Block{
		{Kind: domain.BlockTitle, Source: "The Lighthouse Keeper"},
		{Kind: domain.BlockTOC, Source: "⟦1⟧Chapter 1⟦/1⟧"},
		{Kind: domain.BlockTOC, Source: "⟦1⟧Chapter 2⟦/1⟧"},
		{Kind: domain.BlockText, Chapter: 2, Source: "Chapter One"},
		{Kind: domain.BlockText, Chapter: 2, Source: "Mara lit the lamp."},
		{Kind: domain.BlockText, Chapter: 2, Source: "The sea was calm."},
		{Kind: domain.BlockText, Chapter: 3, Source: "Chapter Two"},
		{Kind: domain.BlockText, Chapter: 3, Source: "A ship appeared at dawn."},
	}
	// The navigation document comes first in this spine, so chapters start at 2.
	files := epubtest.Files("The Lighthouse Keeper", "en",
		`<h1>Chapter One</h1><p>Mara lit the lamp.</p><p>The sea was calm.</p>`,
		`<h1>Chapter Two</h1><p>A ship appeared at dawn.</p>`)
	files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"], "<spine>", `<spine><itemref idref="nav"/>`, 1)
	if got := parse(t, epubtest.Zip(files)).Blocks; !slices.Equal(got, want) {
		t.Fatalf("Blocks =\n  %+v\nwant\n  %+v", got, want)
	}
}

func TestInlineElementsBecomeNumberedMarkers(t *testing.T) {
	book := parse(t, epubtest.Book("T", "en",
		`<p>She said <em>never</em> and <a href="n.xhtml#1">left<sup>1</sup></a>.<br/>Then silence.</p>`))

	wantSources(t, text(book), "She said ⟦1⟧never⟦/1⟧ and ⟦2⟧left⟦3⟧1⟦/3⟧⟦/2⟧.⟦4/⟧Then silence.")
}

func TestMarkersRestartInEveryBlock(t *testing.T) {
	book := parse(t, epubtest.Book("T", "en", `<p><b>One</b></p><p><i>Two</i></p>`))

	wantSources(t, text(book), "⟦1⟧One⟦/1⟧", "⟦1⟧Two⟦/1⟧")
}

func TestListItemsTableCellsAndLooseTextAreBlocks(t *testing.T) {
	book := parse(t, epubtest.Book("T", "en", `
		<div>Loose words in a division.</div>
		<ul><li>First item</li><li>Second item<ul><li>Nested item</li></ul></li></ul>
		<table><tr><th>Name</th><td>Mara</td></tr></table>
		<blockquote><p>Quoted line.</p></blockquote>
		<p>***</p>`))

	wantSources(t, text(book),
		"Loose words in a division.", "First item", "Second item", "Nested item", "Name", "Mara", "Quoted line.", "***")
}

func TestWhitespaceIsCollapsedAndEmptyParagraphsSkipped(t *testing.T) {
	book := parse(t, epubtest.Book("T", "en", "<p>  A line\n\t  broken   in the file.  </p><p> </p><p><br/></p><p>caf&eacute;&nbsp;noir</p>"))

	wantSources(t, text(book), "A line broken in the file.", "café noir")
}

func TestImageAlternativeTextIsItsOwnBlock(t *testing.T) {
	book := parse(t, epubtest.Book("T", "en",
		`<p>See the map <img src="m.png" alt="A map of the coast"/> below.</p><p><img src="d.png" alt=""/></p><div><img src="s.png" alt="The ship"/></div>`))

	want := []domain.Block{
		{Kind: domain.BlockText, Chapter: 1, Source: "See the map ⟦1/⟧ below."},
		{Kind: domain.BlockAlt, Chapter: 1, Source: "A map of the coast"},
		{Kind: domain.BlockAlt, Chapter: 1, Source: "The ship"},
	}
	if got := text(book); !slices.Equal(got, want) {
		t.Fatalf("Blocks = %+v, want %+v", got, want)
	}
}

func TestSkipsScriptsStylesAndHead(t *testing.T) {
	// A self-closing <script/> must not swallow the text after it.
	book := parse(t, epubtest.Book("T", "en", `<script src="x.js"/><style>p{}</style><p>Visible.</p><script>var hidden;</script>`))

	wantSources(t, text(book), "Visible.")
}

func TestInlineElementAroundParagraphsLeavesNoBrokenMarker(t *testing.T) {
	book := parse(t, epubtest.Book("T", "en", `<span>Before<p>Inside <b>bold</b></p>After</span>`))

	wantSources(t, text(book), "Before", "Inside ⟦1⟧bold⟦/1⟧", "After")
}

func TestInlineElementsOfEveryKindStayInsideTheirBlock(t *testing.T) {
	book := parse(t, epubtest.Book("T", "en",
		`<p>The <acronym>NASA</acronym> <label>launch</label> <nobr>at noon</nobr>.</p>`+
			`<p>Energy is <math><mi>E</mi></math> and <svg><text>drawn</text></svg> here.</p>`))

	wantSources(t, text(book),
		"The ⟦1⟧NASA⟦/1⟧ ⟦2⟧launch⟦/2⟧ ⟦3⟧at noon⟦/3⟧.",
		"Energy is ⟦1/⟧ and ⟦2/⟧ here.")
}

func TestReadsChapterStartingWithByteOrderMark(t *testing.T) {
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	files["OEBPS/ch1.xhtml"] = "\uFEFF" + files["OEBPS/ch1.xhtml"]

	wantSources(t, text(parse(t, epubtest.Zip(files))), "Text.")
}

func TestReadsChaptersDeclaredAsHTML(t *testing.T) {
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"],
		`href="ch1.xhtml" media-type="application/xhtml+xml"`, `href="ch1.xhtml" media-type="text/html"`, 1)

	wantSources(t, text(parse(t, epubtest.Zip(files))), "Text.")
}

func TestReadsEntitiesInMetadata(t *testing.T) {
	book := parse(t, epubtest.Book("Caf&eacute;&nbsp;Noir", "fr", `<p>Texte.</p>`))

	if book.Title != "Café Noir" {
		t.Fatalf("title = %q, want %q", book.Title, "Café Noir")
	}
}

func TestReadsBothTablesOfContentsWhenABookCarriesTwo(t *testing.T) {
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	files["OEBPS/toc.ncx"] = `<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap>
  <navPoint id="a"><navLabel><text>The Storm</text></navLabel><content src="ch1.xhtml"/></navPoint>
</navMap></ncx>`
	files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"], "<manifest>",
		`<manifest><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>`, 1)

	var toc []domain.Block
	for _, b := range parse(t, epubtest.Zip(files)).Blocks {
		if b.Kind == domain.BlockTOC {
			toc = append(toc, b)
		}
	}
	wantSources(t, toc, "The Storm", "⟦1⟧Chapter 1⟦/1⟧")
}

func TestReadsEPUB2TableOfContents(t *testing.T) {
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	delete(files, "OEBPS/nav.xhtml")
	files["OEBPS/toc.ncx"] = `<?xml version="1.0"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/">
  <docTitle><text>T</text></docTitle>
  <docAuthor><text>An Author</text></docAuthor>
  <navMap>
    <navPoint id="a"><navLabel><text>The Storm</text></navLabel><content src="ch1.xhtml"/>
      <navPoint id="b"><navLabel><text>The Calm</text></navLabel><content src="ch1.xhtml#b"/></navPoint>
    </navPoint>
  </navMap>
</ncx>`
	files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"],
		`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`,
		`<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>`, 1)

	want := []domain.Block{
		{Kind: domain.BlockTitle, Source: "T"},
		{Kind: domain.BlockTOC, Source: "The Storm"},
		{Kind: domain.BlockTOC, Source: "The Calm"},
		{Kind: domain.BlockText, Chapter: 1, Source: "Text."},
	}
	if got := parse(t, epubtest.Zip(files)).Blocks; !slices.Equal(got, want) {
		t.Fatalf("Blocks = %+v, want %+v", got, want)
	}
}

func TestResolvesEncodedManifestPaths(t *testing.T) {
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	files["OEBPS/text/chapter one.xhtml"] = files["OEBPS/ch1.xhtml"]
	delete(files, "OEBPS/ch1.xhtml")
	files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"], `href="ch1.xhtml"`, `href="text/chapter%20one.xhtml"`, 1)

	wantSources(t, text(parse(t, epubtest.Zip(files))), "Text.")
}

func TestParsingIsDeterministic(t *testing.T) {
	content := epubtest.Book("T", "en", `<p>One <i>two</i></p><ul><li>three</li></ul>`, `<p>four</p>`)

	if a, b := parse(t, content), parse(t, content); !slices.Equal(a.Blocks, b.Blocks) {
		t.Fatalf("two parses differ: %+v vs %+v", a.Blocks, b.Blocks)
	}
}

const encryption = `<?xml version="1.0"?>
<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#">
  <enc:EncryptedData>
    <enc:EncryptionMethod Algorithm="%ALG%"/>
    <enc:CipherData><enc:CipherReference URI="%URI%"/></enc:CipherData>
  </enc:EncryptedData>
</encryption>`

func withEncryption(algorithm, uri string) []byte {
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	files["OEBPS/fonts/serif.otf"] = "obfuscated font bytes"
	files["META-INF/encryption.xml"] = strings.NewReplacer("%ALG%", algorithm, "%URI%", uri).Replace(encryption)
	return epubtest.Zip(files)
}

func TestRefusesEncryptedContentAsDRM(t *testing.T) {
	for _, uri := range []string{"OEBPS/ch1.xhtml", "/OEBPS/ch1.xhtml", "OEBPS/content.opf", "OEBPS/nav.xhtml"} {
		_, err := epub.Parse(withEncryption("http://www.w3.org/2001/04/xmlenc#aes128-cbc", uri))
		if !errors.Is(err, epub.ErrDRM) {
			t.Errorf("encrypted %s: err = %v, want ErrDRM", uri, err)
		}
	}
}

func TestAcceptsObfuscatedFonts(t *testing.T) {
	book := parse(t, withEncryption("http://www.idpf.org/2008/embedding", "OEBPS/fonts/serif.otf"))

	wantSources(t, text(book), "Text.")
}

func TestRejectsWhatIsNotAnEPUB(t *testing.T) {
	plainZip := epubtest.Files("T", "en", `<p>Text.</p>`)
	plainZip["mimetype"] = "application/zip"

	for name, content := range map[string][]byte{
		"not a zip":      []byte("%PDF-1.7 not a zip"),
		"wrong mimetype": epubtest.Zip(plainZip),
		"empty":          nil,
	} {
		if _, err := epub.Parse(content); !errors.Is(err, epub.ErrNotEPUB) {
			t.Errorf("%s: err = %v, want ErrNotEPUB", name, err)
		}
	}
}

func TestRejectsUnreadableEPUBs(t *testing.T) {
	noContainer := epubtest.Files("T", "en", `<p>Text.</p>`)
	delete(noContainer, "META-INF/container.xml")
	missingChapter := epubtest.Files("T", "en", `<p>Text.</p>`)
	delete(missingChapter, "OEBPS/ch1.xhtml")

	for name, files := range map[string]map[string]string{
		"no container":    noContainer,
		"missing chapter": missingChapter,
		"no text":         epubtest.Files("T", "en", `<p><img src="a.png" alt="Only a picture"/></p>`),
	} {
		if _, err := epub.Parse(epubtest.Zip(files)); !errors.Is(err, epub.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestRejectsEntryThatUnpacksTooLarge(t *testing.T) {
	files := epubtest.Files("T", "en", `<p>Text.</p>`+strings.Repeat(" ", 16<<20))

	_, err := epub.Parse(epubtest.Zip(files))
	if !errors.Is(err, epub.ErrInvalid) || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want ErrInvalid saying it is too large", err)
	}
}

func TestBookWithoutDeclaredLanguageHasNone(t *testing.T) {
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	files["OEBPS/content.opf"] = strings.Replace(files["OEBPS/content.opf"], "<dc:language>en</dc:language>", "", 1)

	if got := parse(t, epubtest.Zip(files)).Language; got != "" {
		t.Fatalf("language = %q, want none", got)
	}
}
