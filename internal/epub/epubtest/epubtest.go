// Package epubtest builds small EPUBs for tests.
package epubtest

import (
	"archive/zip"
	"bytes"
	"fmt"
	"slices"
	"strings"
)

// Container is the META-INF/container.xml of the EPUBs built here.
const Container = `<?xml version="1.0"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

// Zip packs files into an EPUB-shaped zip: the mimetype entry first, then the
// rest in name order. A "mimetype" key overrides the EPUB mimetype.
func Zip(files map[string]string) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	write := func(name, content string) {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			panic(err)
		}
		_, _ = f.Write([]byte(content))
	}
	mimetype, ok := files["mimetype"]
	if !ok {
		mimetype = "application/epub+zip"
	}
	write("mimetype", mimetype)
	names := make([]string, 0, len(files))
	for name := range files {
		if name != "mimetype" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		write(name, files[name])
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// Files returns the entries of an EPUB 3 with one XHTML document per chapter;
// each chapter is the inner markup of its <body>. Callers may add or replace
// entries before passing them to Zip.
func Files(title, language string, chapters ...string) map[string]string {
	files := map[string]string{"META-INF/container.xml": Container}
	var manifest, spine, nav strings.Builder
	for i, body := range chapters {
		name := fmt.Sprintf("ch%d.xhtml", i+1)
		files["OEBPS/"+name] = Chapter(body)
		fmt.Fprintf(&manifest, `<item id="ch%d" href="%s" media-type="application/xhtml+xml"/>`, i+1, name)
		fmt.Fprintf(&spine, `<itemref idref="ch%d"/>`, i+1)
		fmt.Fprintf(&nav, `<li><a href="%s">Chapter %d</a></li>`, name, i+1)
	}
	files["OEBPS/nav.xhtml"] = Chapter(`<nav epub:type="toc"><ol>` + nav.String() + `</ol></nav>`)
	files["OEBPS/content.opf"] = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id">
  <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
    <dc:identifier id="id">urn:uuid:test</dc:identifier>
    <dc:title>%s</dc:title>
    <dc:language>%s</dc:language>
    <meta property="dcterms:modified">2020-01-01T00:00:00Z</meta>
    <meta name="cover" content="cover">
    </meta>
  </metadata>
  <manifest>
    <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
    %s
  </manifest>
  <spine>%s</spine>
</package>`, title, language, manifest.String(), spine.String())
	return files
}

// Book is Zip(Files(...)).
func Book(title, language string, chapters ...string) []byte {
	return Zip(Files(title, language, chapters...))
}

// Chapter wraps body markup in an XHTML document.
func Chapter(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<head><title>ignored</title><style>p { margin: 0 }</style></head>
<body>` + body + `</body>
</html>`
}
