// A small, self-contained QR Code encoder (standard library only) used to render
// a scannable "connect over LAN" code for the operator console. It supports just
// what this app needs: byte mode, error-correction level M, automatic version
// selection (1–10, far more than a short LAN URL needs), and PNG output.
//
// The encoding follows ISO/IEC 18004. The matrix construction, masking and
// penalty scoring are a faithful port of Project Nayuki's reference QR Code
// generator (public domain, https://www.nayuki.io/page/qr-code-generator-library),
// adapted to Go and trimmed to a single mode and EC level.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
)

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// --- Reed–Solomon over GF(2^8) (primitive polynomial 0x11D) ---------------

// rsMul multiplies two field elements without lookup tables.
func rsMul(x, y byte) byte {
	z := 0
	for i := 7; i >= 0; i-- {
		z = (z << 1) ^ ((z >> 7) * 0x11D)
		z ^= int((y>>uint(i))&1) * int(x)
	}
	return byte(z & 0xFF)
}

// rsDivisor returns the generator polynomial of the given degree (number of EC
// codewords), as coefficients with the highest power stored last.
func rsDivisor(degree int) []byte {
	result := make([]byte, degree)
	result[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := 0; j < degree; j++ {
			result[j] = rsMul(result[j], root)
			if j+1 < degree {
				result[j] ^= result[j+1]
			}
		}
		root = rsMul(root, 0x02)
	}
	return result
}

// rsRemainder computes the EC codewords for data using the given divisor.
func rsRemainder(data, divisor []byte) []byte {
	result := make([]byte, len(divisor))
	for _, b := range data {
		factor := b ^ result[0]
		copy(result, result[1:])
		result[len(result)-1] = 0
		for i := range result {
			result[i] ^= rsMul(divisor[i], factor)
		}
	}
	return result
}

// --- Per-version tables (error-correction level M only) -------------------

// ecCodewordsPerBlockM[v] / numECBlocksM[v] for versions 1..10.
var ecCodewordsPerBlockM = [11]int{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26}
var numECBlocksM = [11]int{0, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5}

// alignPositions holds the alignment-pattern centre coordinates per version.
var alignPositions = [11][]int{
	{}, {}, {6, 18}, {6, 22}, {6, 26}, {6, 30},
	{6, 34}, {6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50},
}

const qrMaxVersion = 10

// qrNumRawDataModules returns the number of data+EC bits available for a version.
func qrNumRawDataModules(ver int) int {
	result := (16*ver + 128) * ver + 64
	if ver >= 2 {
		numAlign := ver/7 + 2
		result -= (25*numAlign-10)*numAlign - 55
		if ver >= 7 {
			result -= 36
		}
	}
	return result
}

func qrNumRawCodewords(ver int) int  { return qrNumRawDataModules(ver) / 8 }
func qrNumDataCodewords(ver int) int { return qrNumRawCodewords(ver) - numECBlocksM[ver]*ecCodewordsPerBlockM[ver] }

// qrCharCountBits is 8 for versions 1–9 and 16 for 10+ (byte mode).
func qrCharCountBits(ver int) int {
	if ver <= 9 {
		return 8
	}
	return 16
}

// qrChooseVersion picks the smallest version (1..10) that fits dataLen bytes.
func qrChooseVersion(dataLen int) (int, error) {
	for v := 1; v <= qrMaxVersion; v++ {
		capacityBits := qrNumDataCodewords(v) * 8
		needed := 4 + qrCharCountBits(v) + 8*dataLen
		if needed <= capacityBits {
			return v, nil
		}
	}
	return 0, fmt.Errorf("qr: data too long (%d bytes)", dataLen)
}

// --- Bit buffer ------------------------------------------------------------

type bitBuffer struct{ bits []bool }

func (b *bitBuffer) appendBits(val uint, n int) {
	for i := n - 1; i >= 0; i-- {
		b.bits = append(b.bits, (val>>uint(i))&1 != 0)
	}
}
func (b *bitBuffer) len() int { return len(b.bits) }
func (b *bitBuffer) bytes() []byte {
	out := make([]byte, (len(b.bits)+7)/8)
	for i, bit := range b.bits {
		if bit {
			out[i>>3] |= 1 << uint(7-(i&7))
		}
	}
	return out
}

// --- Matrix ----------------------------------------------------------------

type qrMatrix struct {
	version int
	size    int
	modules [][]bool // true = dark
	isFunc  [][]bool // true = reserved function module (not data, not masked)
}

func newQrMatrix(version int) *qrMatrix {
	size := version*4 + 17
	m := &qrMatrix{version: version, size: size}
	m.modules = make([][]bool, size)
	m.isFunc = make([][]bool, size)
	for i := range m.modules {
		m.modules[i] = make([]bool, size)
		m.isFunc[i] = make([]bool, size)
	}
	return m
}

func (m *qrMatrix) setFunc(x, y int, dark bool) {
	m.modules[y][x] = dark
	m.isFunc[y][x] = true
}

func getBit(x uint, i int) bool { return (x>>uint(i))&1 != 0 }

func (m *qrMatrix) drawFunctionPatterns() {
	// Timing patterns.
	for i := 0; i < m.size; i++ {
		m.setFunc(6, i, i%2 == 0)
		m.setFunc(i, 6, i%2 == 0)
	}
	// Finder patterns (with surrounding separators).
	m.drawFinder(3, 3)
	m.drawFinder(m.size-4, 3)
	m.drawFinder(3, m.size-4)
	// Alignment patterns.
	pos := alignPositions[m.version]
	n := len(pos)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if (i == 0 && j == 0) || (i == 0 && j == n-1) || (i == n-1 && j == 0) {
				continue // overlaps finder patterns
			}
			m.drawAlignment(pos[i], pos[j])
		}
	}
	// Reserve format/version areas (dummy mask 0 for now).
	m.drawFormatBits(0)
	m.drawVersion()
}

func (m *qrMatrix) drawFinder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			x, y := cx+dx, cy+dy
			if x < 0 || x >= m.size || y < 0 || y >= m.size {
				continue
			}
			dist := max(abs(dx), abs(dy))
			m.setFunc(x, y, dist != 2 && dist != 4)
		}
	}
}

func (m *qrMatrix) drawAlignment(cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			m.setFunc(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

// drawFormatBits writes the 15-bit format information (EC level M + mask).
func (m *qrMatrix) drawFormatBits(mask int) {
	const eclFormatBitsM = 0 // L=1, M=0, Q=3, H=2
	data := eclFormatBitsM<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := uint((data<<10 | rem) ^ 0x5412)

	for i := 0; i <= 5; i++ {
		m.setFunc(8, i, getBit(bits, i))
	}
	m.setFunc(8, 7, getBit(bits, 6))
	m.setFunc(8, 8, getBit(bits, 7))
	m.setFunc(7, 8, getBit(bits, 8))
	for i := 9; i < 15; i++ {
		m.setFunc(14-i, 8, getBit(bits, i))
	}
	for i := 0; i < 8; i++ {
		m.setFunc(m.size-1-i, 8, getBit(bits, i))
	}
	for i := 8; i < 15; i++ {
		m.setFunc(8, m.size-15+i, getBit(bits, i))
	}
	m.setFunc(8, m.size-8, true) // always-dark module
}

// drawVersion writes the 18-bit version information (versions 7+ only).
func (m *qrMatrix) drawVersion() {
	if m.version < 7 {
		return
	}
	rem := m.version
	for i := 0; i < 12; i++ {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
	}
	bits := uint(m.version<<12 | rem)
	for i := 0; i < 18; i++ {
		bit := getBit(bits, i)
		a := m.size - 11 + i%3
		b := i / 3
		m.setFunc(a, b, bit)
		m.setFunc(b, a, bit)
	}
}

// drawCodewords lays the full data+EC bitstream into the matrix in zigzag order.
func (m *qrMatrix) drawCodewords(data []byte) {
	i := 0 // bit index
	for right := m.size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < m.size; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := (right+1)&2 == 0
				y := vert
				if upward {
					y = m.size - 1 - vert
				}
				if !m.isFunc[y][x] && i < len(data)*8 {
					m.modules[y][x] = getBit(uint(data[i>>3]), 7-(i&7))
					i++
				}
			}
		}
	}
}

// qrMaskCondition reports whether the given mask inverts module (x, y).
func qrMaskCondition(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	case 7:
		return ((x+y)%2+x*y%3)%2 == 0
	}
	return false
}

// applyMask toggles every non-function module where the mask condition holds.
func (m *qrMatrix) applyMask(mask int) {
	for y := 0; y < m.size; y++ {
		for x := 0; x < m.size; x++ {
			if !m.isFunc[y][x] && qrMaskCondition(mask, x, y) {
				m.modules[y][x] = !m.modules[y][x]
			}
		}
	}
}

const (
	penaltyN1 = 3
	penaltyN2 = 3
	penaltyN3 = 40
	penaltyN4 = 10
)

func (m *qrMatrix) penalty() int {
	result := 0
	size := m.size
	mod := m.modules

	// Rule 1 + 3: runs in rows.
	for y := 0; y < size; y++ {
		runColor := false
		runLen := 0
		var hist [7]int
		for x := 0; x < size; x++ {
			if mod[y][x] == runColor {
				runLen++
				if runLen == 5 {
					result += penaltyN1
				} else if runLen > 5 {
					result++
				}
			} else {
				m.finderAddHistory(runLen, &hist)
				if !runColor {
					result += m.finderCountPatterns(hist) * penaltyN3
				}
				runColor = mod[y][x]
				runLen = 1
			}
		}
		result += m.finderTerminate(runColor, runLen, &hist) * penaltyN3
	}
	// Rule 1 + 3: runs in columns.
	for x := 0; x < size; x++ {
		runColor := false
		runLen := 0
		var hist [7]int
		for y := 0; y < size; y++ {
			if mod[y][x] == runColor {
				runLen++
				if runLen == 5 {
					result += penaltyN1
				} else if runLen > 5 {
					result++
				}
			} else {
				m.finderAddHistory(runLen, &hist)
				if !runColor {
					result += m.finderCountPatterns(hist) * penaltyN3
				}
				runColor = mod[y][x]
				runLen = 1
			}
		}
		result += m.finderTerminate(runColor, runLen, &hist) * penaltyN3
	}
	// Rule 2: 2x2 blocks of one color.
	for y := 0; y < size-1; y++ {
		for x := 0; x < size-1; x++ {
			c := mod[y][x]
			if c == mod[y][x+1] && c == mod[y+1][x] && c == mod[y+1][x+1] {
				result += penaltyN2
			}
		}
	}
	// Rule 4: overall dark/light balance.
	dark := 0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if mod[y][x] {
				dark++
			}
		}
	}
	total := size * size
	k := (abs(dark*20-total*10)+total-1)/total - 1
	result += k * penaltyN4
	return result
}

func (m *qrMatrix) finderAddHistory(runLen int, hist *[7]int) {
	if hist[0] == 0 {
		runLen += m.size // light border before the first run
	}
	copy(hist[1:], hist[:6])
	hist[0] = runLen
}

func (m *qrMatrix) finderCountPatterns(hist [7]int) int {
	n := hist[1]
	core := n > 0 && hist[2] == n && hist[3] == n*3 && hist[4] == n && hist[5] == n
	count := 0
	if core && hist[0] >= n*4 && hist[6] >= n {
		count++
	}
	if core && hist[6] >= n*4 && hist[0] >= n {
		count++
	}
	return count
}

func (m *qrMatrix) finderTerminate(runColor bool, runLen int, hist *[7]int) int {
	if runColor {
		m.finderAddHistory(runLen, hist)
		runLen = 0
	}
	runLen += m.size
	m.finderAddHistory(runLen, hist)
	return m.finderCountPatterns(*hist)
}

// --- Top-level encode ------------------------------------------------------

// qrEncode builds the final dark/light module matrix for the given text using
// byte mode, EC level M, and automatic version + mask selection.
func qrEncode(text string) ([][]bool, error) {
	data := []byte(text)
	ver, err := qrChooseVersion(len(data))
	if err != nil {
		return nil, err
	}

	bb := &bitBuffer{}
	bb.appendBits(0x4, 4) // byte mode indicator
	bb.appendBits(uint(len(data)), qrCharCountBits(ver))
	for _, b := range data {
		bb.appendBits(uint(b), 8)
	}
	capacityBits := qrNumDataCodewords(ver) * 8
	bb.appendBits(0, min(4, capacityBits-bb.len())) // terminator
	bb.appendBits(0, (8-bb.len()%8)%8)              // pad to byte boundary
	for pad := byte(0xEC); bb.len() < capacityBits; pad ^= 0xEC ^ 0x11 {
		bb.appendBits(uint(pad), 8)
	}
	codewords := qrAddEccAndInterleave(ver, bb.bytes())

	m := newQrMatrix(ver)
	m.drawFunctionPatterns()
	m.drawCodewords(codewords)

	// Pick the mask with the lowest penalty.
	bestMask, minPenalty := 0, math.MaxInt
	for mask := 0; mask < 8; mask++ {
		m.applyMask(mask)
		m.drawFormatBits(mask)
		if p := m.penalty(); p < minPenalty {
			minPenalty = p
			bestMask = mask
		}
		m.applyMask(mask) // undo
	}
	m.applyMask(bestMask)
	m.drawFormatBits(bestMask)
	return m.modules, nil
}

// qrAddEccAndInterleave splits the data codewords into blocks, appends Reed–Solomon
// EC codewords, and interleaves them into the final transmission order.
func qrAddEccAndInterleave(ver int, data []byte) []byte {
	numBlocks := numECBlocksM[ver]
	ecLen := ecCodewordsPerBlockM[ver]
	rawCodewords := qrNumRawCodewords(ver)
	numShort := numBlocks - rawCodewords%numBlocks
	shortLen := rawCodewords / numBlocks
	div := rsDivisor(ecLen)

	blocks := make([][]byte, numBlocks)
	k := 0
	for i := 0; i < numBlocks; i++ {
		datLen := shortLen - ecLen
		if i >= numShort {
			datLen++
		}
		dat := make([]byte, datLen)
		copy(dat, data[k:k+datLen])
		k += datLen
		ecc := rsRemainder(dat, div)
		blk := append([]byte{}, dat...)
		if i < numShort {
			blk = append(blk, 0) // placeholder to align short blocks
		}
		blk = append(blk, ecc...)
		blocks[i] = blk
	}

	var result []byte
	blkLen := shortLen + 1
	for i := 0; i < blkLen; i++ {
		for j := 0; j < numBlocks; j++ {
			if i == shortLen-ecLen && j < numShort {
				continue // skip the short-block placeholder column
			}
			result = append(result, blocks[j][i])
		}
	}
	return result
}

// --- PNG rendering ---------------------------------------------------------

// qrPNG renders a module matrix to PNG bytes: each module is scale×scale pixels,
// surrounded by a quiet zone of quiet modules. Dark = black, light = white.
func qrPNG(matrix [][]bool, scale, quiet int) ([]byte, error) {
	if scale < 1 {
		scale = 1
	}
	n := len(matrix)
	dim := (n + 2*quiet) * scale
	img := image.NewGray(image.Rect(0, 0, dim, dim))
	for i := range img.Pix {
		img.Pix[i] = 0xFF // white background (incl. quiet zone)
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if !matrix[y][x] {
				continue
			}
			px0 := (x + quiet) * scale
			py0 := (y + quiet) * scale
			for dy := 0; dy < scale; dy++ {
				row := (py0 + dy) * img.Stride
				for dx := 0; dx < scale; dx++ {
					img.Pix[row+px0+dx] = 0x00
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// qrPNGForText is the convenience entry point: encode text and render a PNG.
func qrPNGForText(text string, scale, quiet int) ([]byte, error) {
	matrix, err := qrEncode(text)
	if err != nil {
		return nil, err
	}
	return qrPNG(matrix, scale, quiet)
}
