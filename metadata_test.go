package main

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

// --- helpers: craft images carrying metadata -------------------------------

func appendPNGChunk(dst []byte, typ string, data []byte) []byte {
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(data)))
	body := append([]byte(typ), data...)
	var cb [4]byte
	binary.BigEndian.PutUint32(cb[:], crc32.ChecksumIEEE(body))
	dst = append(dst, lb[:]...)
	dst = append(dst, body...)
	return append(dst, cb[:]...)
}

// pngWithText encodes a w×h PNG and splices tEXt chunks in before IEND.
func pngWithText(t *testing.T, w, h int, kv map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	out := append([]byte{}, data[:len(data)-12]...) // everything except trailing IEND
	for k, v := range kv {
		out = appendPNGChunk(out, "tEXt", append(append([]byte(k), 0), []byte(v)...))
	}
	return append(out, data[len(data)-12:]...) // IEND back on the end
}

// exifAPP1 builds a little-endian APP1 "Exif" segment with the given ASCII tags.
func exifAPP1(asciiTags map[uint16]string) []byte {
	var tags []uint16
	for k := range asciiTags {
		tags = append(tags, k)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i] < tags[j] })
	n := len(tags)
	dataOffset := 8 + 2 + 12*n + 4 // tiff-relative start of overflow string data

	var tiff bytes.Buffer
	tiff.WriteString("II")
	binary.Write(&tiff, binary.LittleEndian, uint16(0x2A))
	binary.Write(&tiff, binary.LittleEndian, uint32(8)) // IFD0 offset
	binary.Write(&tiff, binary.LittleEndian, uint16(n))
	var strData bytes.Buffer
	for _, tag := range tags {
		s := asciiTags[tag] + "\x00"
		cnt := len(s)
		binary.Write(&tiff, binary.LittleEndian, tag)
		binary.Write(&tiff, binary.LittleEndian, uint16(2)) // ASCII
		binary.Write(&tiff, binary.LittleEndian, uint32(cnt))
		if cnt <= 4 {
			b := make([]byte, 4)
			copy(b, s)
			tiff.Write(b)
		} else {
			binary.Write(&tiff, binary.LittleEndian, uint32(dataOffset+strData.Len()))
			strData.WriteString(s)
		}
	}
	binary.Write(&tiff, binary.LittleEndian, uint32(0)) // no next IFD
	tiff.Write(strData.Bytes())
	return append([]byte("Exif\x00\x00"), tiff.Bytes()...)
}

func jpegWithExif(seg []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1}) // SOI, APP1
	segLen := len(seg) + 2
	b.Write([]byte{byte(segLen >> 8), byte(segLen)})
	b.Write(seg)
	b.Write([]byte{0xFF, 0xD9}) // EOI
	return b.Bytes()
}

// --- tests -----------------------------------------------------------------

func TestSanitizeUploadName(t *testing.T) {
	cases := []struct{ in, ext, want string }{
		{"Sunset Over Hills", ".jpg", "Sunset Over Hills.jpg"},
		{`a/b\c:d*e?f"g<h>i|j`, ".png", "abcdefghij.png"},
		{"  ..hidden..  ", ".jpg", "hidden.jpg"},
		{"   ", ".jpg", ""},
		{"", ".png", ""},
		{"two   spaces", ".jpg", "two spaces.jpg"},
	}
	for _, c := range cases {
		if got := sanitizeUploadName(c.in, c.ext); got != c.want {
			t.Errorf("sanitizeUploadName(%q): got %q want %q", c.in, got, c.want)
		}
	}
	if got := sanitizeUploadName(strings.Repeat("x", 200), ".jpg"); len(got) > 124 {
		t.Errorf("long title not truncated: len=%d", len(got))
	}
}

func TestPNGTextExtraction(t *testing.T) {
	b := pngWithText(t, 4, 2, map[string]string{"Title": "Morning Light", "Author": "Vivian Maier"})
	m := pngText(b)
	if m["title"] != "Morning Light" {
		t.Errorf("title: got %q", m["title"])
	}
	if m["author"] != "Vivian Maier" {
		t.Errorf("author: got %q", m["author"])
	}
}

func TestExifStringExtraction(t *testing.T) {
	seg := exifAPP1(map[uint16]string{tagArtist: "Ansel Adams", tagImageDescription: "Mountain Vista"})
	m := exifStrings(seg)
	if m[tagArtist] != "Ansel Adams" || m[tagImageDescription] != "Mountain Vista" {
		t.Fatalf("exifStrings: %v", m)
	}
	// And through the full JPEG marker walk.
	m2 := jpegExifStrings(jpegWithExif(seg))
	if m2[tagArtist] != "Ansel Adams" || m2[tagImageDescription] != "Mountain Vista" {
		t.Fatalf("jpegExifStrings: %v", m2)
	}
	// The orientation reader must still work on the same segment-free JPEG.
	if o := exifOrientation(jpegWithExif(seg)); o != 0 {
		t.Errorf("unexpected orientation %d (no orientation tag set)", o)
	}
}

func TestDecodeUTF16LE(t *testing.T) {
	// "Hi" in UTF-16LE with a trailing NUL terminator.
	b := []byte{'H', 0, 'i', 0, 0, 0}
	if got := decodeUTF16LE(b); got != "Hi" {
		t.Errorf("decodeUTF16LE: got %q", got)
	}
}

func uploadFile(t *testing.T, s *server, sid, cat, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("session", sid)
	mw.WriteField("category", cat)
	fw, err := mw.CreateFormFile("files", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.handleUpload(rr, req)
	return rr
}

func TestUploadImportsMetadata(t *testing.T) {
	s := newTestServer(t)
	s.importMetadata = true
	ss, err := s.createSession("2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	img := pngWithText(t, 4, 2, map[string]string{"Title": "Sunset Over Hills", "Author": "Jane Doe"})
	if rr := uploadFile(t, s, ss.ID, cat, "DSC_0001.png", img); rr.Code != 200 {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	land := s.photoFiles(ss.ID, cat, "Landscape")
	if len(land) != 1 || land[0] != "Sunset Over Hills.png" {
		t.Fatalf("expected file renamed to the title, got %v", land)
	}
	names := loadNames(s.photosDir(ss.ID, cat, "Landscape"))
	if names["Sunset Over Hills.png"] != "Jane Doe" {
		t.Errorf("photographer not imported: %v", names)
	}
}

func TestUploadIgnoresMetadataWhenOff(t *testing.T) {
	s := newTestServer(t)
	s.importMetadata = false
	ss, err := s.createSession("2026-06-01")
	if err != nil {
		t.Fatal(err)
	}
	cat := ss.Categories[0]
	img := pngWithText(t, 4, 2, map[string]string{"Title": "Sunset Over Hills", "Author": "Jane Doe"})
	if rr := uploadFile(t, s, ss.ID, cat, "DSC_0001.png", img); rr.Code != 200 {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	land := s.photoFiles(ss.ID, cat, "Landscape")
	if len(land) != 1 || land[0] != "DSC_0001.png" {
		t.Fatalf("filename should be unchanged when import is off, got %v", land)
	}
	if names := loadNames(s.photosDir(ss.ID, cat, "Landscape")); len(names) != 0 {
		t.Errorf("photographer should not be set when import is off: %v", names)
	}
}
