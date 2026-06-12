// Optional extraction of a photo's title and photographer from image metadata on
// upload (toggled by importMetadata in photo-judge.properties). Standard library
// only — JPEG EXIF tags and PNG text chunks are parsed by hand, reusing the same
// TIFF/IFD walking the orientation reader uses.
package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// EXIF (IFD0) tags we look at.
const (
	tagImageDescription = 0x010E // ASCII
	tagArtist           = 0x013B // ASCII
	tagXPTitle          = 0x9C9B // UTF-16LE (Windows)
	tagXPAuthor         = 0x9C9D // UTF-16LE (Windows)
)

// imageMetadata reads the embedded title and photographer for an uploaded image,
// returning "" for anything absent. It always leaves f rewound to the start.
func imageMetadata(f io.ReadSeeker, name string) (title, photographer string) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", ""
	}
	defer f.Seek(0, io.SeekStart)

	const headMax = 256 * 1024 // metadata lives near the file start
	head := make([]byte, headMax)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		m := jpegExifStrings(head)
		photographer = firstNonEmpty(m[tagXPAuthor], m[tagArtist])
		title = firstNonEmpty(m[tagXPTitle], m[tagImageDescription])
	case ".png":
		t := pngText(head)
		photographer = firstNonEmpty(t["author"], t["artist"], t["creator"])
		title = firstNonEmpty(t["title"], t["description"])
	}
	return strings.TrimSpace(title), strings.TrimSpace(photographer)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// --- JPEG EXIF -------------------------------------------------------------

// jpegExifStrings walks a JPEG's markers, finds the APP1/Exif segment, and returns
// its IFD0 string tags (ASCII and the UTF-16 XP tags), keyed by tag id.
func jpegExifStrings(b []byte) map[int]string {
	out := map[int]string{}
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 { // SOI
		return out
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return out
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 { // SOS / EOI — image data starts
			return out
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) { // standalone, no length
			i += 2
			continue
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		segStart, segEnd := i+4, i+2+segLen
		if segLen < 2 || segEnd > len(b) {
			return out
		}
		if marker == 0xE1 { // APP1
			if m := exifStrings(b[segStart:segEnd]); len(m) > 0 {
				return m
			}
		}
		i = segEnd
	}
	return out
}

// exifStrings extracts the ASCII / XP-UTF16 string tags from an APP1 "Exif" segment.
func exifStrings(seg []byte) map[int]string {
	out := map[int]string{}
	if len(seg) < 14 || string(seg[0:6]) != "Exif\x00\x00" {
		return out
	}
	tiff := seg[6:]
	var bo binary.ByteOrder
	switch string(tiff[0:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return out
	}
	if len(tiff) < 8 || bo.Uint16(tiff[2:4]) != 0x2A {
		return out
	}
	ifd := int(bo.Uint32(tiff[4:8]))
	if ifd < 8 || ifd+2 > len(tiff) {
		return out
	}
	count := int(bo.Uint16(tiff[ifd : ifd+2]))
	for k, p := 0, ifd+2; k < count; k, p = k+1, p+12 {
		if p+12 > len(tiff) {
			break
		}
		tag := int(bo.Uint16(tiff[p : p+2]))
		typ := bo.Uint16(tiff[p+2 : p+4])
		cnt := int(bo.Uint32(tiff[p+4 : p+8]))
		if (typ != 1 && typ != 2) || cnt <= 0 || cnt > 1<<16 { // BYTE / ASCII only
			continue
		}
		var raw []byte
		if cnt <= 4 {
			raw = tiff[p+8 : p+8+cnt]
		} else {
			off := int(bo.Uint32(tiff[p+8 : p+12]))
			if off < 0 || off+cnt > len(tiff) {
				continue
			}
			raw = tiff[off : off+cnt]
		}
		var val string
		switch tag {
		case tagXPTitle, tagXPAuthor:
			val = decodeUTF16LE(raw)
		default:
			val = strings.TrimRight(string(raw), "\x00")
		}
		if val = strings.TrimSpace(val); val != "" {
			out[tag] = val
		}
	}
	return out
}

func decodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	b = b[:len(b)&^1] // even length
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u))
}

// --- PNG text chunks -------------------------------------------------------

// pngText returns the tEXt/zTXt/iTXt key/value pairs near the start of a PNG, with
// keys lower-cased. Stops at the first IDAT/IEND.
func pngText(b []byte) map[string]string {
	out := map[string]string{}
	if len(b) < 8 || string(b[0:8]) != "\x89PNG\r\n\x1a\n" {
		return out
	}
	for i := 8; i+8 <= len(b); {
		length := int(binary.BigEndian.Uint32(b[i : i+4]))
		typ := string(b[i+4 : i+8])
		start := i + 8
		end := start + length
		if length < 0 || end+4 > len(b) {
			break // truncated (head buffer ended) or corrupt
		}
		data := b[start:end]
		switch typ {
		case "IEND":
			return out
		// IDAT and any other chunk are skipped (text can appear before or after the
		// image data); we just advance past them by their declared length.
		case "tEXt":
			if kw, v, ok := cut0(data); ok {
				out[strings.ToLower(kw)] = latin1(v)
			}
		case "zTXt":
			if kw, rest, ok := cut0(data); ok && len(rest) >= 1 {
				if dec, err := zlibInflate(rest[1:]); err == nil { // rest[0] = method
					out[strings.ToLower(kw)] = latin1(dec)
				}
			}
		case "iTXt":
			if kw, rest, ok := cut0(data); ok && len(rest) >= 2 {
				compressed := rest[0] == 1
				r := rest[2:] // skip compression flag + method
				if _, after, ok := cut0(r); ok { // skip language tag
					if _, txt, ok := cut0(after); ok { // skip translated keyword
						if compressed {
							if dec, err := zlibInflate(txt); err == nil {
								out[strings.ToLower(kw)] = string(dec)
							}
						} else {
							out[strings.ToLower(kw)] = string(txt)
						}
					}
				}
			}
		}
		i = end + 4 // skip CRC
	}
	return out
}

// cut0 splits b at the first NUL into (before, after, found).
func cut0(b []byte) (string, []byte, bool) {
	j := bytes.IndexByte(b, 0)
	if j < 0 {
		return "", nil, false
	}
	return string(b[:j]), b[j+1:], true
}

// latin1 decodes Latin-1 bytes (PNG tEXt/zTXt) to a Go UTF-8 string.
func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

func zlibInflate(b []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(io.LimitReader(zr, 1<<20)) // cap at 1 MB
}

// --- filename from a title -------------------------------------------------

// sanitizeUploadName turns a metadata title into a safe upload filename with ext,
// or "" if nothing usable remains (so the caller keeps the original name).
func sanitizeUploadName(title, ext string) string {
	s := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return -1
		}
		if r < 32 {
			return -1
		}
		return r
	}, title)
	s = strings.Join(strings.Fields(s), " ") // collapse whitespace
	for strings.Contains(s, "..") {           // never allow path-traversal dots
		s = strings.ReplaceAll(s, "..", ".")
	}
	s = strings.Trim(s, " .")
	if len(s) > 120 {
		s = strings.TrimSpace(s[:120])
	}
	if s == "" {
		return ""
	}
	return s + ext
}
