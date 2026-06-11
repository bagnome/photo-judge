package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	s := &server{baseDir: dir, screens: map[string]*Screen{}, h: newHub(), shutdownCh: make(chan struct{})}
	if err := os.MkdirAll(filepath.Join(dir, "photos"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.loadCategories() // seeds s.categories from defaults (first-session seed)
	return s
}

func postJSON(t *testing.T, h http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest("POST", "/", bytes.NewReader(b)))
	return rr
}

type catView struct {
	Active   []string `json:"active"`
	Inactive []string `json:"inactive"`
	Used     []string `json:"used"`
}

func detail(t *testing.T, s *server, id string) catView {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handleSessionCategories(rr, httptest.NewRequest("GET", "/api/session/categories?session="+id, nil))
	if rr.Code != 200 {
		t.Fatalf("categories GET: %d %s", rr.Code, rr.Body.String())
	}
	var d catView
	if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func TestCategoryManager(t *testing.T) {
	s := newTestServer(t)

	// First session inherits the default seed as its active slate.
	ss, err := s.createSession("2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	if d := detail(t, s, ss.ID); len(d.Active) != len(defaultCategories) || len(d.Inactive) != 0 {
		t.Fatalf("first session slate = %v / %v, want defaults", d.Active, d.Inactive)
	}

	// Add lands in Active (at the end).
	if rr := postJSON(t, s.handleCategoryAdd, map[string]string{"session": ss.ID, "name": "Nature"}); rr.Code != 200 {
		t.Fatalf("add: %d %s", rr.Code, rr.Body.String())
	}
	d := detail(t, s, ss.ID)
	if d.Active[len(d.Active)-1] != "Nature" {
		t.Fatalf("Nature not last active: %v", d.Active)
	}

	// Duplicate add (case-insensitive) is rejected.
	if rr := postJSON(t, s.handleCategoryAdd, map[string]string{"session": ss.ID, "name": "nature"}); rr.Code != 409 {
		t.Fatalf("dup add want 409, got %d", rr.Code)
	}

	// Deactivate then reactivate.
	if rr := postJSON(t, s.handleCategoryDeactivate, map[string]string{"session": ss.ID, "name": "Wildlife"}); rr.Code != 200 {
		t.Fatalf("deactivate: %d %s", rr.Code, rr.Body.String())
	}
	d = detail(t, s, ss.ID)
	if has(d.Active, "Wildlife") || !has(d.Inactive, "Wildlife") {
		t.Fatalf("Wildlife not deactivated: %v / %v", d.Active, d.Inactive)
	}
	if rr := postJSON(t, s.handleCategoryActivate, map[string]string{"session": ss.ID, "name": "Wildlife"}); rr.Code != 200 {
		t.Fatalf("activate: %d %s", rr.Code, rr.Body.String())
	}
	d = detail(t, s, ss.ID)
	if d.Active[len(d.Active)-1] != "Wildlife" {
		t.Fatalf("Wildlife not reactivated at end: %v", d.Active)
	}

	// Reorder must be a permutation of the active set.
	rev := make([]string, len(d.Active))
	for i, c := range d.Active {
		rev[len(d.Active)-1-i] = c
	}
	if rr := postJSON(t, s.handleCategoryReorder, map[string]any{"session": ss.ID, "order": rev}); rr.Code != 200 {
		t.Fatalf("reorder: %d %s", rr.Code, rr.Body.String())
	}
	if got := detail(t, s, ss.ID).Active; strings.Join(got, "|") != strings.Join(rev, "|") {
		t.Fatalf("reorder not applied: %v want %v", got, rev)
	}
	if rr := postJSON(t, s.handleCategoryReorder, map[string]any{"session": ss.ID, "order": []string{"Nature"}}); rr.Code != 400 {
		t.Fatalf("non-permutation reorder want 400, got %d", rr.Code)
	}

	// A category with photos is "used" and cannot be deleted (only deactivated).
	photo := filepath.Join(s.baseDir, "photos", ss.ID, "Nature", "Landscape", "x.jpg")
	if err := os.WriteFile(photo, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !has(detail(t, s, ss.ID).Used, "Nature") {
		t.Fatal("Nature should report used")
	}
	if rr := postJSON(t, s.handleCategoryDelete, map[string]string{"session": ss.ID, "name": "Nature"}); rr.Code != 409 {
		t.Fatalf("delete used want 409, got %d %s", rr.Code, rr.Body.String())
	}
	// Empty it → delete succeeds and the folder is removed.
	os.Remove(photo)
	if rr := postJSON(t, s.handleCategoryDelete, map[string]string{"session": ss.ID, "name": "Nature"}); rr.Code != 200 {
		t.Fatalf("delete unused: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.baseDir, "photos", ss.ID, "Nature")); !os.IsNotExist(err) {
		t.Fatal("Nature folder should be gone after delete")
	}

	// Path-traversal guard on the name.
	if rr := postJSON(t, s.handleCategoryAdd, map[string]string{"session": ss.ID, "name": "../evil"}); rr.Code != 400 {
		t.Fatalf("traversal add want 400, got %d", rr.Code)
	}

	// A new session inherits the latest session's active order + inactive set.
	postJSON(t, s.handleCategoryDeactivate, map[string]string{"session": ss.ID, "name": "Macro"})
	prev := detail(t, s, ss.ID)
	ss2, err := s.createSession("2026-06-02")
	if err != nil {
		t.Fatal(err)
	}
	d2 := detail(t, s, ss2.ID)
	if strings.Join(d2.Active, "|") != strings.Join(prev.Active, "|") {
		t.Fatalf("inheritance active mismatch: %v vs %v", d2.Active, prev.Active)
	}
	if !has(d2.Inactive, "Macro") {
		t.Fatalf("inheritance should carry inactive Macro: %v", d2.Inactive)
	}

	// Independence: editing ss2 must not change ss.
	postJSON(t, s.handleCategoryDeactivate, map[string]string{"session": ss2.ID, "name": d2.Active[0]})
	if after := detail(t, s, ss.ID); strings.Join(after.Active, "|") != strings.Join(prev.Active, "|") {
		t.Fatalf("ss changed after editing ss2: %v vs %v", after.Active, prev.Active)
	}
}

// TestSessionBackCompat verifies an old session.json without inactiveCategories loads.
func TestSessionBackCompat(t *testing.T) {
	s := newTestServer(t)
	id := "007"
	dir := filepath.Join(s.baseDir, "photos", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"id":"007","date":"2026-01-01","created":"2026-01-01T00:00:00Z","categories":["Pictorial","Wildlife"]}`
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s.scanSessions()
	d := detail(t, s, id)
	if strings.Join(d.Active, "|") != "Pictorial|Wildlife" || len(d.Inactive) != 0 {
		t.Fatalf("back-compat load: active=%v inactive=%v", d.Active, d.Inactive)
	}
}
