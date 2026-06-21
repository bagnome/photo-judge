// Writing a photo's title and photographer back into a JPEG's EXIF metadata, so a
// submitted entry carries its title (ImageDescription) and author (Artist) inside the
// file itself. This is the write side of metadata.go's reader and round-trips with it:
// IFD0 ASCII tags in a big-endian ("MM") TIFF block inside an APP1 "Exif" segment.
// Standard library only.
package main

import (
	"bytes"
	"encoding/binary"
)

// jpegSetTitleArtist returns the JPEG with its title (ImageDescription) and photographer
// (Artist) set in EXIF. Any existing APP1/Exif segment is replaced. On anything unexpected
// (not a JPEG, nothing to write, oversized segment) it returns the input unchanged.
func jpegSetTitleArtist(data []byte, title, artist string) []byte {
	if len(data) < 2 || data[0] != 0xFF || data[1] != 0xD8 { // SOI
		return data
	}
	if title == "" && artist == "" {
		return data
	}
	tiff := buildExifTIFF(title, artist)
	if len(tiff) == 0 {
		return data
	}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	if len(payload)+2 > 0xFFFF { // APP1 length field is 16-bit
		return data
	}
	app1 := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
	app1 = append(app1, payload...)

	out := make([]byte, 0, len(data)+len(app1))
	out = append(out, 0xFF, 0xD8) // SOI
	i := 2
	// Keep a leading APP0 (JFIF) first, then put our APP1 right after it.
	if i+4 <= len(data) && data[i] == 0xFF && data[i+1] == 0xE0 {
		segEnd := i + 2 + (int(data[i+2])<<8 | int(data[i+3]))
		if segEnd <= len(data) {
			out = append(out, data[i:segEnd]...)
			i = segEnd
		}
	}
	out = append(out, app1...)
	// Copy the remaining segments, dropping any existing APP1/Exif so we don't duplicate it.
	for i < len(data) {
		if data[i] != 0xFF || i+1 >= len(data) {
			out = append(out, data[i:]...)
			break
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // SOS / EOI — copy the rest verbatim
			out = append(out, data[i:]...)
			break
		}
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) { // standalone markers
			out = append(out, data[i], data[i+1])
			i += 2
			continue
		}
		if i+4 > len(data) {
			out = append(out, data[i:]...)
			break
		}
		segEnd := i + 2 + (int(data[i+2])<<8 | int(data[i+3]))
		if segEnd > len(data) {
			out = append(out, data[i:]...)
			break
		}
		if marker == 0xE1 && segEnd-i >= 10 && string(data[i+4:i+10]) == "Exif\x00\x00" {
			i = segEnd // drop the old Exif
			continue
		}
		out = append(out, data[i:segEnd]...)
		i = segEnd
	}
	return out
}

// buildExifTIFF builds a minimal big-endian TIFF block with IFD0 holding the ASCII
// ImageDescription and Artist tags (ascending tag order, as TIFF requires).
func buildExifTIFF(title, artist string) []byte {
	type field struct {
		tag  uint16
		data []byte // ASCII bytes including the trailing NUL
	}
	var fields []field
	if title != "" {
		fields = append(fields, field{tagImageDescription, append([]byte(title), 0)})
	}
	if artist != "" {
		fields = append(fields, field{tagArtist, append([]byte(artist), 0)})
	}
	if len(fields) == 0 {
		return nil
	}
	be := binary.BigEndian
	n := len(fields)
	dataStart := 8 + 2 + n*12 + 4 // header + count + entries + next-IFD pointer

	buf := new(bytes.Buffer)
	buf.WriteString("MM")                   // big-endian
	_ = binary.Write(buf, be, uint16(0x2A)) // magic
	_ = binary.Write(buf, be, uint32(8))    // IFD0 offset
	_ = binary.Write(buf, be, uint16(n))    // entry count
	var overflow []byte
	for _, f := range fields {
		_ = binary.Write(buf, be, f.tag)
		_ = binary.Write(buf, be, uint16(2))           // ASCII
		_ = binary.Write(buf, be, uint32(len(f.data))) // count (with NUL)
		if len(f.data) <= 4 {
			v := make([]byte, 4)
			copy(v, f.data)
			buf.Write(v) // inline value, left-justified
		} else {
			_ = binary.Write(buf, be, uint32(dataStart+len(overflow)))
			overflow = append(overflow, f.data...)
			if len(overflow)%2 == 1 {
				overflow = append(overflow, 0) // word-align the next value
			}
		}
	}
	_ = binary.Write(buf, be, uint32(0)) // no next IFD
	buf.Write(overflow)
	return buf.Bytes()
}
