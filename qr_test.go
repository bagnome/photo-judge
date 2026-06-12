package main

import (
	"bytes"
	"fmt"
	"image/png"
	"testing"
)

// qrBitReader reads bits MSB-first from a codeword slice.
type qrBitReader struct {
	data []byte
	pos  int
}

func (r *qrBitReader) read(n int) int {
	v := 0
	for i := 0; i < n; i++ {
		v <<= 1
		byteIdx := r.pos >> 3
		bit := 0
		if byteIdx < len(r.data) && r.data[byteIdx]&(1<<uint(7-(r.pos&7))) != 0 {
			bit = 1
		}
		v |= bit
		r.pos++
	}
	return v
}

// qrDeinterleave reverses qrAddEccAndInterleave and verifies each block's
// Reed–Solomon syndromes are zero (i.e. the EC codewords are valid), returning
// the original data codewords.
func qrDeinterleave(ver int, cw []byte) ([]byte, error) {
	numBlocks := numECBlocksM[ver]
	ecLen := ecCodewordsPerBlockM[ver]
	raw := qrNumRawCodewords(ver)
	numShort := numBlocks - raw%numBlocks
	shortLen := raw / numBlocks
	blkLen := shortLen + 1

	blocks := make([][]byte, numBlocks)
	for j := range blocks {
		blocks[j] = make([]byte, blkLen)
	}
	idx := 0
	for i := 0; i < blkLen; i++ {
		for j := 0; j < numBlocks; j++ {
			if i == shortLen-ecLen && j < numShort {
				continue
			}
			blocks[j][i] = cw[idx]
			idx++
		}
	}

	div := rsDivisor(ecLen)
	var data []byte
	for j := 0; j < numBlocks; j++ {
		dataLen := shortLen - ecLen
		if j >= numShort {
			dataLen++
		}
		var full []byte
		if j < numShort {
			full = append(full, blocks[j][:dataLen]...)   // data
			full = append(full, blocks[j][dataLen+1:]...) // ecc (skip placeholder)
		} else {
			full = append([]byte{}, blocks[j]...) // data + ecc contiguous
		}
		if rem := rsRemainder(full, div); !allZero(rem) {
			return nil, fmt.Errorf("block %d: nonzero RS syndrome %v", j, rem)
		}
		data = append(data, full[:dataLen]...)
	}
	return data, nil
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// qrDecode independently recovers the text from a finished module matrix,
// exercising format-bit decoding, mask removal, zigzag reading, de-interleaving,
// RS verification, and byte-mode parsing. It is the test-side inverse of qrEncode.
func qrDecode(modules [][]bool) (string, error) {
	n := len(modules)
	ver := (n - 17) / 4
	if ver < 1 || ver > qrMaxVersion || ver*4+17 != n {
		return "", fmt.Errorf("bad matrix size %d", n)
	}

	// Reconstruct which modules are function patterns.
	fm := newQrMatrix(ver)
	fm.drawFunctionPatterns()
	isFunc := fm.isFunc

	read := func(x, y int) uint {
		if modules[y][x] {
			return 1
		}
		return 0
	}

	// Decode the 15-bit format information (first copy) to recover EC level + mask.
	var fmtVal uint
	set := func(i int, x, y int) { fmtVal |= read(x, y) << uint(i) }
	for i := 0; i <= 5; i++ {
		set(i, 8, i)
	}
	set(6, 8, 7)
	set(7, 8, 8)
	set(8, 7, 8)
	for i := 9; i < 15; i++ {
		set(i, 14-i, 8)
	}
	dataFmt := (fmtVal ^ 0x5412) >> 10
	ecl := dataFmt >> 3
	mask := int(dataFmt & 7)
	if ecl != 0 {
		return "", fmt.Errorf("expected EC level M (0), got %d", ecl)
	}

	// Copy and remove the mask from data modules.
	cp := make([][]bool, n)
	for i := range cp {
		cp[i] = append([]bool{}, modules[i]...)
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !isFunc[y][x] && qrMaskCondition(mask, x, y) {
				cp[y][x] = !cp[y][x]
			}
		}
	}

	// Read the data+EC bitstream in the same zigzag order as drawCodewords.
	totalBits := qrNumRawCodewords(ver) * 8
	bits := make([]bool, 0, totalBits)
	i := 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < n; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := (right+1)&2 == 0
				y := vert
				if upward {
					y = n - 1 - vert
				}
				if !isFunc[y][x] && i < totalBits {
					bits = append(bits, cp[y][x])
					i++
				}
			}
		}
	}
	cw := make([]byte, qrNumRawCodewords(ver))
	for idx, b := range bits {
		if b {
			cw[idx>>3] |= 1 << uint(7-(idx&7))
		}
	}

	data, err := qrDeinterleave(ver, cw)
	if err != nil {
		return "", err
	}

	br := &qrBitReader{data: data}
	if mode := br.read(4); mode != 0x4 {
		return "", fmt.Errorf("expected byte mode (4), got %d", mode)
	}
	length := br.read(qrCharCountBits(ver))
	out := make([]byte, length)
	for k := 0; k < length; k++ {
		out[k] = byte(br.read(8))
	}
	return string(out), nil
}

func TestQREncodeDecodeRoundTrip(t *testing.T) {
	inputs := []string{
		"http://192.168.1.5",
		"http://192.168.1.50:8753",
		"http://10.0.0.123",
		"A",
		"",
		"The quick brown fox jumps over the lazy dog 0123456789",
	}
	for _, in := range inputs {
		t.Run(fmt.Sprintf("%q", in), func(t *testing.T) {
			matrix, err := qrEncode(in)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := qrDecode(matrix)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got != in {
				t.Fatalf("round-trip mismatch: got %q want %q", got, in)
			}
		})
	}
}

func TestQRFunctionPatterns(t *testing.T) {
	m, err := qrEncode("http://192.168.1.5")
	if err != nil {
		t.Fatal(err)
	}
	n := len(m)
	// Finder patterns: the three corners must have a dark centre and dark border.
	corners := [][2]int{{3, 3}, {n - 4, 3}, {3, n - 4}}
	for _, c := range corners {
		if !m[c[1]][c[0]] {
			t.Errorf("finder centre at (%d,%d) should be dark", c[0], c[1])
		}
	}
	// Timing pattern alternates along row/col 6.
	for i := 8; i < n-8; i++ {
		if m[6][i] != (i%2 == 0) {
			t.Errorf("timing row mismatch at col %d", i)
		}
	}
	// The always-dark module.
	if !m[n-8][8] {
		t.Error("dark module at (8, size-8) should be dark")
	}
}

func TestQRVersionSelection(t *testing.T) {
	cases := []struct {
		length  int
		wantVer int
	}{
		{1, 1}, {14, 1}, {15, 2}, {26, 2}, {27, 3},
	}
	for _, c := range cases {
		v, err := qrChooseVersion(c.length)
		if err != nil {
			t.Fatalf("len %d: %v", c.length, err)
		}
		if v != c.wantVer {
			t.Errorf("len %d: got version %d want %d", c.length, v, c.wantVer)
		}
	}
}

func TestQRPNGOutput(t *testing.T) {
	const text = "http://192.168.1.5"
	matrix, err := qrEncode(text)
	if err != nil {
		t.Fatal(err)
	}
	pngBytes, err := qrPNGForText(text, 6, 4)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	// (modules + quiet zone on both sides) * scale.
	want := (len(matrix) + 2*4) * 6
	if b := img.Bounds(); b.Dx() != want || b.Dy() != want {
		t.Errorf("png size = %dx%d, want %dx%d", b.Dx(), b.Dy(), want, want)
	}
}
