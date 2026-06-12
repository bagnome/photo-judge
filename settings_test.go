package main

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSettingsDefaultsAndPrecedence(t *testing.T) {
	s := newTestServer(t) // newTestServer calls loadSettings → defaults
	if !s.settings.LanAccess || !s.settings.ImportPhotographer || !s.settings.ImportTitle {
		t.Fatalf("defaults should enable LAN + both imports: %+v", s.settings)
	}

	// A "false" in the properties file wins over the setting.
	s.propsLanAccess, s.propsImportMetadata = false, false
	if s.effLanAccess() || s.effImportPhotographer() || s.effImportTitle() {
		t.Error("properties false should force the effective values off")
	}

	// With properties true, each setting controls its own effective value.
	s.propsLanAccess, s.propsImportMetadata = true, true
	s.settings.ImportTitle = false
	if !s.effLanAccess() || !s.effImportPhotographer() {
		t.Error("properties true + setting true should be on")
	}
	if s.effImportTitle() {
		t.Error("per-field setting should turn just that import off")
	}
}

func TestSettingsHandlerSaveLoad(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"lockScorekeeper": true, "singleLiveScreen": true,
		"pdfHeader": "  Springfield Camera Club  ", // trimmed on save
		"lanAccess": false, "importPhotographer": false, "importTitle": true,
	}
	if rr := postJSON(t, s.handleSettings, body); rr.Code != 200 {
		t.Fatalf("post settings: %d %s", rr.Code, rr.Body.String())
	}
	if !s.settings.LockScorekeeper || !s.settings.SingleLiveScreen ||
		s.settings.PDFHeader != "Springfield Camera Club" || s.settings.LanAccess || s.settings.ImportPhotographer || !s.settings.ImportTitle {
		t.Fatalf("settings not applied as posted: %+v", s.settings)
	}
	// Persisted to settings.json — reloading from disk yields the same.
	s.settings = Settings{}
	s.loadSettings()
	if !s.settings.LockScorekeeper || s.settings.PDFHeader != "Springfield Camera Club" {
		t.Errorf("settings not persisted: %+v", s.settings)
	}

	// PDF header is capped to the two-line limit.
	long := strings.Repeat("x", 200)
	postJSON(t, s.handleSettings, map[string]any{"pdfHeader": long})
	if n := len([]rune(s.settings.PDFHeader)); n != pdfHeaderMax {
		t.Errorf("pdf header should be capped to %d, got %d", pdfHeaderMax, n)
	}
}

func uploadLogo(t *testing.T, s *server, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", filename)
	fw.Write(content)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/logo/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.handleLogoUpload(rr, req)
	return rr
}

func TestLogoLibrary(t *testing.T) {
	s := newTestServer(t)
	if err := os.MkdirAll(s.logoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	png := pngWithText(t, 2, 2, map[string]string{}) // valid PNG, no text

	if rr := uploadLogo(t, s, "club.png", png); rr.Code != 200 {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	if logos := s.listLogos(); len(logos) != 1 || logos[0] != "club.png" {
		t.Fatalf("listLogos = %v", s.listLogos())
	}
	// The first uploaded logo becomes active and resolves into logoFile.
	if s.settings.ActiveLogo != "club.png" {
		t.Errorf("first logo should become active, got %q", s.settings.ActiveLogo)
	}
	if s.logoFile == "" {
		t.Error("logoFile should resolve to the active logo")
	}

	// A second logo doesn't steal active.
	uploadLogo(t, s, "club.png", png) // collides → "club (2).png"
	if s.settings.ActiveLogo != "club.png" {
		t.Errorf("active should stay club.png, got %q", s.settings.ActiveLogo)
	}
	if len(s.listLogos()) != 2 {
		t.Fatalf("expected 2 logos, got %v", s.listLogos())
	}

	// Deleting the active logo clears active and refreshes to another.
	if rr := postJSON(t, s.handleLogoDelete, map[string]string{"file": "club.png"}); rr.Code != 200 {
		t.Fatalf("delete: %d", rr.Code)
	}
	if s.settings.ActiveLogo != "" {
		t.Errorf("active should clear when the active logo is deleted, got %q", s.settings.ActiveLogo)
	}
	if len(s.listLogos()) != 1 {
		t.Errorf("expected 1 logo after delete, got %v", s.listLogos())
	}
	// refreshLogo fell back to the remaining logo.
	if s.logoFile == "" {
		t.Error("logoFile should fall back to the remaining logo")
	}
}
