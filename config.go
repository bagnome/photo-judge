// Runtime configuration for Photo Judge, read from a plain-text properties file
// next to the exe (photo-judge.properties). The file is created with defaults on
// first run; users edit it and restart. Standard library only, like everything else.
package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// configFileName is the properties file created/read next to the exe.
const configFileName = "photo-judge.properties"

// defaultPort is what Photo Judge listens on out of the box. Port 80 means the
// console URL is just http://127.0.0.1 (no :port needed).
const defaultPort = 80

// appConfig holds the settings read from photo-judge.properties.
type appConfig struct {
	Port           int  // TCP port to listen on (ignored when AutoPort is true)
	AutoPort       bool // when true, ask the OS for any free port instead of using Port
	LanAccess      bool // when true, bind all interfaces so other LAN devices can connect
	ImportMetadata bool // when true, read photographer/title from image metadata on upload
}

// configHeader and configBlocks make up the seeded properties file. Splitting it
// into per-setting blocks lets the app APPEND any settings a newer version added to
// an existing file, so upgrading users see (and can change) the new options.
const configHeader = `# Photo Judge configuration
# Edit these settings and restart Photo Judge for them to take effect.
# Lines starting with # are comments.
`

type configBlock struct {
	key   string // lower-case property name, as written in the file
	block string // documentation comment(s) followed by "key=default"
}

var configBlocks = []configBlock{
	{"port", `# Port the app listens on. The control page opens at http://127.0.0.1:<port>
# (with the default 80, that's just http://127.0.0.1). Pick another number,
# e.g. 8753, if something else on this machine already uses port 80.
port=80`},
	{"autoport", `# Auto-assign a free port. When true, the "port" setting above is ignored and
# the app asks Windows for any available port. Handy if you don't know which
# ports are free. Note: with autoPort on, double-clicking the exe a second time
# can't find the already-running copy (each launch picks a different port), so
# prefer a fixed port for normal use.
autoPort=false`},
	{"lanaccess", `# Allow control from other devices on the same network (a second laptop, a phone,
# or a tablet). When true, the app listens on all network interfaces, the console
# shows its LAN address and a QR code, and you may need to allow it through the
# Windows Firewall (see the User Guide). Set to false to listen on this computer
# only (most private/secure; the LAN address and QR are hidden).
lanAccess=true`},
	{"importmetadata", `# Read the photographer and title from a photo's embedded metadata when it's
# uploaded. When true and a photo has an EXIF/PNG "artist/author" value, it's filled
# in as the photographer; if it has a "title" value, the photo is saved under that
# title (its on-screen name) instead of the original filename. Anything missing
# falls back to the usual behavior, and you can still edit the photographer by hand.
importMetadata=false`},
}

// configTemplate assembles the full default file from the header and every block.
func configTemplate() string {
	var b strings.Builder
	b.WriteString(configHeader)
	for _, blk := range configBlocks {
		b.WriteString("\n")
		b.WriteString(blk.block)
		b.WriteString("\n")
	}
	return b.String()
}

// loadConfig reads photo-judge.properties from baseDir, seeding it with defaults
// on first run. Unknown keys are ignored and any malformed value falls back to
// its default, so a hand-edited file can never stop the app from starting.
func loadConfig(baseDir string) appConfig {
	cfg := appConfig{Port: defaultPort, AutoPort: false, LanAccess: true, ImportMetadata: false}
	path := filepath.Join(baseDir, configFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		// First run (file missing) — seed a documented default for next time.
		if os.IsNotExist(err) {
			_ = os.WriteFile(path, []byte(configTemplate()), 0o644)
		}
		return cfg
	}

	// Strip a leading UTF-8 BOM (bytes EF BB BF) — some Windows editors (Notepad)
	// add one when saving, which would otherwise corrupt the very first key.
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")

	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		seen[key] = true
		switch key {
		case "port":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 && n <= 65535 {
				cfg.Port = n
			}
		case "autoport":
			cfg.AutoPort = parseConfigBool(val, false)
		case "lanaccess":
			cfg.LanAccess = parseConfigBool(val, true)
		case "importmetadata":
			cfg.ImportMetadata = parseConfigBool(val, false)
		}
	}

	// Append any settings a newer version added that this (older) file is missing, so
	// upgrading users get the new options documented without losing their edits.
	appendMissingConfig(path, seen)
	return cfg
}

// appendMissingConfig appends the documentation block for any known setting absent
// from the file. Best-effort and append-only — it never rewrites existing lines.
func appendMissingConfig(path string, seen map[string]bool) {
	var add strings.Builder
	for _, blk := range configBlocks {
		if !seen[blk.key] {
			add.WriteString("\n")
			add.WriteString(blk.block)
			add.WriteString("\n")
		}
	}
	if add.Len() == 0 {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(add.String())
}

// parseConfigBool reads a forgiving boolean: true/yes/on/1 are true and
// false/no/off/0 are false (any capitalization). An unrecognized value returns
// def, so a typo can't silently flip a setting away from its intended default.
func parseConfigBool(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1":
		return true
	case "false", "no", "off", "0":
		return false
	}
	return def
}
