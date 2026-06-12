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
	Port      int  // TCP port to listen on (ignored when AutoPort is true)
	AutoPort  bool // when true, ask the OS for any free port instead of using Port
	LanAccess bool // when true, bind all interfaces so other LAN devices can connect
}

// configTemplate is the default file seeded on first run. It documents each key.
const configTemplate = `# Photo Judge configuration
# Edit these settings and restart Photo Judge for them to take effect.
# Lines starting with # are comments.

# Port the app listens on. The control page opens at http://127.0.0.1:<port>
# (with the default 80, that's just http://127.0.0.1). Pick another number,
# e.g. 8753, if something else on this machine already uses port 80.
port=80

# Auto-assign a free port. When true, the "port" setting above is ignored and
# the app asks Windows for any available port. Handy if you don't know which
# ports are free. Note: with autoPort on, double-clicking the exe a second time
# can't find the already-running copy (each launch picks a different port), so
# prefer a fixed port for normal use.
autoPort=false

# Allow control from other devices on the same network (a second laptop, a phone,
# or a tablet). When true, the app listens on all network interfaces, the console
# shows its LAN address and a QR code, and you may need to allow it through the
# Windows Firewall (see the User Guide). Set to false to listen on this computer
# only (most private/secure; the LAN address and QR are hidden).
lanAccess=true
`

// loadConfig reads photo-judge.properties from baseDir, seeding it with defaults
// on first run. Unknown keys are ignored and any malformed value falls back to
// its default, so a hand-edited file can never stop the app from starting.
func loadConfig(baseDir string) appConfig {
	cfg := appConfig{Port: defaultPort, AutoPort: false, LanAccess: true}
	path := filepath.Join(baseDir, configFileName)

	data, err := os.ReadFile(path)
	if err != nil {
		// First run (file missing) — seed a documented default for next time.
		if os.IsNotExist(err) {
			_ = os.WriteFile(path, []byte(configTemplate), 0o644)
		}
		return cfg
	}

	// Strip a leading UTF-8 BOM (bytes EF BB BF) — some Windows editors (Notepad)
	// add one when saving, which would otherwise corrupt the very first key.
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")

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
		switch key {
		case "port":
			if n, err := strconv.Atoi(val); err == nil && n >= 0 && n <= 65535 {
				cfg.Port = n
			}
		case "autoport":
			cfg.AutoPort = parseConfigBool(val, false)
		case "lanaccess":
			cfg.LanAccess = parseConfigBool(val, true)
		}
	}
	return cfg
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
