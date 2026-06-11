package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGettingStartedImagesEmbedded confirms the screenshots are baked into the
// binary and that the file server resolves names containing spaces and commas
// (as the browser sends them, URL-encoded).
func TestGettingStartedImagesEmbedded(t *testing.T) {
	entries, err := fs.ReadDir(gettingStartedFS, "getting-started-images")
	if err != nil {
		t.Fatalf("embed dir: %v", err)
	}
	if len(entries) < 9 {
		t.Fatalf("expected >=9 embedded screenshots, got %d", len(entries))
	}

	h := http.FileServer(http.FS(gettingStartedFS))
	name := "3-2-Activate, deactivate, add, delete, or reorder categories for selected session.png"
	target := (&url.URL{Path: "/getting-started-images/" + name}).String() // encodes spaces -> %20
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", target, nil))
	if rr.Code != 200 {
		t.Fatalf("serving %q: got %d", name, rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		t.Fatalf("content-type = %q, want image/*", ct)
	}
	if rr.Body.Len() < 1000 {
		t.Fatalf("served image too small (%d bytes)", rr.Body.Len())
	}
}
