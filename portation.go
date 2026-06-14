// Session import/export. Whole sessions — their photos, sidecar metadata, physical
// prints, and (for past nights) their archive record — can be bundled into portable
// files and carried to another computer. The files are ordinary zip archives (stdlib
// archive/zip) with a custom extension:
//
//	.pjs   "Photo Judge Session"  — exactly one session
//	.pjss  "Photo Judge Sessions" — more than one
//
// Internal layout (identical for both; the extension only reflects the count):
//
//	manifest.json                 {format, exportedAt, appVersion, sessions:[entry...]}
//	sessions/<origId>/...         live: the whole photos/<id>/ subtree (session.json,
//	                              category/orientation folders, order/names/scores.json,
//	                              physical.json); archived: a single archive.json
//
// On import the original session IDs are never adopted — each imported session gets a
// fresh ID from nextID() so they can never collide with what's already here. Standard
// library only, like the rest of the app.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// portManifest is the index written at the root of every .pjs/.pjss file.
type portManifest struct {
	Format     int         `json:"format"` // bundle format version (currently 1)
	ExportedAt string      `json:"exportedAt"`
	AppVersion string      `json:"appVersion"`
	Sessions   []portEntry `json:"sessions"`
}

// portEntry describes one session inside a bundle — enough to render the import
// preview table without unzipping any media.
type portEntry struct {
	OrigID      string `json:"origId"`
	Kind        string `json:"kind"` // "live" | "archived"
	Date        string `json:"date"`
	Description string `json:"description,omitempty"`
	Created     string `json:"created,omitempty"`
	Archived    bool   `json:"archived"`
}

const portFormat = 1

func (s *server) importTmpDir() string { return filepath.Join(s.baseDir, "_import_tmp") }

// sweepImportTmp clears any leftover import staging from a previous run. Called once
// at startup; each import also removes its own token dir when it finishes.
func (s *server) sweepImportTmp() { _ = os.RemoveAll(s.importTmpDir()) }

func newToken() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ---- unified session list (export source + general index) -----------------

// sessionRow is one line in the export picker: works for both live and archived
// sessions so they can be listed and filtered together.
type sessionRow struct {
	ID          string `json:"id"`
	Date        string `json:"date"`
	Description string `json:"description"`
	Created     string `json:"created"`
	Archived    bool   `json:"archived"`
	PhotoCount  int    `json:"photoCount"`
}

// handleSessionsAll lists every session (live + archived) for the export picker,
// honoring optional filters and pagination. Query params: archived=all|yes|no,
// photographer, title (substring, any photo/print), from, to (session date,
// inclusive), sort=date|created|id, dir=asc|desc, page (1-based), pageSize.
func (s *server) handleSessionsAll(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	archived := strings.ToLower(strings.TrimSpace(q.Get("archived")))
	photog := strings.ToLower(strings.TrimSpace(q.Get("photographer")))
	title := strings.ToLower(strings.TrimSpace(q.Get("title")))
	from := strings.TrimSpace(q.Get("from"))
	to := strings.TrimSpace(q.Get("to"))

	// Build a uniform ArchivedSession view for every session so live and archived
	// rows filter through exactly the same matchers (matchesPhotographer/Title).
	type item struct {
		a        ArchivedSession
		archived bool
	}
	var items []item
	s.mu.Lock()
	for _, ss := range s.sessions {
		items = append(items, item{s.buildArchive(ss), false})
	}
	s.mu.Unlock()
	for _, a := range s.loadArchives() {
		items = append(items, item{a, true})
	}

	var rows []sessionRow
	for _, it := range items {
		if archived == "yes" && !it.archived {
			continue
		}
		if archived == "no" && it.archived {
			continue
		}
		if from != "" && it.a.Date < from {
			continue
		}
		if to != "" && it.a.Date > to {
			continue
		}
		if photog != "" && !matchesPhotographer(it.a, photog) {
			continue
		}
		if title != "" && !matchesTitle(it.a, title) {
			continue
		}
		rows = append(rows, sessionRow{
			ID:          it.a.SessionID,
			Date:        it.a.Date,
			Description: it.a.Description,
			Created:     it.a.Created,
			Archived:    it.archived,
			PhotoCount:  it.a.PhotoCount,
		})
	}

	// Sort.
	sortKey := strings.ToLower(strings.TrimSpace(q.Get("sort")))
	less := func(i, j int) bool { // default: session date
		if rows[i].Date != rows[j].Date {
			return rows[i].Date < rows[j].Date
		}
		return rows[i].ID < rows[j].ID
	}
	switch sortKey {
	case "created":
		less = func(i, j int) bool {
			if rows[i].Created != rows[j].Created {
				return rows[i].Created < rows[j].Created
			}
			return rows[i].ID < rows[j].ID
		}
	case "id":
		less = func(i, j int) bool { return rows[i].ID < rows[j].ID }
	}
	sort.SliceStable(rows, less)
	if strings.ToLower(strings.TrimSpace(q.Get("dir"))) == "desc" {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}

	// Paginate.
	pageSize := s.exportPageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if n := atoiDefault(q.Get("pageSize"), 0); n >= 1 && n <= 500 {
		pageSize = n
	}
	total := len(rows)
	page := atoiDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageRows := rows[start:end]
	if pageRows == nil {
		pageRows = []sessionRow{}
	}
	writeJSON(w, map[string]any{"sessions": pageRows, "total": total, "page": page, "pageSize": pageSize})
}

func atoiDefault(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// ---- export ---------------------------------------------------------------

// handleExport streams a .pjs/.pjss bundle of the requested session IDs. Accepts
// GET (?ids=001,002) so a plain link downloads it, or POST {"ids":[...]}.
func (s *server) handleExport(w http.ResponseWriter, r *http.Request) {
	var ids []string
	if r.Method == http.MethodPost {
		var body struct {
			IDs []string `json:"ids"`
		}
		if decode(r, &body) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		ids = body.IDs
	} else {
		for _, p := range strings.Split(r.URL.Query().Get("ids"), ",") {
			if p = strings.TrimSpace(p); p != "" {
				ids = append(ids, p)
			}
		}
	}
	var clean []string
	for _, id := range ids {
		if safeName(id) {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		http.Error(w, "no sessions selected", 400)
		return
	}

	s.mu.Lock()
	buf, entries, err := s.buildExportZip(clean)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, "could not build export: "+err.Error(), 500)
		return
	}
	if len(entries) == 0 {
		http.Error(w, "none of the requested sessions exist", 404)
		return
	}

	var name string
	if len(entries) == 1 {
		name = fmt.Sprintf("session-%s-%s.pjs", entries[0].OrigID, sanitizeFilePart(entries[0].Date))
	} else {
		name = fmt.Sprintf("photo-judge-sessions-%s.pjss", todayStr())
	}
	log.Printf("exported %d session(s) as %s", len(entries), name)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buf.Len()))
	w.Write(buf.Bytes())
}

// buildExportZip builds the bundle in memory and returns it with the manifest
// entries actually written (skipping any unknown IDs). Caller holds s.mu.
func (s *server) buildExportZip(ids []string) (*bytes.Buffer, []portEntry, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	man := portManifest{Format: portFormat, ExportedAt: time.Now().Format(time.RFC3339), AppVersion: appVersion}

	for _, id := range ids {
		if ss := s.sessionByID(id); ss != nil {
			man.Sessions = append(man.Sessions, portEntry{OrigID: id, Kind: "live", Date: ss.Date, Description: ss.Description, Created: ss.Created, Archived: false})
			if err := s.addLiveSessionToZip(zw, id); err != nil {
				return nil, nil, err
			}
		} else if arch, ok := s.loadArchive(id); ok {
			man.Sessions = append(man.Sessions, portEntry{OrigID: id, Kind: "archived", Date: arch.Date, Description: arch.Description, Created: arch.Created, Archived: true})
			data, _ := json.MarshalIndent(arch, "", "  ")
			if err := writeZipFile(zw, "sessions/"+id+"/archive.json", data); err != nil {
				return nil, nil, err
			}
		}
	}
	data, _ := json.MarshalIndent(man, "", "  ")
	if err := writeZipFile(zw, "manifest.json", data); err != nil {
		return nil, nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, nil, err
	}
	return buf, man.Sessions, nil
}

// addLiveSessionToZip copies the whole photos/<id>/ subtree into sessions/<id>/.
func (s *server) addLiveSessionToZip(zw *zip.Writer, id string) error {
	root := filepath.Join(s.baseDir, "photos", id)
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return writeZipFile(zw, "sessions/"+id+"/"+filepath.ToSlash(rel), data)
	})
}

func writeZipFile(zw *zip.Writer, name string, data []byte) error {
	f, err := zw.Create(name) // Create uses Deflate compression by default
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}

// ---- import: preview ------------------------------------------------------

// previewEntry is one row of the import preview table.
type previewEntry struct {
	File        string `json:"file"`     // on-disk staging name (e.g. "0.zip"), used at commit
	FileName    string `json:"fileName"` // original upload name, for display
	OrigID      string `json:"origId"`
	Kind        string `json:"kind"`
	Date        string `json:"date"`
	Description string `json:"description,omitempty"`
	Archived    bool   `json:"archived"`
}

// handleImportPreview stages one or more uploaded bundles and returns the sessions
// they contain. The files are kept under _import_tmp/<token>/ for the commit step so
// the (possibly large) media only has to upload once.
func (s *server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "could not read upload: "+err.Error(), 400)
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "no files uploaded", 400)
		return
	}
	token, err := newToken()
	if err != nil {
		http.Error(w, "could not start import", 500)
		return
	}
	dir := filepath.Join(s.importTmpDir(), token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "could not stage import: "+err.Error(), 500)
		return
	}

	var sessions []previewEntry
	var errs []string
	for i, fh := range files {
		diskName := fmt.Sprintf("%d.zip", i)
		if err := saveUpload(fh, filepath.Join(dir, diskName)); err != nil {
			errs = append(errs, fh.Filename+": could not save")
			continue
		}
		zr, err := zip.OpenReader(filepath.Join(dir, diskName))
		if err != nil {
			errs = append(errs, fh.Filename+": not a valid Photo Judge file")
			continue
		}
		man, err := readManifest(&zr.Reader)
		if err != nil {
			zr.Close()
			errs = append(errs, fh.Filename+": missing or unreadable manifest")
			continue
		}
		for _, e := range man.Sessions {
			sessions = append(sessions, previewEntry{
				File: diskName, FileName: fh.Filename, OrigID: e.OrigID, Kind: e.Kind,
				Date: e.Date, Description: e.Description, Archived: e.Archived,
			})
		}
		zr.Close()
	}

	if len(sessions) == 0 {
		_ = os.RemoveAll(dir)
		msg := "No sessions found in the selected file(s)."
		if len(errs) > 0 {
			msg += " " + strings.Join(errs, "; ")
		}
		http.Error(w, msg, 400)
		return
	}
	writeJSON(w, map[string]any{"token": token, "sessions": sessions, "errors": errs})
}

func saveUpload(fh *multipart.FileHeader, dst string) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}

// ---- import: commit -------------------------------------------------------

// handleImportCommit imports the selected sessions from a previously-previewed
// upload, assigning each a fresh ID, and returns the old→new ID mapping.
func (s *server) handleImportCommit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token  string `json:"token"`
		Select []struct {
			File   string `json:"file"`
			OrigID string `json:"origId"`
		} `json:"select"`
	}
	if decode(r, &body) != nil || !safeName(body.Token) {
		http.Error(w, "bad request", 400)
		return
	}
	dir := filepath.Join(s.importTmpDir(), body.Token)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		http.Error(w, "This import has expired — please choose the file(s) again.", 400)
		return
	}
	if len(body.Select) == 0 {
		http.Error(w, "no sessions selected to import", 400)
		return
	}

	var mapping []map[string]any
	s.mu.Lock()
	for _, sel := range body.Select {
		if !safeName(sel.File) || !safeName(sel.OrigID) {
			continue
		}
		zr, err := zip.OpenReader(filepath.Join(dir, sel.File))
		if err != nil {
			continue
		}
		man, _ := readManifest(&zr.Reader)
		var ent portEntry
		for _, e := range man.Sessions {
			if e.OrigID == sel.OrigID {
				ent = e
				break
			}
		}
		if ent.OrigID == "" {
			zr.Close()
			continue
		}
		newID := s.nextID() // each extract reserves its dir/file, so this advances per import
		var perr error
		if ent.Kind == "archived" {
			perr = s.importArchivedSession(&zr.Reader, sel.OrigID, newID)
		} else {
			perr = s.importLiveSession(&zr.Reader, sel.OrigID, newID)
		}
		zr.Close()
		if perr != nil {
			log.Printf("import: session %s failed: %v", sel.OrigID, perr)
			continue
		}
		mapping = append(mapping, map[string]any{
			"oldId": sel.OrigID, "newId": newID, "date": ent.Date,
			"description": ent.Description, "archived": ent.Archived,
		})
	}
	s.scanSessions()
	s.mu.Unlock()

	_ = os.RemoveAll(dir)
	s.pushConsole()
	log.Printf("imported %d session(s)", len(mapping))
	writeJSON(w, map[string]any{"imported": mapping})
}

// importLiveSession writes a bundle's sessions/<origID>/ subtree out to a new
// photos/<newID>/ folder, rewriting session.json's id. Caller holds s.mu.
func (s *server) importLiveSession(zr *zip.Reader, origID, newID string) error {
	prefix := "sessions/" + origID + "/"
	dest := filepath.Join(s.baseDir, "photos", newID)
	if err := os.MkdirAll(dest, 0o755); err != nil { // reserves the ID for nextID()
		return err
	}
	for _, f := range zr.File {
		if !strings.HasPrefix(f.Name, prefix) || strings.HasSuffix(f.Name, "/") {
			continue
		}
		rel := strings.TrimPrefix(f.Name, prefix)
		out, ok := safeJoin(dest, rel)
		if !ok {
			continue
		}
		data, err := readZipFile(f)
		if err != nil {
			return err
		}
		if rel == "session.json" {
			data = rewriteSessionID(data, newID)
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// importArchivedSession writes a bundle's archive.json out to archives/<newID>.json,
// rewriting its sessionId. Caller holds s.mu.
func (s *server) importArchivedSession(zr *zip.Reader, origID, newID string) error {
	name := "sessions/" + origID + "/archive.json"
	var f *zip.File
	for _, zf := range zr.File {
		if zf.Name == name {
			f = zf
			break
		}
	}
	if f == nil {
		return fmt.Errorf("archive.json not found for %s", origID)
	}
	data, err := readZipFile(f)
	if err != nil {
		return err
	}
	var a ArchivedSession
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	a.SessionID = newID
	if err := os.MkdirAll(s.archivesDir(), 0o755); err != nil {
		return err
	}
	out, _ := json.MarshalIndent(a, "", "  ")
	return os.WriteFile(filepath.Join(s.archivesDir(), newID+".json"), out, 0o644) // reserves the ID
}

// ---- shared helpers -------------------------------------------------------

func readManifest(zr *zip.Reader) (portManifest, error) {
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			data, err := readZipFile(f)
			if err != nil {
				return portManifest{}, err
			}
			var m portManifest
			if err := json.Unmarshal(data, &m); err != nil {
				return portManifest{}, err
			}
			return m, nil
		}
	}
	return portManifest{}, fmt.Errorf("manifest.json not found")
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func rewriteSessionID(data []byte, newID string) []byte {
	var ss Session
	if json.Unmarshal(data, &ss) == nil {
		ss.ID = newID
		if b, err := json.MarshalIndent(&ss, "", "  "); err == nil {
			return b
		}
	}
	return data
}

// safeJoin resolves rel under base, refusing anything that would escape it
// (zip-slip protection). Returns the cleaned absolute-ish path and ok.
func safeJoin(base, rel string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
		return "", false
	}
	base = filepath.Clean(base)
	out := filepath.Join(base, clean)
	if out != base && !strings.HasPrefix(out, base+string(os.PathSeparator)) {
		return "", false
	}
	return out, true
}

// sanitizeFilePart keeps a value safe for a download filename (letters, digits,
// dash, underscore, dot); everything else becomes a dash.
func sanitizeFilePart(s string) string {
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}
