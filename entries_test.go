package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io/fs"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// jpegBytes encodes a solid-color JPEG of the given size, so orientation detection
// (taller-than-wide = Portrait) and the JPEG/PNG sniff in handleEntrySubmit see a real image.
func jpegBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{90, 140, 200, 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

func submitEntry(t *testing.T, s *server, first, last, cat, title string, img []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("firstName", first)
	_ = mw.WriteField("lastName", last)
	_ = mw.WriteField("category", cat)
	_ = mw.WriteField("title", title)
	fw, _ := mw.CreateFormFile("file", "photo.jpg")
	fw.Write(img)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/entry/submit", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.handleEntrySubmit(rr, req)
	return rr
}

// findEntry returns the ledger record with the given title (and whether it was found).
func findEntry(s *server, sid, title string) (entryRecord, bool) {
	for _, e := range s.loadEntries(sid) {
		if e.Title == title {
			return e, true
		}
	}
	return entryRecord{}, false
}

func TestEntryFlow(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2026-06-18", "Smoke")
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]

	// Switching a screen to the Entry type opens the form, locked to the session.
	s.ensureScreen("Main")
	if rr := postJSON(t, s.handleScreenType, map[string]string{"name": "Main", "type": "entry", "sessionId": ss.ID}); rr.Code != 204 {
		t.Fatalf("screen type: %d %s", rr.Code, rr.Body.String())
	}
	if !s.entryOpen || s.entrySessionID != ss.ID {
		t.Fatalf("entry form not opened/locked (open=%v sid=%q)", s.entryOpen, s.entrySessionID)
	}
	if v := s.buildView(s.screens["Main"]); v.Mode != "entry" {
		t.Fatalf("entry screen view mode = %q, want entry", v.Mode)
	}

	// Submit a landscape and a portrait photo.
	if rr := submitEntry(t, s, "Ada", "Lovelace", cat, "Misty Morning", jpegBytes(40, 20)); rr.Code != 200 {
		t.Fatalf("submit landscape: %d %s", rr.Code, rr.Body.String())
	}
	if rr := submitEntry(t, s, "Ada", "Lovelace", cat, "Tall Tree", jpegBytes(20, 40)); rr.Code != 200 {
		t.Fatalf("submit portrait: %d %s", rr.Code, rr.Body.String())
	}
	if n := s.pendingCount(ss.ID); n != 2 {
		t.Fatalf("pendingCount = %d, want 2", n)
	}

	land, ok := findEntry(s, ss.ID, "Misty Morning")
	if !ok || land.Orientation != "Landscape" {
		t.Fatalf("landscape entry orientation = %q (found=%v), want Landscape", land.Orientation, ok)
	}
	port, ok := findEntry(s, ss.ID, "Tall Tree")
	if !ok || port.Orientation != "Portrait" {
		t.Fatalf("portrait entry orientation = %q (found=%v), want Portrait", port.Orientation, ok)
	}

	// Approve the landscape: it should move into the category/Landscape folder with the
	// photographer set and the order updated; ledger row becomes "approved".
	if rr := postJSON(t, s.handleEntryApprove, map[string]string{"sessionId": ss.ID, "id": land.ID}); rr.Code != 200 {
		t.Fatalf("approve: %d %s", rr.Code, rr.Body.String())
	}
	files := s.photoFiles(ss.ID, cat, "Landscape")
	if len(files) != 1 || files[0] != "Misty Morning.jpg" {
		t.Fatalf("approved file = %v, want [Misty Morning.jpg]", files)
	}
	dir := s.photosDir(ss.ID, cat, "Landscape")
	if got := loadNames(dir)[files[0]]; got != "Ada Lovelace" {
		t.Fatalf("photographer = %q, want Ada Lovelace", got)
	}
	if la, _ := findEntry(s, ss.ID, "Misty Morning"); la.Status != "approved" {
		t.Fatalf("ledger status = %q, want approved", la.Status)
	}

	// Reject the portrait: ledger row becomes "rejected" and it leaves the pending set.
	if rr := postJSON(t, s.handleEntryReject, map[string]string{"sessionId": ss.ID, "id": port.ID}); rr.Code != 200 {
		t.Fatalf("reject: %d %s", rr.Code, rr.Body.String())
	}
	if po, _ := findEntry(s, ss.ID, "Tall Tree"); po.Status != "rejected" {
		t.Fatalf("ledger status = %q, want rejected", po.Status)
	}
	if n := s.pendingCount(ss.ID); n != 0 {
		t.Fatalf("pendingCount after approve+reject = %d, want 0", n)
	}
}

func TestEntryLimitsAndGates(t *testing.T) {
	s := newTestServer(t)
	ss, err := s.createSession("2026-06-18", "")
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	s.entryOpen = true
	s.entrySessionID = ss.ID
	img := jpegBytes(40, 20)

	// Per-category limit of 1: first ok, second rejected.
	s.settings.MaxEntriesPerCategory = 1
	if rr := submitEntry(t, s, "Bob", "Jones", cat, "A", img); rr.Code != 200 {
		t.Fatalf("first entry: %d %s", rr.Code, rr.Body.String())
	}
	if rr := submitEntry(t, s, "Bob", "Jones", cat, "B", img); rr.Code != 409 {
		t.Fatalf("over-limit entry: %d, want 409", rr.Code)
	}

	// Rejecting the first frees the slot, so another submit succeeds.
	a, _ := findEntry(s, ss.ID, "A")
	if rr := postJSON(t, s.handleEntryReject, map[string]string{"sessionId": ss.ID, "id": a.ID}); rr.Code != 200 {
		t.Fatalf("reject A: %d", rr.Code)
	}
	if rr := submitEntry(t, s, "Bob", "Jones", cat, "C", img); rr.Code != 200 {
		t.Fatalf("entry after reject freed slot: %d %s", rr.Code, rr.Body.String())
	}

	// A category that isn't in the session is refused.
	if rr := submitEntry(t, s, "Bob", "Jones", "NoSuchCat", "D", img); rr.Code != 400 {
		t.Fatalf("bad-category entry: %d, want 400", rr.Code)
	}

	// Missing name is refused.
	if rr := submitEntry(t, s, "", "Jones", cat, "E", img); rr.Code != 400 {
		t.Fatalf("no-name entry: %d, want 400", rr.Code)
	}

	// Closing the form blocks new entries.
	if rr := postJSON(t, s.handleEntryClose, map[string]string{}); rr.Code != 204 {
		t.Fatalf("close: %d", rr.Code)
	}
	if s.entryOpen {
		t.Fatal("entryOpen still true after close")
	}
	if rr := submitEntry(t, s, "Bob", "Jones", cat, "F", img); rr.Code != 409 {
		t.Fatalf("closed-form entry: %d, want 409", rr.Code)
	}
}

func TestEntryPerCompetitorLimit(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-06-18", "")
	s.entryOpen = true
	s.entrySessionID = ss.ID
	s.settings.MaxEntriesPerCompetitor = 2
	img := jpegBytes(40, 20)
	c1, c2 := ss.Categories[0], ss.Categories[1]

	if rr := submitEntry(t, s, "Cy", "Young", c1, "one", img); rr.Code != 200 {
		t.Fatalf("entry 1: %d %s", rr.Code, rr.Body.String())
	}
	if rr := submitEntry(t, s, "Cy", "Young", c2, "two", img); rr.Code != 200 {
		t.Fatalf("entry 2: %d %s", rr.Code, rr.Body.String())
	}
	// Third across the whole session exceeds the per-competitor cap.
	if rr := submitEntry(t, s, "Cy", "Young", c1, "three", img); rr.Code != 409 {
		t.Fatalf("entry 3: %d, want 409 (per-competitor cap)", rr.Code)
	}
	// A different competitor is unaffected.
	if rr := submitEntry(t, s, "Dot", "Wells", c1, "other", img); rr.Code != 200 {
		t.Fatalf("other competitor: %d %s", rr.Code, rr.Body.String())
	}
}

// TestEntryAssetsEmbedded guards that the competitor-facing pages are bundled into the exe.
func TestEntryAssetsEmbedded(t *testing.T) {
	for _, name := range []string{"web/landing.html", "web/entry.html", "web/review.html"} {
		if _, err := fs.ReadFile(webFS, name); err != nil {
			t.Errorf("embedded asset %s missing: %v", name, err)
		}
	}
}

// TestExifRoundTrip writes a title + author into a JPEG and reads them back with the
// same metadata reader the upload path uses.
func TestExifRoundTrip(t *testing.T) {
	withExif := jpegSetTitleArtist(jpegBytes(40, 30), "Harbor Lights", "Grace Hopper")
	title, photographer := imageMetadata(bytes.NewReader(withExif), "photo.jpg")
	if title != "Harbor Lights" {
		t.Errorf("title = %q, want Harbor Lights", title)
	}
	if photographer != "Grace Hopper" {
		t.Errorf("photographer = %q, want Grace Hopper", photographer)
	}
	// The result must still decode as a valid JPEG.
	if _, err := jpeg.Decode(bytes.NewReader(withExif)); err != nil {
		t.Errorf("re-encoded JPEG no longer decodes: %v", err)
	}
}

// TestEntrySubmitEmbedsExif checks a submitted JPEG carries its title/author in EXIF.
func TestEntrySubmitEmbedsExif(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-06-18", "")
	cat := ss.Categories[0]
	s.entryOpen, s.entrySessionID = true, ss.ID
	if rr := submitEntry(t, s, "Grace", "Hopper", cat, "Harbor Lights", jpegBytes(40, 30)); rr.Code != 200 {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	rec, _ := findEntry(s, ss.ID, "Harbor Lights")
	raw, err := os.ReadFile(filepath.Join(s.pendingDir(ss.ID), rec.ID+"."+rec.Ext))
	if err != nil {
		t.Fatal(err)
	}
	title, ph := imageMetadata(bytes.NewReader(raw), "x.jpg")
	if title != "Harbor Lights" || ph != "Grace Hopper" {
		t.Errorf("embedded EXIF = (%q,%q), want (Harbor Lights, Grace Hopper)", title, ph)
	}
}

// TestLimitCountsOperatorPhotos verifies limits count photos an operator already added
// (by photographer name), so the same name can't bypass the cap from a fresh browser.
func TestLimitCountsOperatorPhotos(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-06-18", "")
	cat := ss.Categories[0]
	// An operator-added photo attributed to "Grace Hopper".
	dir := s.photosDir(ss.ID, cat, "Landscape")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "op.jpg"), jpegBytes(40, 20), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = setName(dir, "op.jpg", "Grace Hopper")

	s.entryOpen, s.entrySessionID = true, ss.ID
	s.settings.MaxEntriesPerCompetitor = 1
	// Grace already has 1 (the operator photo), so her own submission is over the limit.
	if rr := submitEntry(t, s, "grace", "HOPPER", cat, "mine", jpegBytes(40, 20)); rr.Code != 409 {
		t.Fatalf("submit over operator-counted limit: %d, want 409", rr.Code)
	}
}

func editEntry(t *testing.T, s *server, first, last, id, cat, title string, img []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("firstName", first)
	_ = mw.WriteField("lastName", last)
	_ = mw.WriteField("id", id)
	_ = mw.WriteField("category", cat)
	_ = mw.WriteField("title", title)
	if img != nil {
		fw, _ := mw.CreateFormFile("file", "new.jpg")
		fw.Write(img)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/entry/edit", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.handleEntryEdit(rr, req)
	return rr
}

// TestEntryEditAndRemove covers fixing a rejected entry (back to pending) and removing it.
func TestEntryEditAndRemove(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-06-18", "")
	cat := ss.Categories[0]
	s.entryOpen, s.entrySessionID = true, ss.ID

	if rr := submitEntry(t, s, "Ada", "Lovelace", cat, "Orig", jpegBytes(40, 20)); rr.Code != 200 {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	rec, _ := findEntry(s, ss.ID, "Orig")
	if rr := postJSON(t, s.handleEntryReject, map[string]string{"sessionId": ss.ID, "id": rec.ID}); rr.Code != 200 {
		t.Fatalf("reject: %d", rr.Code)
	}
	// Editing the rejected entry's title puts it back to pending for re-review.
	if rr := editEntry(t, s, "Ada", "Lovelace", rec.ID, cat, "Fixed", nil); rr.Code != 200 {
		t.Fatalf("edit: %d %s", rr.Code, rr.Body.String())
	}
	fixed, ok := findEntry(s, ss.ID, "Fixed")
	if !ok || fixed.Status != "pending" {
		t.Fatalf("edited entry status = %q (found=%v), want pending", fixed.Status, ok)
	}
	// A competitor whose name doesn't match can't edit it.
	if rr := editEntry(t, s, "Some", "One", rec.ID, cat, "Hijack", nil); rr.Code != 404 {
		t.Fatalf("cross-name edit: %d, want 404", rr.Code)
	}
	// Remove it entirely.
	if rr := postJSON(t, s.handleEntryRemove, map[string]string{"firstName": "Ada", "lastName": "Lovelace", "id": rec.ID}); rr.Code != 200 {
		t.Fatalf("remove: %d %s", rr.Code, rr.Body.String())
	}
	if _, ok := findEntry(s, ss.ID, "Fixed"); ok {
		t.Fatal("entry still present after remove")
	}
}

// TestEntryMine returns a competitor's own entries plus their session-wide count.
func TestEntryMine(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-06-18", "")
	cat := ss.Categories[0]
	s.entryOpen, s.entrySessionID = true, ss.ID
	submitEntry(t, s, "Ada", "Lovelace", cat, "one", jpegBytes(40, 20))
	submitEntry(t, s, "Ada", "Lovelace", cat, "two", jpegBytes(20, 40))

	rr := httptest.NewRecorder()
	s.handleEntryMine(rr, httptest.NewRequest("GET", "/api/entry/mine?first=Ada&last=Lovelace", nil))
	if rr.Code != 200 {
		t.Fatalf("mine: %d", rr.Code)
	}
	var resp struct {
		Entries []map[string]any `json:"entries"`
		Total   int              `json:"total"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 2 || resp.Total != 2 {
		t.Fatalf("mine: entries=%d total=%d, want 2/2", len(resp.Entries), resp.Total)
	}
}

// TestEntryAutoApprove: with approval turned off, a submission lands straight in the
// session's photos (no pending review) with its photographer recorded.
func TestEntryAutoApprove(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-06-18", "")
	cat := ss.Categories[0]
	s.entryOpen, s.entrySessionID = true, ss.ID
	s.settings.EntryRequireApproval = false

	if rr := submitEntry(t, s, "Ada", "Lovelace", cat, "Straight In", jpegBytes(40, 20)); rr.Code != 200 {
		t.Fatalf("submit: %d %s", rr.Code, rr.Body.String())
	}
	files := s.photoFiles(ss.ID, cat, "Landscape")
	if len(files) != 1 || files[0] != "Straight In.jpg" {
		t.Fatalf("auto-approved file = %v, want [Straight In.jpg]", files)
	}
	if got := loadNames(s.photosDir(ss.ID, cat, "Landscape"))[files[0]]; got != "Ada Lovelace" {
		t.Fatalf("photographer = %q, want Ada Lovelace", got)
	}
	if n := s.pendingCount(ss.ID); n != 0 {
		t.Fatalf("pendingCount = %d, want 0 (auto-approved)", n)
	}
	if e, _ := findEntry(s, ss.ID, "Straight In"); e.Status != "approved" {
		t.Fatalf("ledger status = %q, want approved", e.Status)
	}
}

// TestEntriesDisabledGate verifies the master toggle blocks submissions and the Entry type.
func TestEntriesDisabledGate(t *testing.T) {
	s := newTestServer(t)
	ss, _ := s.createSession("2026-06-18", "")
	cat := ss.Categories[0]
	s.ensureScreen("Main")
	s.entryOpen, s.entrySessionID = true, ss.ID
	s.settings.EntriesEnabled = false

	if rr := submitEntry(t, s, "Ada", "Lovelace", cat, "x", jpegBytes(40, 20)); rr.Code != 409 {
		t.Fatalf("submit while disabled: %d, want 409", rr.Code)
	}
	if rr := postJSON(t, s.handleScreenType, map[string]string{"name": "Main", "type": "entry", "sessionId": ss.ID}); rr.Code != 409 {
		t.Fatalf("set entry type while disabled: %d, want 409", rr.Code)
	}
}
