package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"testing"
)

// seedLivePhoto drops one image plus its photographer, score and order into a
// session's first category (Landscape), and adds a physical print to the session.
func seedLivePhoto(t *testing.T, s *server, sid string) (cat, file string) {
	t.Helper()
	ss := s.sessionByID(sid)
	if ss == nil || len(ss.Categories) == 0 {
		t.Fatalf("session %s has no categories", sid)
	}
	cat = ss.Categories[0]
	file = "Photo One.jpg"
	dir := s.photosDir(sid, cat, "Landscape")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/"+file, []byte("not-a-real-jpeg-but-fine-for-listing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setName(dir, file, "Ansel Adams"); err != nil {
		t.Fatal(err)
	}
	if err := setScore(dir, file, "9.5"); err != nil {
		t.Fatal(err)
	}
	appendToOrder(dir, []string{file})
	if err := s.savePhysical(sid, []PhysicalPrint{{Category: cat, Title: "Print A", Photographer: "Imogen", Score: "8"}}); err != nil {
		t.Fatal(err)
	}
	return cat, file
}

// exportToFile builds a bundle of the given IDs and returns its bytes.
func exportToFile(t *testing.T, s *server, ids ...string) []byte {
	t.Helper()
	s.mu.Lock()
	buf, entries, err := s.buildExportZip(ids)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("buildExportZip: %v", err)
	}
	if len(entries) != len(ids) {
		t.Fatalf("expected %d entries, got %d", len(ids), len(entries))
	}
	return buf.Bytes()
}

type previewResp struct {
	Token    string `json:"token"`
	Sessions []struct {
		File     string `json:"file"`
		OrigID   string `json:"origId"`
		Kind     string `json:"kind"`
		Date     string `json:"date"`
		Archived bool   `json:"archived"`
	} `json:"sessions"`
}

// importBundle runs the two-step import (preview + commit) of one bundle's bytes,
// importing only the origIds in keep (nil = all), and returns old→new id mapping.
func importBundle(t *testing.T, s *server, bundle []byte, keep map[string]bool) map[string]string {
	t.Helper()

	// Preview (multipart upload).
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("files", "bundle.pjss")
	fw.Write(bundle)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/import/preview", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.handleImportPreview(rr, req)
	if rr.Code != 200 {
		t.Fatalf("preview: %d %s", rr.Code, rr.Body.String())
	}
	var pv previewResp
	if err := json.Unmarshal(rr.Body.Bytes(), &pv); err != nil {
		t.Fatal(err)
	}

	// Commit the selected sessions.
	type sel struct {
		File   string `json:"file"`
		OrigID string `json:"origId"`
	}
	var sels []sel
	for _, e := range pv.Sessions {
		if keep == nil || keep[e.OrigID] {
			sels = append(sels, sel{File: e.File, OrigID: e.OrigID})
		}
	}
	cb, _ := json.Marshal(map[string]any{"token": pv.Token, "select": sels})
	rr2 := httptest.NewRecorder()
	s.handleImportCommit(rr2, httptest.NewRequest("POST", "/api/import/commit", bytes.NewReader(cb)))
	if rr2.Code != 200 {
		t.Fatalf("commit: %d %s", rr2.Code, rr2.Body.String())
	}
	var raw struct {
		Imported []map[string]any `json:"imported"`
	}
	json.Unmarshal(rr2.Body.Bytes(), &raw)
	mapping := map[string]string{}
	for _, m := range raw.Imported {
		mapping[m["oldId"].(string)] = m["newId"].(string)
	}
	return mapping
}

func TestExportImportLiveRoundTrip(t *testing.T) {
	src := newTestServer(t)
	ss, err := src.createSession("2026-05-01", "April club competition — judged by Jane")
	if err != nil {
		t.Fatal(err)
	}
	cat, file := seedLivePhoto(t, src, ss.ID)
	bundle := exportToFile(t, src, ss.ID)

	// Import into a fresh server that already has one session, so the import must get
	// a different ID (IDs are never adopted).
	dst := newTestServer(t)
	if _, err := dst.createSession("2026-04-01", "existing"); err != nil {
		t.Fatal(err)
	}
	mapping := importBundle(t, dst, bundle, nil)
	newID := mapping[ss.ID]
	if newID == "" || newID == ss.ID {
		t.Fatalf("expected a fresh id different from %s, got %q", ss.ID, newID)
	}

	// Photo file, photographer, score, order and physical print all survived.
	dir := dst.photosDir(newID, cat, "Landscape")
	if files := dst.photoFiles(newID, cat, "Landscape"); len(files) != 1 || files[0] != file {
		t.Fatalf("photo not imported: %v", files)
	}
	if loadNames(dir)[file] != "Ansel Adams" {
		t.Errorf("photographer lost: %v", loadNames(dir))
	}
	if loadScores(dir)[file] != "9.5" {
		t.Errorf("score lost: %v", loadScores(dir))
	}
	if ph := dst.loadPhysical(newID); len(ph) != 1 || ph[0].Title != "Print A" {
		t.Errorf("physical print lost: %v", ph)
	}

	// session.json must carry the NEW id and the description.
	imported := dst.sessionByID(newID)
	if imported == nil {
		t.Fatalf("imported session %s not in list", newID)
	}
	if imported.Description != "April club competition — judged by Jane" {
		t.Errorf("description lost: %q", imported.Description)
	}
	if imported.ID != newID {
		t.Errorf("session id not rewritten: %s", imported.ID)
	}
}

func TestExportImportArchivedRoundTrip(t *testing.T) {
	src := newTestServer(t)
	ss, err := src.createSession("2000-01-01", "Old archived night")
	if err != nil {
		t.Fatal(err)
	}
	seedLivePhoto(t, src, ss.ID)
	// Archive it (deletes photos, writes archives/<id>.json).
	rr := postJSON(t, src.handleSessionArchive, map[string]any{"id": ss.ID})
	if rr.Code != 200 {
		t.Fatalf("archive: %d %s", rr.Code, rr.Body.String())
	}
	bundle := exportToFile(t, src, ss.ID)

	dst := newTestServer(t)
	if _, err := dst.createSession("2026-04-01", ""); err != nil { // occupy id 001
		t.Fatal(err)
	}
	mapping := importBundle(t, dst, bundle, nil)
	newID := mapping[ss.ID]
	if newID == "" {
		t.Fatalf("archived session not imported: %v", mapping)
	}
	arch, ok := dst.loadArchive(newID)
	if !ok {
		t.Fatalf("archives/%s.json missing after import", newID)
	}
	if arch.SessionID != newID {
		t.Errorf("archive sessionId not rewritten: %s", arch.SessionID)
	}
	if arch.Description != "Old archived night" {
		t.Errorf("archived description lost: %q", arch.Description)
	}
	if len(arch.Photos) == 0 {
		t.Errorf("archived photo metadata lost")
	}
}

func TestImportSelectionAndDistinctIDs(t *testing.T) {
	src := newTestServer(t)
	a, _ := src.createSession("2026-05-01", "A")
	seedLivePhoto(t, src, a.ID)
	b, _ := src.createSession("2026-05-02", "B")
	seedLivePhoto(t, src, b.ID)
	bundle := exportToFile(t, src, a.ID, b.ID) // a .pjss with two sessions

	dst := newTestServer(t)
	// Import only the second session.
	mapping := importBundle(t, dst, bundle, map[string]bool{b.ID: true})
	if _, skipped := mapping[a.ID]; skipped {
		t.Errorf("session %s should have been skipped (Import unchecked)", a.ID)
	}
	if mapping[b.ID] == "" {
		t.Fatalf("session %s should have been imported: %v", b.ID, mapping)
	}
	if len(dst.sessions) != 1 {
		t.Errorf("expected exactly 1 imported session, got %d", len(dst.sessions))
	}

	// Now import both; the two must get distinct fresh ids.
	dst2 := newTestServer(t)
	m2 := importBundle(t, dst2, bundle, nil)
	if m2[a.ID] == m2[b.ID] {
		t.Errorf("imported sessions share an id: %v", m2)
	}
	if len(dst2.sessions) != 2 {
		t.Errorf("expected 2 imported sessions, got %d", len(dst2.sessions))
	}
}
