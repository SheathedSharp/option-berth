package ports

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"
)

// Every hop of the PEB walk is decodable without Windows, so these tests pin
// the ABI constants and each decoder here, on the developer's machine, and
// then compose them over a synthetic process image. A wrong offset would make
// ReadProcessMemory return real bytes from the wrong field, so the numbers are
// checked against the ones ntdll's headers give, not only against each other.

func TestWindowsABIGeometry(t *testing.T) {
	cases := []struct {
		name   string
		g      winDirGeometry
		params uintptr
		curDir uintptr
	}{
		{"64-bit", winDir64, 0x20, 0x38},
		{"32-bit", winDir32, 0x10, 0x24},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.g.params != tc.params {
				t.Errorf("PEB.ProcessParameters = %#x, want %#x", tc.g.params, tc.params)
			}
			if tc.g.currentDir != tc.curDir {
				t.Errorf("CurrentDirectory.DosPath = %#x, want %#x", tc.g.currentDir, tc.curDir)
			}
			if got := tc.g.headerSize(); got != 2*tc.g.wordsize {
				t.Errorf("headerSize = %d, want %d", got, 2*tc.g.wordsize)
			}
			if got := tc.g.bufferOffset(); got != uintptr(tc.g.wordsize) {
				t.Errorf("bufferOffset = %d, want %d", got, tc.g.wordsize)
			}
		})
	}
}

func TestSelfGeometryFollowsThisBinary(t *testing.T) {
	want := winDir32
	if unsafe.Sizeof(uintptr(0)) == 8 {
		want = winDir64
	}
	if got := selfGeometry(); got != want {
		t.Fatalf("selfGeometry() = %+v, want %+v", got, want)
	}
}

func TestUintptrValueDecodesLittleEndian(t *testing.T) {
	buf := []byte{
		0xEF, 0xBE, 0xAD, 0xDE, 0x00, 0x00, 0x00, 0x00,
		0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x00,
	}
	cases := []struct {
		name     string
		at       uintptr
		wordsize int
		want     uint64
	}{
		{"64-bit low", 0, 8, 0xdeadbeef},
		{"64-bit high", 8, 8, 0x0077665544332211},
		{"32-bit", 0, 4, 0xdeadbeef},
		{"word size 3 is not a pointer", 0, 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := uintptrValue(buf, tc.at, tc.wordsize)
			if tc.want == 0 {
				if ok {
					t.Fatalf("uintptrValue(%d, %d) = %#x, want refusal", tc.at, tc.wordsize, got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("uintptrValue(%d, %d) = %#x, %v; want %#x, true", tc.at, tc.wordsize, got, ok, tc.want)
			}
		})
	}
	// A refused remote read arrives as a short buffer; a partial pointer must
	// never decode.
	if _, ok := uintptrValue(buf[:7], 0, 8); ok {
		t.Error("a 7-byte buffer was accepted as a 64-bit pointer")
	}
	if _, ok := uintptrValue(buf, 9, 8); ok {
		t.Error("a pointer was read past the end of the buffer")
	}
}

// unicodeHeader lays out the bytes of a UNICODE_STRING: two 16-bit lengths and
// one pointer of the geometry's width.
func unicodeHeader(g winDirGeometry, length, capacity uint16, data uint64) []byte {
	b := make([]byte, g.headerSize())
	b[0], b[1] = byte(length), byte(length>>8)
	b[2], b[3] = byte(capacity), byte(capacity>>8)
	for i := 0; i < g.wordsize; i++ {
		b[int(g.bufferOffset())+i] = byte(data >> (8 * i))
	}
	return b
}

func TestDosPathReadsLengthAndBuffer(t *testing.T) {
	// C:\src\api is ten characters, so Length is twenty bytes. Reading it as
	// a character count would cut the path in half.
	header := unicodeHeader(winDir64, 20, 22, 0x00007ffe12340000)
	size, data, ok := winDir64.dosPath(header)
	if !ok {
		t.Fatal("a well-formed UNICODE_STRING was refused")
	}
	if size != 20 {
		t.Errorf("size = %d, want 20 bytes", size)
	}
	if data != 0x00007ffe12340000 {
		t.Errorf("buffer = %#x, want 0x7ffe12340000", data)
	}
}

func TestDosPathRejectsUnusableHeaders(t *testing.T) {
	full := unicodeHeader(winDir64, 20, 22, 0x1000)
	cases := []struct {
		name   string
		header []byte
	}{
		{"short header", full[:10]},
		{"null buffer", unicodeHeader(winDir64, 20, 22, 0)},
		{"zero length", unicodeHeader(winDir64, 0, 22, 0x1000)},
		{"odd length", unicodeHeader(winDir64, 21, 22, 0x1000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := winDir64.dosPath(tc.header); ok {
				t.Error("an unusable header was accepted")
			}
		})
	}
}

// utf16LE encodes s the way the PEB stores a path, with an optional NUL pad.
func utf16LE(s string, pad int) []byte {
	units := append(utf16.Encode([]rune(s)), make([]uint16, pad)...)
	b := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[2*i:], u)
	}
	return b
}

type utf16Case struct {
	label string
	in    []byte
	want  string
}

func TestUTF16Text(t *testing.T) {
	cases := []utf16Case{
		{"plain path", utf16LE(`C:\src\api`, 0), `C:\src\api`},
		{"NUL padded", utf16LE(`C:\src\api`, 1), `C:\src\api`},
		{"non-ascii", utf16LE(`C:\Users\Example User\Bürö`, 0), `C:\Users\Example User\Bürö`},
		{"surrogate pair", utf16LE("C:\\emoji\\🚀", 0), "C:\\emoji\\🚀"},
		{"empty", nil, ""},
		{"odd trailing byte", []byte{0x43, 0x00, 0x3A}, "C"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := utf16Text(tc.in); got != tc.want {
				t.Errorf("utf16Text = %q, want %q", got, tc.want)
			}
		})
	}
}

type textCase struct{ label, in, want string }

func TestNormalizeWindowsCwd(t *testing.T) {
	cases := []textCase{
		{"trailing separator", `C:\src\api\`, `C:\src\api`},
		{"already clean", `C:\src\api`, `C:\src\api`},
		{"drive root", `C:\`, `C:\`},
		{"lowercase drive root", `d:\`, `d:\`},
		{"drive-relative path is left alone", `C:`, `C:`},
		{"extended-length prefix", `\\?\C:\src\api\`, `C:\src\api`},
		{"extended-length UNC", `\\?\UNC\build\share\api\`, `\\build\share\api`},
		{"plain UNC", `\\build\share\api\`, `\\build\share\api`},
		{"surrounding spaces", "  C:\\src\\api\\  ", `C:\src\api`},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := normalizeWindowsCwd(tc.in)
			switch {
			case got != tc.want:
				t.Errorf("normalizeWindowsCwd(%q) = %q, want %q", tc.in, got, tc.want)
			case strings.HasPrefix(got, `\\?\`):
				t.Errorf("normalizeWindowsCwd(%q) left the extended-length prefix on", tc.in)
			}
		})
	}
}

// fakeProcessImage is a byte slice standing in for another process's address
// space, rooted at base. It lets the three hops be composed here rather than
// trusting each decoder in isolation.
type fakeProcessImage struct {
	base uint64
	mem  []byte
}

func (f fakeProcessImage) at(addr uint64, n int) []byte {
	off := addr - f.base
	return f.mem[off : off+uint64(n)]
}

func (f fakeProcessImage) putPointer(addr, value uint64, wordsize int) {
	target := f.at(addr, wordsize)
	for i := 0; i < wordsize; i++ {
		target[i] = byte(value >> (8 * i))
	}
}

func (f fakeProcessImage) putUTF16(addr uint64, s string) {
	target := f.at(addr, len(utf16LE(s, 0)))
	copy(target, utf16LE(s, 0))
}

func TestPEBWalkOverASyntheticProcessImage(t *testing.T) {
	pebAddr, paramsAddr, pathAddr := uint64(0x7FF700000000), uint64(0x7FF700010000), uint64(0x7FF700020000)
	g := winDir64
	image := fakeProcessImage{base: pebAddr, mem: make([]byte, 0x30000)}
	image.putPointer(pebAddr+uint64(g.params), paramsAddr, g.wordsize)
	copy(image.at(paramsAddr+uint64(g.currentDir), g.headerSize()),
		unicodeHeader(g, uint16(len(`C:\src\api`)*2), uint16(len(`C:\src\api`)*2+2), pathAddr))
	image.putUTF16(pathAddr, `C:\src\api`)

	params, ok := uintptrValue(image.at(pebAddr+uint64(g.params), g.wordsize), 0, g.wordsize)
	if !ok || params != paramsAddr {
		t.Fatalf("ProcessParameters = %#x, %v; want %#x", params, ok, paramsAddr)
	}
	size, data, ok := g.dosPath(image.at(params+uint64(g.currentDir), g.headerSize()))
	if !ok || data != pathAddr {
		t.Fatalf("CurrentDirectory.DosPath = %d, %#x, %v; want buffer %#x", size, data, ok, pathAddr)
	}
	if got := normalizeWindowsCwd(utf16Text(image.at(data, size))); got != `C:\src\api` {
		t.Fatalf("the walk produced %q, want %q", got, `C:\src\api`)
	}
}
