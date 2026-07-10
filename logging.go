// Debug logging for Photo Judge — an operator-toggled trace of what the app is
// doing, for diagnosing problems after the fact. It is OFF by default and, when
// turned on from the Settings page, appends to a single rolling log file per day
// (e.g. 2026-07-10.log) in a logs\ folder next to the exe.
//
// Each line is columnar — timestamp, type (REQUEST / ERROR / EVENT), a file:line
// source location when there is one (app events carry it; requests don't), then the
// message — so a day's activity reads down the page like a table.
//
// Shape (all operator-tunable on the Settings page):
//   - a master toggle, plus a detail level (events / requests / all) that decides how
//     much of the traffic is captured;
//   - a size cap in MB — when the folder exceeds it, the oldest DAY files are deleted;
//   - an auto-off timer so logging can't be left running forever by accident;
//   - an "always log errors" switch that captures errors even when the master toggle
//     is off (errors are rare, so this is cheap and safe to leave on).
//
// The download/browse/delete-all UI lives on a separate Logs page (web/logs.html);
// this file holds the writer, the size-cap bookkeeping, the auto-off ticker, the
// request middleware, and the HTTP handlers. Standard library only, like the rest.
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// logEntry is one on-disk day file, tracked in memory so the size cap can be enforced
// without re-statting the whole folder on every request.
type logEntry struct {
	name string
	size int64
}

// logDir is where the per-day log files live, next to the exe.
func (s *server) logDir() string { return filepath.Join(s.baseDir, "logs") }

// ---- config snapshot (hot path) ------------------------------------------

// applyLogConfig copies the logging-relevant settings into the lock-free atomics read
// on every request, and evaluates the auto-off timer. Caller holds s.mu.
func (s *server) applyLogConfig() {
	switch s.settings.LogLevel {
	case logLevelAll:
		s.logLevelV.Store(2)
	case logLevelEvents:
		s.logLevelV.Store(0)
	default:
		s.logLevelV.Store(1)
	}
	s.logAlwaysErr.Store(s.settings.LogAlwaysErrors)
	s.logMaxBytes.Store(int64(s.settings.LogMaxMB) * 1024 * 1024)
	s.logDebugOn.Store(s.settings.LoggingEnabled && !s.logExpired())
}

// logExpired reports whether an auto-off deadline has passed. Caller holds s.mu.
func (s *server) logExpired() bool {
	m := s.settings.LogAutoOffMinutes
	if m <= 0 || s.settings.LogEnabledAt <= 0 {
		return false
	}
	return time.Now().Unix() >= s.settings.LogEnabledAt+int64(m)*60
}

// logRemainingSeconds returns seconds until auto-off, or -1 when no auto-off applies
// (disabled, or no timer set). Caller holds s.mu.
func (s *server) logRemainingSeconds() int64 {
	if !s.settings.LoggingEnabled || s.settings.LogAutoOffMinutes <= 0 || s.settings.LogEnabledAt <= 0 {
		return -1
	}
	left := s.settings.LogEnabledAt + int64(s.settings.LogAutoOffMinutes)*60 - time.Now().Unix()
	if left < 0 {
		left = 0
	}
	return left
}

// startLogAutoOff runs a background ticker that flips the master switch off once the
// auto-off deadline passes, so a run left logging on eventually stops on its own.
func (s *server) startLogAutoOff() {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-s.shutdownCh:
				return
			case <-t.C:
				s.mu.Lock()
				flip := s.settings.LoggingEnabled && s.logExpired()
				if flip {
					s.settings.LoggingEnabled = false
					s.settings.LogEnabledAt = 0
					s.applyLogConfig()
					_ = s.saveSettings()
				}
				s.mu.Unlock()
				if flip {
					log.Printf("debug logging auto-turned off after the configured time limit")
					s.pushConsole() // nudge any open Settings/console view to refresh
				}
			}
		}
	}()
}

// ---- writing --------------------------------------------------------------

// logLevelSkipNoise lists the high-frequency, low-signal paths that the "requests"
// level leaves out (they're only written at the "all" level, or when they error).
// These are the SSE stream, the state/board polls, and the image/asset fetches.
func logIsNoise(path string) bool {
	switch path {
	case "/api/events", "/api/state", "/api/netinfo",
		"/api/judge/state", "/api/judge/board", "/api/entry/state", "/api/entry/mine":
		return true
	}
	return strings.HasPrefix(path, "/api/photo") ||
		strings.HasPrefix(path, "/api/logo") ||
		strings.HasPrefix(path, "/api/entry/photo") ||
		strings.HasPrefix(path, "/getting-started-images/")
}

// shouldLogRequest decides whether a finished request is written, given the current
// config snapshot. When logging is fully on, every error (status >= 400) is captured at
// any level. When the master switch is off, "always log errors" still captures genuine
// server failures (5xx) as a safety net — but not routine 4xx (a favicon probe, a bad
// link), which would otherwise be noise with nothing actually wrong in the app.
func (s *server) shouldLogRequest(path string, status int) bool {
	if status >= 500 && s.logAlwaysErr.Load() {
		return true
	}
	if !s.logDebugOn.Load() {
		return false
	}
	if status >= 400 {
		return true // errors log at every level while logging is on
	}
	switch s.logLevelV.Load() {
	case 0: // events only — no per-request access lines except errors (handled above)
		return false
	case 2: // all — everything, including the noisy traffic
		return true
	default: // requests — the meaningful calls, minus the high-frequency noise
		return !logIsNoise(path)
	}
}

// logColWidths line up the columns so a day's file reads like a table in any monospace
// viewer: timestamp (fixed 29), type (7), file:line (20), then the free-form message.
const (
	logTSWidth   = 29 // "2006-01-02 15:04:05.000 -0700"
	logTypeWidth = 7  // REQUEST / ERROR / EVENT
	logLocWidth  = 20 // file.go:123 (blank for requests)
)

// logColumns formats one aligned, space-separated log row (with trailing newline).
func logColumns(ts, typ, loc, msg string) string {
	return fmt.Sprintf("%-*s  %-*s  %-*s  %s\n", logTSWidth, ts, logTypeWidth, typ, logLocWidth, loc, msg)
}

// appendLog writes one columnar line to today's log file, opening (and rolling to) the
// day file as needed and enforcing the size cap. Best-effort: logging must never
// disrupt a request, so all errors are swallowed.
func (s *server) appendLog(typ, loc, msg string) {
	now := time.Now()
	line := logColumns(now.Format("2006-01-02 15:04:05.000 -0700"), typ, loc, msg)
	day := now.Format("2006-01-02")
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logCurFile == nil || s.logCurDay != day {
		s.openDayFileLocked(day)
	}
	if s.logCurFile == nil {
		return // open failed
	}
	n, err := s.logCurFile.WriteString(line)
	if err != nil {
		return
	}
	s.addSizeLocked(int64(n))
	s.enforceCapLocked()
}

// openDayFileLocked (re)opens the append handle for the given day, ensures the day has
// an index entry as the newest element, and writes a header row on a brand-new file.
// Caller holds s.logMu.
func (s *server) openDayFileLocked(day string) {
	if s.logCurFile != nil {
		s.logCurFile.Close()
		s.logCurFile = nil
	}
	if err := os.MkdirAll(s.logDir(), 0o755); err != nil {
		return
	}
	name := day + ".log"
	f, err := os.OpenFile(filepath.Join(s.logDir(), name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	s.logCurFile = f
	s.logCurDay = day
	var sz int64
	if info, err := f.Stat(); err == nil {
		sz = info.Size()
	}
	// The active day is always the newest, so it belongs at the end of the index.
	if len(s.logIndex) == 0 || s.logIndex[len(s.logIndex)-1].name != name {
		s.logIndex = append(s.logIndex, logEntry{name: name, size: sz})
		s.logTotal += sz
	}
	if sz == 0 { // header row on a freshly-created file
		if n, err := f.WriteString(logColumns("TIMESTAMP", "TYPE", "FILE:LINE", "MESSAGE")); err == nil {
			s.addSizeLocked(int64(n))
		}
	}
}

// addSizeLocked folds n bytes into the active day's size and the running total. The
// active day is the last index entry. Caller holds s.logMu.
func (s *server) addSizeLocked(n int64) {
	if len(s.logIndex) > 0 {
		s.logIndex[len(s.logIndex)-1].size += n
	}
	s.logTotal += n
}

// enforceCapLocked deletes the oldest DAY files until the folder is within the byte
// cap, never touching the currently-open (newest) day file. Caller holds s.logMu.
func (s *server) enforceCapLocked() {
	max := s.logMaxBytes.Load()
	if max <= 0 {
		return
	}
	for s.logTotal > max && len(s.logIndex) > 1 {
		e := s.logIndex[0]
		_ = os.Remove(filepath.Join(s.logDir(), e.name))
		s.logTotal -= e.size
		s.logIndex = s.logIndex[1:]
	}
}

// seedLogIndex scans logs\ at startup so the size cap and the Logs page start from an
// accurate picture of any files a previous run left behind. Caller need not hold locks
// (runs before serving).
func (s *server) seedLogIndex() {
	entries, err := os.ReadDir(s.logDir())
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	sizes := map[string]int64{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		names = append(names, e.Name())
		sizes[e.Name()] = info.Size()
	}
	sort.Strings(names) // filename order == chronological order
	s.logMu.Lock()
	s.logIndex = s.logIndex[:0]
	s.logTotal = 0
	for _, n := range names {
		s.logIndex = append(s.logIndex, logEntry{name: n, size: sizes[n]})
		s.logTotal += sizes[n]
	}
	s.enforceCapLocked()
	s.logMu.Unlock()
}

// ---- event tee (captures log.Printf output) -------------------------------

// logTee mirrors the standard logger to stderr (so the console window still shows
// startup/activity lines) and, when logging is on, appends each line as an EVENT row.
// It also catches error-shaped lines when "always log errors" is set, so a failure
// recorded via log.Printf survives even with the master switch off. The standard
// logger runs with Lshortfile, so each captured line carries a "file.go:NN:" source
// location that becomes the row's file:line column.
type logTee struct {
	s   *server
	std io.Writer // the original os.Stderr
}

func (t *logTee) Write(p []byte) (int, error) {
	n, err := t.std.Write(p) // always keep the live console output
	line := strings.TrimRight(string(p), "\r\n")
	if line != "" {
		on := t.s.logDebugOn.Load()
		errish := t.s.logAlwaysErr.Load() && looksLikeError(line)
		if on || errish {
			loc, msg := splitFileLine(stripLogTimestamp(line))
			t.s.appendLog("EVENT", loc, msg)
		}
	}
	return n, err
}

// looksLikeError is a heuristic for the "always log errors" path over free-form log
// lines: the app's own failure logs use words like these. Request-level errors are
// caught precisely by status code; this just catches the log.Printf ones too.
func looksLikeError(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "error") || strings.Contains(l, "fail") ||
		strings.Contains(l, "could not") || strings.Contains(l, "cannot") ||
		strings.Contains(l, "panic")
}

// stripLogTimestamp removes the standard logger's "2006/01/02 15:04:05 " prefix
// (LstdFlags) so appendLog can stamp its own consistent timestamp column instead.
func stripLogTimestamp(line string) string {
	const n = len("2006/01/02 15:04:05 ")
	if len(line) >= n && line[4] == '/' && line[7] == '/' && line[10] == ' ' &&
		line[13] == ':' && line[16] == ':' && line[19] == ' ' {
		return line[n:]
	}
	return line
}

// splitFileLine peels a leading "file.go:123: " source location (added by Lshortfile)
// off a log line, returning it as the file:line column and the rest as the message.
// A line without that shape (no source location) yields an empty column.
func splitFileLine(rest string) (loc, msg string) {
	i := strings.Index(rest, ": ")
	if i <= 0 {
		return "", rest
	}
	cand := rest[:i]
	if c := strings.LastIndexByte(cand, ':'); c > 0 && strings.Contains(cand[:c], ".") && allDigits(cand[c+1:]) {
		return cand, rest[i+2:]
	}
	return "", rest
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---- request middleware ---------------------------------------------------

// logResponseWriter records the status and byte count of a response while passing
// everything through to the real writer. It preserves http.Flusher so Server-Sent
// Events (the /api/events stream) keep working when wrapped.
type logResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *logResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *logResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *logResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// withLogging wraps the whole mux. It's the single choke point for request logging:
// the wrapper is cheap when logging is fully off (two atomic loads), and only appends
// a row when the current config says this request should be captured.
func (s *server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fast path: nothing is being captured at all → don't even wrap the writer.
		if !s.logDebugOn.Load() && !s.logAlwaysErr.Load() {
			next.ServeHTTP(w, r)
			return
		}
		lw := &logResponseWriter{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(lw, r)
		if lw.status == 0 {
			lw.status = http.StatusOK
		}
		if !s.shouldLogRequest(r.URL.Path, lw.status) {
			return
		}
		kind := "REQUEST"
		if lw.status >= 400 {
			kind = "ERROR"
		}
		// Requests have no source file:line, so that column stays blank.
		s.appendLog(kind, "", formatRequestMsg(r, lw, time.Since(start)))
	})
}

// formatRequestMsg builds the message column for a request row: method, path+query,
// status, duration, client, bytes, and (when present) the user-agent and referer.
func formatRequestMsg(r *http.Request, lw *logResponseWriter, dur time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s status=%d dur=%s remote=%s bytes=%d",
		r.Method, r.URL.RequestURI(), lw.status, dur.Round(time.Microsecond), r.RemoteAddr, lw.bytes)
	if ua := r.UserAgent(); ua != "" {
		fmt.Fprintf(&b, " ua=%q", ua)
	}
	if ref := r.Referer(); ref != "" {
		fmt.Fprintf(&b, " ref=%q", ref)
	}
	return b.String()
}

// ---- HTTP handlers (Logs page) --------------------------------------------

const logsPageSize = 100

// handleLogsList returns a page of log files (newest first) plus folder totals and the
// current logging status, for the Logs page and the Settings card.
func (s *server) handleLogsList(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	s.logMu.Lock()
	total := len(s.logIndex)
	totalBytes := s.logTotal
	// Copy names newest-first (logIndex is oldest-first).
	all := make([]logEntry, total)
	for i, e := range s.logIndex {
		all[total-1-i] = e
	}
	s.logMu.Unlock()

	start := (page - 1) * logsPageSize
	if start > len(all) {
		start = len(all)
	}
	end := start + logsPageSize
	if end > len(all) {
		end = len(all)
	}
	files := make([]map[string]any, 0, end-start)
	for _, e := range all[start:end] {
		files = append(files, map[string]any{"name": e.name, "size": e.size, "time": logNameTime(e.name)})
	}

	s.mu.Lock()
	resp := map[string]any{
		"dir":                 s.logDir(),
		"totalFiles":          total,
		"totalBytes":          totalBytes,
		"maxBytes":            s.logMaxBytes.Load(),
		"page":                page,
		"pageSize":            logsPageSize,
		"pages":               (total + logsPageSize - 1) / logsPageSize,
		"files":               files,
		"enabled":             s.settings.LoggingEnabled,
		"active":              s.settings.LoggingEnabled && !s.logExpired(),
		"level":               s.settings.LogLevel,
		"alwaysErrors":        s.settings.LogAlwaysErrors,
		"autoOffMinutes":      s.settings.LogAutoOffMinutes,
		"logRemainingSeconds": s.logRemainingSeconds(),
	}
	s.mu.Unlock()
	writeJSON(w, resp)
}

// logNameTime pulls the date back out of a "2006-01-02.log" day-file name.
func logNameTime(name string) string {
	if len(name) < 10 {
		return ""
	}
	if _, err := time.Parse("2006-01-02", name[:10]); err != nil {
		return ""
	}
	return name[:10]
}

// handleLogFile downloads a single log file as plain text (validated against the index).
func (s *server) handleLogFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if !validLogName(name) {
		http.Error(w, "bad name", 400)
		return
	}
	path := filepath.Join(s.logDir(), name)
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.Write(data)
}

// handleLogsZip bundles selected files (or all of them) into a single zip download.
func (s *server) handleLogsZip(w http.ResponseWriter, r *http.Request) {
	var body struct {
		All   bool     `json:"all"`
		Names []string `json:"names"`
	}
	if decode(r, &body) != nil {
		http.Error(w, "bad body", 400)
		return
	}
	var names []string
	if body.All {
		s.logMu.Lock()
		for _, e := range s.logIndex {
			names = append(names, e.name)
		}
		s.logMu.Unlock()
	} else {
		for _, n := range body.Names {
			if validLogName(n) {
				names = append(names, n)
			}
		}
	}
	if len(names) == 0 {
		http.Error(w, "no logs selected", 400)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"photo-judge-logs.zip\"")
	zw := zip.NewWriter(w)
	defer zw.Close()
	dir := s.logDir()
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		f, err := zw.Create(n)
		if err != nil {
			continue
		}
		_, _ = f.Write(data)
	}
}

// handleLogsClear deletes every log file and resets the in-memory index. The active
// day file is closed first so Windows will actually let it be removed; the next write
// reopens a fresh file for today.
func (s *server) handleLogsClear(w http.ResponseWriter, r *http.Request) {
	s.logMu.Lock()
	if s.logCurFile != nil {
		s.logCurFile.Close()
		s.logCurFile = nil
	}
	s.logCurDay = ""
	dir := s.logDir()
	removed := 0
	for _, e := range s.logIndex {
		if os.Remove(filepath.Join(dir, e.name)) == nil {
			removed++
		}
	}
	// Also sweep any stray .log files not tracked in the index (belt and braces).
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	s.logIndex = s.logIndex[:0]
	s.logTotal = 0
	s.logMu.Unlock()
	log.Printf("debug logs cleared (%d files)", removed)
	writeJSON(w, map[string]any{"removed": removed, "totalFiles": 0, "totalBytes": 0})
}

// validLogName guards downloads: a log filename only, no path parts or traversal.
func validLogName(n string) bool {
	return safeName(n) && strings.HasSuffix(n, ".log")
}
