// Package web serves Raggman's UI: static HTML, 98.css and plain JavaScript
// embedded in the binary, so there is nothing to build or run separately.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var static embed.FS

// Handler serves the embedded UI files. Directory listings are not served.
func Handler() http.Handler {
	files, _ := fs.Sub(static, "static") // "static" is embedded above, so this cannot fail
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
