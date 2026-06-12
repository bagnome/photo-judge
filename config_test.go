package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigSeedsDefaultOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	cfg := loadConfig(dir)
	if cfg.Port != defaultPort || cfg.AutoPort {
		t.Fatalf("defaults: got port=%d autoPort=%v, want port=%d autoPort=false", cfg.Port, cfg.AutoPort, defaultPort)
	}
	// First run must have written a documented properties file for next time.
	data, err := os.ReadFile(filepath.Join(dir, configFileName))
	if err != nil {
		t.Fatalf("expected %s to be seeded: %v", configFileName, err)
	}
	if len(data) == 0 {
		t.Fatal("seeded config file is empty")
	}
	// Re-loading the seeded file must yield the same defaults.
	if again := loadConfig(dir); again.Port != defaultPort || again.AutoPort {
		t.Fatalf("reload of seeded file: got %+v, want defaults", again)
	}
}

func TestLoadConfigParsesValues(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantPort int
		wantAuto bool
	}{
		{"custom port", "port=8753\nautoPort=false\n", 8753, false},
		{"autoport true", "port=80\nautoPort=true\n", 80, true},
		{"forgiving bool yes", "autoPort=YES\n", defaultPort, true},
		{"comments and blanks", "# comment\n\n  port = 9000 \n", 9000, false},
		{"malformed port falls back", "port=not-a-number\n", defaultPort, false},
		{"out-of-range port falls back", "port=99999\n", defaultPort, false},
		{"unknown keys ignored", "color=blue\nport=1234\n", 1234, false},
		{"bang comment", "! also a comment\nport=2345\n", 2345, false},
		{"leading utf-8 bom", "\xef\xbb\xbfautoPort=true\n", defaultPort, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := loadConfig(dir)
			if cfg.Port != tc.wantPort || cfg.AutoPort != tc.wantAuto {
				t.Fatalf("got port=%d autoPort=%v, want port=%d autoPort=%v", cfg.Port, cfg.AutoPort, tc.wantPort, tc.wantAuto)
			}
		})
	}
}
