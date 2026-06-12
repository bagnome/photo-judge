package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigSeedsDefaultOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	cfg := loadConfig(dir)
	if cfg.Port != defaultPort || cfg.AutoPort || !cfg.LanAccess {
		t.Fatalf("defaults: got port=%d autoPort=%v lanAccess=%v, want port=%d autoPort=false lanAccess=true", cfg.Port, cfg.AutoPort, cfg.LanAccess, defaultPort)
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
	if again := loadConfig(dir); again.Port != defaultPort || again.AutoPort || !again.LanAccess {
		t.Fatalf("reload of seeded file: got %+v, want defaults", again)
	}
}

func TestLoadConfigParsesValues(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantPort int
		wantAuto bool
		wantLan  bool
	}{
		{"custom port", "port=8753\nautoPort=false\n", 8753, false, true},
		{"autoport true", "port=80\nautoPort=true\n", 80, true, true},
		{"forgiving bool yes", "autoPort=YES\n", defaultPort, true, true},
		{"comments and blanks", "# comment\n\n  port = 9000 \n", 9000, false, true},
		{"malformed port falls back", "port=not-a-number\n", defaultPort, false, true},
		{"out-of-range port falls back", "port=99999\n", defaultPort, false, true},
		{"unknown keys ignored", "color=blue\nport=1234\n", 1234, false, true},
		{"bang comment", "! also a comment\nport=2345\n", 2345, false, true},
		{"leading utf-8 bom", "\xef\xbb\xbfautoPort=true\n", defaultPort, true, true},
		{"lan access off", "lanAccess=false\n", defaultPort, false, false},
		{"lan access off no", "lanAccess=no\n", defaultPort, false, false},
		{"lan malformed keeps default", "lanAccess=maybe\n", defaultPort, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := loadConfig(dir)
			if cfg.Port != tc.wantPort || cfg.AutoPort != tc.wantAuto || cfg.LanAccess != tc.wantLan {
				t.Fatalf("got port=%d autoPort=%v lanAccess=%v, want port=%d autoPort=%v lanAccess=%v",
					cfg.Port, cfg.AutoPort, cfg.LanAccess, tc.wantPort, tc.wantAuto, tc.wantLan)
			}
		})
	}
}
