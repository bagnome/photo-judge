package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func setPhysical(t *testing.T, s *server, sid, cat string, rows [][3]string) *httptest.ResponseRecorder {
	t.Helper()
	prints := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		prints = append(prints, map[string]string{"title": r[0], "photographer": r[1], "score": r[2]})
	}
	return postJSON(t, s.handlePhysicalSet, map[string]any{"session": sid, "category": cat, "prints": prints})
}

func listPhysical(t *testing.T, s *server, sid string) []PhysicalPrint {
	t.Helper()
	rr := httptest.NewRecorder()
	s.handlePhysicalList(rr, httptest.NewRequest("GET", "/api/session/physical?session="+sid, nil))
	if rr.Code != 200 {
		t.Fatalf("physical list: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Prints []PhysicalPrint `json:"prints"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	return resp.Prints
}

func TestPhysicalPrintsCRUD(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	c1, c2 := ss.Categories[0], ss.Categories[1]

	// Set category 1 with two real rows plus one empty row (which must be dropped).
	if rr := setPhysical(t, s, ss.ID, c1, [][3]string{{"Sunset", "Jane", "8.5"}, {"Dawn", "Bob", "7"}, {"", "", ""}}); rr.Code != 204 {
		t.Fatalf("set c1: %d %s", rr.Code, rr.Body.String())
	}
	if got := listPhysical(t, s, ss.ID); len(got) != 2 {
		t.Fatalf("after set c1: want 2 prints, got %d (%+v)", len(got), got)
	}

	// A different category accumulates alongside the first.
	if rr := setPhysical(t, s, ss.ID, c2, [][3]string{{"Macro Drop", "Vivian", "9"}}); rr.Code != 204 {
		t.Fatalf("set c2: %d", rr.Code)
	}
	if got := listPhysical(t, s, ss.ID); len(got) != 3 {
		t.Fatalf("after set c2: want 3, got %d", len(got))
	}

	// Re-setting a category replaces only that category's rows.
	if rr := setPhysical(t, s, ss.ID, c1, [][3]string{{"Only One", "Solo", "5"}}); rr.Code != 204 {
		t.Fatalf("replace c1: %d", rr.Code)
	}
	got := listPhysical(t, s, ss.ID)
	if len(got) != 2 {
		t.Fatalf("after replacing c1: want 2 (1 c1 + 1 c2), got %d (%+v)", len(got), got)
	}
	c1Count := 0
	for _, p := range got {
		if p.Category == c1 {
			c1Count++
			if p.Title != "Only One" {
				t.Errorf("c1 row not replaced: %+v", p)
			}
		}
	}
	if c1Count != 1 {
		t.Errorf("want 1 c1 row after replace, got %d", c1Count)
	}

	// A category not in the session is rejected.
	if rr := setPhysical(t, s, ss.ID, "Not A Category", [][3]string{{"x", "", ""}}); rr.Code != 400 {
		t.Errorf("bad category: want 400, got %d", rr.Code)
	}
	// Unknown session is rejected.
	if rr := postJSON(t, s.handlePhysicalSet, map[string]any{"session": "999", "category": c1, "prints": []any{}}); rr.Code != 404 {
		t.Errorf("unknown session: want 404, got %d", rr.Code)
	}
}

func TestPhysicalPrintsArchived(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2000-01-01") // past, so it can be archived
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	if rr := setPhysical(t, s, ss.ID, cat, [][3]string{{"Print A", "Ansel Adams", "9.0"}}); rr.Code != 204 {
		t.Fatalf("set physical: %d", rr.Code)
	}

	if rr := postJSON(t, s.handleSessionArchive, map[string]string{"id": ss.ID}); rr.Code != 200 {
		t.Fatalf("archive: %d %s", rr.Code, rr.Body.String())
	}

	arch, ok := s.loadArchive(ss.ID)
	if !ok {
		t.Fatal("archive not found")
	}
	if len(arch.PhysicalPrints) != 1 || arch.PhysicalPrints[0].Title != "Print A" || arch.PhysicalPrints[0].Photographer != "Ansel Adams" {
		t.Fatalf("physical prints not archived: %+v", arch.PhysicalPrints)
	}

	// Searching archives by a physical print's photographer matches the session.
	if got := listArchives(t, s, "photographer=ansel"); len(got) != 1 {
		t.Errorf("photographer search over physical prints: want 1, got %d", len(got))
	}
	if got := listArchives(t, s, "title=print"); len(got) != 1 {
		t.Errorf("title search over physical prints: want 1, got %d", len(got))
	}

	// The archive PDF renders with physical prints present.
	pr := httptest.NewRecorder()
	s.handleArchivePDF(pr, httptest.NewRequest("GET", "/api/archive/pdf?session="+ss.ID, nil))
	if pr.Code != 200 || pr.Body.Len() < 100 {
		t.Errorf("archive pdf with physical prints: code %d, %d bytes", pr.Code, pr.Body.Len())
	}
}
