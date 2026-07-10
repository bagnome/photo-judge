package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactPayloadMasksSecrets(t *testing.T) {
	body := []byte(`{"wifiSSID":"Club","wifiPassword":"hunter2","nested":{"apiToken":"abc"},"n":3}`)
	got := redactPayload("application/json", body)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "abc") {
		t.Fatalf("secret leaked into log rendering: %s", got)
	}
	if !strings.Contains(got, `"wifiPassword":"***"`) || !strings.Contains(got, `"apiToken":"***"`) {
		t.Fatalf("secrets not masked: %s", got)
	}
	if !strings.Contains(got, `"wifiSSID":"Club"`) || !strings.Contains(got, `"n":3`) {
		t.Fatalf("non-secret fields lost: %s", got)
	}
}

func TestCaptureBodyRestoresFullPayload(t *testing.T) {
	s := &server{}
	full := `{"a":1,"password":"secret","b":"` + strings.Repeat("x", 40*1024) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/api/settings", strings.NewReader(full))
	r.Header.Set("Content-Type", "application/json")

	logged := s.captureBody(r)
	if strings.Contains(logged, "secret") {
		t.Fatalf("secret leaked: %s", logged[:80])
	}
	if !strings.Contains(logged, "truncated") {
		t.Fatalf("oversized body should be marked truncated: %s", logged[:80])
	}
	// The handler must still be able to read the ENTIRE original body.
	got, _ := io.ReadAll(r.Body)
	if string(got) != full {
		t.Fatalf("handler saw a different body: len=%d want=%d", len(got), len(full))
	}
}

func TestScrubSecretsTextTruncated(t *testing.T) {
	// Value cut off by truncation (no closing quote) must still be masked, not leaked.
	if got := scrubSecretsText(`{"user":"al","wifiPassword":"hunte`); strings.Contains(got, "hunte") {
		t.Fatalf("unterminated secret leaked: %s", got)
	}
	// Form value with no terminator.
	if got := scrubSecretsText(`user=al&apiToken=abc123`); strings.Contains(got, "abc123") {
		t.Fatalf("form secret leaked: %s", got)
	}
}

func TestWantsPayloadSkipsUploads(t *testing.T) {
	mk := func(method, ct string) *http.Request {
		r := httptest.NewRequest(method, "/x", strings.NewReader("data"))
		r.Header.Set("Content-Type", ct)
		return r
	}
	if !wantsPayload(mk(http.MethodPost, "application/json")) {
		t.Fatal("json POST should be captured")
	}
	if wantsPayload(mk(http.MethodPost, "multipart/form-data; boundary=x")) {
		t.Fatal("multipart upload must NOT be captured")
	}
	if wantsPayload(mk(http.MethodGet, "application/json")) {
		t.Fatal("GET has no body to capture")
	}
}

func TestHandleLogViewTail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("line ")
		sb.WriteByte(byte('0' + i%10))
		sb.WriteString(strings.Repeat("z", 50))
		sb.WriteByte('\n')
	}
	full := sb.String()
	if err := os.WriteFile(filepath.Join(dir, "logs", "2026-07-10.log"), []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &server{baseDir: dir}

	// Full view returns everything.
	rec := httptest.NewRecorder()
	s.handleLogView(rec, httptest.NewRequest("GET", "/api/log/view?name=2026-07-10.log", nil))
	if rec.Code != 200 || rec.Body.String() != full {
		t.Fatalf("full view mismatch: code=%d len=%d", rec.Code, rec.Body.Len())
	}

	// Tail view returns a clean-line-aligned suffix smaller than the whole file.
	rec = httptest.NewRecorder()
	s.handleLogView(rec, httptest.NewRequest("GET", "/api/log/view?name=2026-07-10.log&tail=500", nil))
	out := rec.Body.String()
	if len(out) == 0 || len(out) >= len(full) {
		t.Fatalf("tail should be a proper suffix: got %d of %d", len(out), len(full))
	}
	if !strings.HasSuffix(full, out) {
		t.Fatal("tail is not a suffix of the file")
	}
	if !strings.HasPrefix(out, "line ") {
		t.Fatalf("tail should start on a clean line boundary, got: %q", out[:20])
	}
	if rec.Header().Get("X-Log-Total") == "" || rec.Header().Get("X-Log-Returned") == "" {
		t.Fatal("size headers missing")
	}

	// Path traversal is rejected.
	rec = httptest.NewRecorder()
	s.handleLogView(rec, httptest.NewRequest("GET", "/api/log/view?name=../settings.json", nil))
	if rec.Code != 400 {
		t.Fatalf("traversal should be rejected, got %d", rec.Code)
	}
}
