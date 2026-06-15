package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGettingStartedImagesEmbedded confirms the How To screenshots are baked into
// the binary and that the file server serves one via its URL-encoded path. The
// filenames change as the guides are updated, so it picks a real embedded image
// rather than hard-coding a name.
func TestGettingStartedImagesEmbedded(t *testing.T) {
	entries, err := fs.ReadDir(gettingStartedFS, "getting-started-images")
	if err != nil {
		t.Fatalf("embed dir: %v", err)
	}
	var name string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
			name = e.Name()
			break
		}
	}
	if name == "" {
		t.Fatalf("no embedded screenshots found (got %d entries)", len(entries))
	}

	h := http.FileServer(http.FS(gettingStartedFS))
	target := (&url.URL{Path: "/getting-started-images/" + name}).String() // URL-encodes any spaces/commas
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
