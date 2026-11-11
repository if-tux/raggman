package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUIIsServedFromTheBinary(t *testing.T) {
	server := newServer(t)

	for _, tt := range []struct {
		path, contentType, contains string
	}{
		{path: "/", contentType: "text/html", contains: "<title>Raggman</title>"},
		{path: "/app.js", contentType: "text/javascript", contains: "/questions"},
		{path: "/vendor/98css/98.css", contentType: "text/css", contains: ".window"},
	} {
		resp := get(t, server.URL+tt.path)
		wantStatus(t, resp, http.StatusOK)
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, tt.contentType) {
			t.Fatalf("%s: Content-Type = %q, want %s", tt.path, ct, tt.contentType)
		}
		if body, _ := io.ReadAll(resp.Body); !strings.Contains(string(body), tt.contains) {
			t.Fatalf("%s does not contain %q", tt.path, tt.contains)
		}
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	server := newServer(t)

	wantStatus(t, get(t, server.URL+"/no-such-page"), http.StatusNotFound)
}

func TestDirectoriesAreNotListed(t *testing.T) {
	server := newServer(t)

	wantStatus(t, get(t, server.URL+"/vendor/98css/"), http.StatusNotFound)
}
