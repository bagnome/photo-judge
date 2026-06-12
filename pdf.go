// Minimal PDF generation for the per-session score sheet, using only the standard
// library so the app stays a single dependency-free exe. We emit a hand-built PDF
// that relies on the built-in Helvetica fonts (no font embedding needed) and a
// single-column table layout: one row per photo with blanks for the photographer
// name and a score.
package main

import (
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// ---- low-level PDF writer -------------------------------------------------

const (
	pdfPageW  = 612.0 // US Letter, in points (1/72")
	pdfPageH  = 792.0
	pdfMargin = 54.0 // 0.75"
)

// pdfWriter accumulates page content streams and renders them into a complete
// PDF document. y tracks the current vertical cursor on the active page, in PDF
// user space (origin bottom-left, y grows upward).
type pdfWriter struct {
	pages []*strings.Builder
	buf   *strings.Builder
	y     float64
}

func newPDF() *pdfWriter { return &pdfWriter{} }

// newPage starts a fresh page and resets the cursor to just below the top margin.
func (p *pdfWriter) newPage() {
	b := &strings.Builder{}
	p.pages = append(p.pages, b)
	p.buf = b
	p.y = pdfPageH - pdfMargin
}

// cp1252High maps the Unicode runes that WinAnsi (Windows-1252) places in the
// 0x80–0x9F range — smart quotes, dashes, ellipsis and the like — which is where
// they differ from Latin-1. Lets common typographic characters in filenames and
// the em-dash in our title render instead of becoming '?'.
var cp1252High = map[rune]byte{
	'€': 0x80, '‚': 0x82, 'ƒ': 0x83, '„': 0x84, '…': 0x85,
	'†': 0x86, '‡': 0x87, 'ˆ': 0x88, '‰': 0x89, 'Š': 0x8A,
	'‹': 0x8B, 'Œ': 0x8C, 'Ž': 0x8E, '‘': 0x91, '’': 0x92,
	'“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97,
	'˜': 0x98, '™': 0x99, 'š': 0x9A, '›': 0x9B, 'œ': 0x9C,
	'ž': 0x9E, 'Ÿ': 0x9F,
}

// pdfEsc escapes a string for a PDF literal and folds it into the font's WinAnsi
// encoding (Latin-1 in 0xA0–0xFF, plus the CP1252 high range); anything else
// becomes '?'.
func pdfEsc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '(':
			b.WriteString(`\(`)
		case r == ')':
			b.WriteString(`\)`)
		case r >= 32 && r < 127:
			b.WriteByte(byte(r))
		case r >= 0xA0 && r < 256:
			b.WriteByte(byte(r))
		default:
			if win, ok := cp1252High[r]; ok {
				b.WriteByte(win)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}

// text draws a single line of text with its baseline at (x, y). font is a
// resource name ("F1" Helvetica, "F2" Helvetica-Bold).
func (p *pdfWriter) text(x, y float64, font string, size float64, s string) {
	fmt.Fprintf(p.buf, "BT /%s %.2f Tf 0 g 1 0 0 1 %.2f %.2f Tm (%s) Tj ET\n", font, size, x, y, pdfEsc(s))
}

// hline strokes a horizontal line at height y from x1 to x2 in a gray shade.
func (p *pdfWriter) hline(x1, x2, y, gray, width float64) {
	fmt.Fprintf(p.buf, "%.2f w %.2f G %.2f %.2f m %.2f %.2f l S\n", width, gray, x1, y, x2, y)
}

// helvW holds Helvetica advance widths (per 1000 units) for ASCII 32..126, used
// to measure text so long photo names can be truncated to their column.
var helvW = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

func textWidth(s string, size float64) float64 {
	w := 0
	for _, r := range s {
		if r >= 32 && r < 127 {
			w += helvW[r-32]
		} else {
			w += 556 // reasonable default for anything outside the table
		}
	}
	return float64(w) / 1000 * size
}

// truncateToWidth shortens s with a trailing "..." until it fits maxW.
func truncateToWidth(s string, size, maxW float64) string {
	if textWidth(s, size) <= maxW {
		return s
	}
	ellW := textWidth("...", size)
	var b strings.Builder
	w := 0.0
	for _, r := range s {
		cw := textWidth(string(r), size)
		if w+cw+ellW > maxW {
			break
		}
		b.WriteRune(r)
		w += cw
	}
	return b.String() + "..."
}

// render assembles the accumulated pages into a complete PDF byte stream.
//
// Object layout: 1 catalog, 2 pages, 3 Helvetica, 4 Helvetica-Bold, then for each
// page a content-stream object (5,7,9,…) and a page object (6,8,10,…).
func (p *pdfWriter) render() []byte {
	if len(p.pages) == 0 {
		p.newPage()
	}
	nObj := 4 + 2*len(p.pages)
	obj := make([]string, nObj+1) // 1-indexed

	obj[1] = "<< /Type /Catalog /Pages 2 0 R >>"
	var kids strings.Builder
	for i := range p.pages {
		fmt.Fprintf(&kids, "%d 0 R ", 6+2*i)
	}
	obj[2] = fmt.Sprintf("<< /Type /Pages /Kids [ %s] /Count %d >>", kids.String(), len(p.pages))
	obj[3] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"
	obj[4] = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>"
	for i, pg := range p.pages {
		content := pg.String()
		obj[5+2*i] = fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
		obj[6+2*i] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] "+
			"/Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", pdfPageW, pdfPageH, 5+2*i)
	}

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, nObj+1)
	for n := 1; n <= nObj; n++ {
		offsets[n] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, obj[n])
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", nObj+1)
	out.WriteString("0000000000 65535 f \n")
	for n := 1; n <= nObj; n++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", nObj+1, xref)
	return out.Bytes()
}

// ---- score-sheet layout ---------------------------------------------------

// photoRow is one line of the score sheet: the displayed photo name (filename
// without extension), an optional operator-entered photographer name, and an
// optional judge's score.
type photoRow struct {
	name         string
	photographer string
	score        string
}

// photoRows gathers the rows for one category/orientation in display order,
// pairing each photo with its photographer name (names.json) and score (scores.json).
func (s *server) photoRows(sid, cat, orient string) []photoRow {
	dir := s.photosDir(sid, cat, orient)
	files := s.photoFiles(sid, cat, orient)
	names := loadNames(dir)
	scores := loadScores(dir)
	rows := make([]photoRow, len(files))
	for i, f := range files {
		rows[i] = photoRow{name: strings.TrimSuffix(f, filepath.Ext(f)), photographer: names[f], score: scores[f]}
	}
	return rows
}

// buildScoreSheetPDF renders a single-column scoring form for one session:
// a section per category (in the session's category order), Landscape before
// Portrait within each, one row per photo (in display order) with blanks for the
// photographer name and a score. Empty categories/orientations are omitted.
func (s *server) buildScoreSheetPDF(sess *Session) []byte {
	const rowH = 26.0
	left := pdfMargin
	right := pdfPageW - pdfMargin // 558
	nameX := left
	phX := 320.0
	scX := 500.0
	nameColW := phX - nameX - 12

	p := newPDF()
	p.newPage()

	// Title block.
	p.text(left, p.y-18, "F2", 18, "Photo Judge — Score Sheet")
	p.y -= 26
	p.text(left, p.y-11, "F1", 11, sess.Date+"   (Session #"+sess.ID+")")
	p.y -= 24

	phColW := scX - phX - 8 // room for a pre-filled photographer name before Score

	// orientation draws one Landscape/Portrait sub-table, paginating as needed.
	orientation := func(orient string, rows []photoRow) {
		header := func(suffix string) {
			p.text(nameX, p.y-11, "F2", 11, orient+suffix)
			p.y -= 16
			p.text(nameX, p.y-9, "F2", 9, "Photo")
			p.text(phX, p.y-9, "F2", 9, "Photographer")
			p.text(scX, p.y-9, "F2", 9, "Score")
			p.y -= 11
			p.hline(left, right, p.y+2, 0.3, 0.8)
			p.y -= 2
		}
		if p.y-(16+11+2+rowH) < pdfMargin {
			p.newPage()
		}
		header("")
		for _, row := range rows {
			if p.y-rowH < pdfMargin {
				p.newPage()
				header(" (continued)")
			}
			p.text(nameX, p.y-16, "F1", 10, truncateToWidth(row.name, 10, nameColW))
			if row.photographer != "" {
				p.text(phX, p.y-16, "F1", 10, truncateToWidth(row.photographer, 10, phColW))
			}
			if row.score != "" {
				p.text(scX, p.y-16, "F1", 10, truncateToWidth(row.score, 10, right-scX))
			}
			p.hline(left, right, p.y-rowH+3, 0.78, 0.5)
			p.y -= rowH
		}
		p.y -= 6
	}

	any := false
	for _, cat := range sess.Categories {
		land := s.photoRows(sess.ID, cat, "Landscape")
		port := s.photoRows(sess.ID, cat, "Portrait")
		if len(land) == 0 && len(port) == 0 {
			continue
		}
		any = true
		// Keep the category heading with at least the start of its first sub-table.
		if p.y-90 < pdfMargin {
			p.newPage()
		}
		p.y -= 6
		p.text(nameX, p.y-13, "F2", 14, cat)
		p.y -= 18
		p.hline(left, right, p.y+4, 0.4, 1.0)
		p.y -= 4
		if len(land) > 0 {
			orientation("Landscape", land)
		}
		if len(port) > 0 {
			orientation("Portrait", port)
		}
		p.y -= 6
	}
	if !any {
		p.text(left, p.y-12, "F1", 12, "No photos have been uploaded for this session yet.")
	}
	return p.render()
}

// handleSessionPDF streams the score sheet for ?session=<id> as a PDF download.
func (s *server) handleSessionPDF(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("session")
	if !safeName(sid) {
		http.Error(w, "bad session", 400)
		return
	}
	s.mu.Lock()
	sess := s.sessionByID(sid)
	s.mu.Unlock()
	if sess == nil {
		http.Error(w, "no such session", 404)
		return
	}
	pdf := s.buildScoreSheetPDF(sess)
	name := fmt.Sprintf("score-sheet-session-%s.pdf", sess.ID)
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, name))
	// ServeContent sets Content-Length and handles Range/HEAD requests, so the
	// response is a sized download rather than a chunked stream — browsers fail
	// (open-ended "network error") on chunked localhost downloads.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(pdf))
}
