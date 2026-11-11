package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/BrunoPereiradosSantos/raggman/internal/epub/epubtest"
	"github.com/google/uuid"
)

type bookJSON struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Title    string `json:"title"`
	Language string `json:"language"`
	Blocks   int    `json:"blocks"`
}

type errorJSON struct {
	Error string `json:"error"`
}

// uniqueBook returns an EPUB whose content never repeats: a title, one
// table-of-contents entry, a heading and two paragraphs.
func uniqueBook(t *testing.T) []byte {
	t.Helper()
	return epubtest.Book("The Lighthouse Keeper", "en",
		`<h1>Chapter One</h1><p>Mara lit the <em>lamp</em>.</p><p>`+rand.Text()+`</p>`)
}

func submitBook(t *testing.T, server *httptest.Server, filename string, content []byte) *http.Response {
	t.Helper()
	return submitFile(t, server, "/books", filename, content)
}

func blockSources(t *testing.T, bookID string) []string {
	t.Helper()
	rows, err := db(t).Query(context.Background(), "SELECT source FROM blocks WHERE book_id = $1 ORDER BY position", bookID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sources []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, s)
	}
	return sources
}

func TestSubmittingCreatesBookWithItsBlocks(t *testing.T) {
	server := newServer(t)

	content := uniqueBook(t)
	resp := submitBook(t, server, "lighthouse.epub", content)
	wantStatus(t, resp, http.StatusCreated)
	created := decode[bookJSON](t, resp.Body)

	want := bookJSON{ID: created.ID, Filename: "lighthouse.epub", Title: "The Lighthouse Keeper", Language: "en", Blocks: 5}
	if created != want {
		t.Fatalf("created = %+v, want %+v", created, want)
	}

	resp = get(t, server.URL+"/books/"+created.ID)
	wantStatus(t, resp, http.StatusOK)
	if got := decode[bookJSON](t, resp.Body); got != created {
		t.Fatalf("GET = %+v, want %+v", got, created)
	}

	var stored []byte
	if err := db(t).QueryRow(context.Background(), "SELECT content FROM book_files WHERE book_id = $1", created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, content) {
		t.Fatalf("stored %d bytes, want the %d submitted", len(stored), len(content))
	}

	sources := blockSources(t, created.ID)
	wantStart := []string{"The Lighthouse Keeper", "⟦1⟧Chapter 1⟦/1⟧", "Chapter One", "Mara lit the ⟦1⟧lamp⟦/1⟧."}
	if len(sources) != 5 || !slices.Equal(sources[:4], wantStart) {
		t.Fatalf("stored Blocks = %q, want %q then one more", sources, wantStart)
	}
}

func TestIdenticalContentReturnsExistingBook(t *testing.T) {
	server := newServer(t)
	content := uniqueBook(t)

	first := decode[bookJSON](t, submitBook(t, server, "original.epub", content).Body)

	resp := submitBook(t, server, "renamed.epub", content)
	wantStatus(t, resp, http.StatusOK)
	if second := decode[bookJSON](t, resp.Body); second != first {
		t.Fatalf("second submission = %+v, want existing Book %+v", second, first)
	}
	if n := len(blockSources(t, first.ID)); n != first.Blocks {
		t.Fatalf("stored Blocks = %d, want %d (resubmitting must not add Blocks)", n, first.Blocks)
	}
}

func TestRejectsNonEPUBEvenWithEPUBExtension(t *testing.T) {
	server := newServer(t)
	plainZip := epubtest.Files("T", "en", `<p>Text.</p>`)
	plainZip["mimetype"] = "application/zip"

	wantStatus(t, submitBook(t, server, "fake.epub", uniquePDF(t)), http.StatusUnsupportedMediaType)
	wantStatus(t, submitBook(t, server, "fake.epub", epubtest.Zip(plainZip)), http.StatusUnsupportedMediaType)
}

func TestRefusesBookWithDRM(t *testing.T) {
	server := newServer(t)
	files := epubtest.Files("T", "en", `<p>`+rand.Text()+`</p>`)
	files["META-INF/encryption.xml"] = `<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#">
  <enc:EncryptedData><enc:CipherData><enc:CipherReference URI="OEBPS/ch1.xhtml"/></enc:CipherData></enc:EncryptedData>
</encryption>`

	resp := submitBook(t, server, "locked.epub", epubtest.Zip(files))
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if msg := decode[errorJSON](t, resp.Body).Error; !strings.Contains(msg, "DRM") {
		t.Fatalf("error = %q, want it to name DRM", msg)
	}
}

func TestAcceptsBookWithOnlyObfuscatedFonts(t *testing.T) {
	server := newServer(t)
	files := epubtest.Files("T", "en", `<p>`+rand.Text()+`</p>`)
	files["OEBPS/serif.otf"] = "obfuscated"
	files["META-INF/encryption.xml"] = `<encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#">
  <enc:EncryptedData><enc:CipherData><enc:CipherReference URI="OEBPS/serif.otf"/></enc:CipherData></enc:EncryptedData>
</encryption>`

	wantStatus(t, submitBook(t, server, "fonts.epub", epubtest.Zip(files)), http.StatusCreated)
}

func TestRejectsUnreadableBookSayingWhy(t *testing.T) {
	server := newServer(t)
	files := epubtest.Files("T", "en", `<p>Text.</p>`)
	delete(files, "OEBPS/ch1.xhtml")

	resp := submitBook(t, server, "broken.epub", epubtest.Zip(files))
	wantStatus(t, resp, http.StatusUnprocessableEntity)
	if msg := decode[errorJSON](t, resp.Body).Error; !strings.Contains(msg, "ch1.xhtml is missing") {
		t.Fatalf("error = %q, want it to name the missing chapter", msg)
	}
}

func TestRejectsBooksOver50MB(t *testing.T) {
	server := newServer(t)

	resp := submitBook(t, server, "huge.epub", bytes.Repeat([]byte("x"), 50<<20+1))
	wantStatus(t, resp, http.StatusRequestEntityTooLarge)
	if msg := decode[errorJSON](t, resp.Body).Error; !strings.Contains(msg, "EPUB exceeds the 50 MB limit") {
		t.Fatalf("error = %q, want it to state the limit", msg)
	}
}

func TestListIncludesSubmittedBooks(t *testing.T) {
	server := newServer(t)
	book := decode[bookJSON](t, submitBook(t, server, "listed.epub", uniqueBook(t)).Body)

	resp := get(t, server.URL+"/books")
	wantStatus(t, resp, http.StatusOK)
	if !slices.Contains(decode[[]bookJSON](t, resp.Body), book) {
		t.Fatalf("list does not contain %+v", book)
	}
}

func TestBookStaysOutOfTheCorpus(t *testing.T) {
	server := newServer(t)
	book := decode[bookJSON](t, submitBook(t, server, "novel.epub", uniqueBook(t)).Body)

	resp := get(t, server.URL+"/documents")
	wantStatus(t, resp, http.StatusOK)
	for _, d := range decode[[]documentJSON](t, resp.Body) {
		if d.ID == book.ID || d.Filename == "novel.epub" {
			t.Fatalf("Book appears among Documents: %+v", d)
		}
	}
	wantStatus(t, get(t, server.URL+"/documents/"+book.ID), http.StatusNotFound)
}

func TestRemovedBookIsGoneWithItsBlocksAndCanBeSubmittedAgain(t *testing.T) {
	server := newServer(t)
	content := uniqueBook(t)
	book := decode[bookJSON](t, submitBook(t, server, "a.epub", content).Body)

	wantStatus(t, del(t, server.URL+"/books/"+book.ID), http.StatusNoContent)

	if sources := blockSources(t, book.ID); len(sources) != 0 {
		t.Fatalf("Blocks after removal = %d, want 0", len(sources))
	}
	wantStatus(t, get(t, server.URL+"/books/"+book.ID), http.StatusNotFound)
	wantStatus(t, del(t, server.URL+"/books/"+book.ID), http.StatusNotFound)

	resp := submitBook(t, server, "a.epub", content)
	wantStatus(t, resp, http.StatusCreated)
	if again := decode[bookJSON](t, resp.Body); again.ID == book.ID {
		t.Fatalf("resubmission after removal reused id %s, want a new Book", book.ID)
	}
}

func TestUnknownBookIsNotFound(t *testing.T) {
	server := newServer(t)

	wantStatus(t, get(t, server.URL+"/books/"+uuid.NewString()), http.StatusNotFound)
	wantStatus(t, get(t, server.URL+"/books/not-a-uuid"), http.StatusNotFound)
}
