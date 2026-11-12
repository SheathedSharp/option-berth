package ports

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
	"unsafe"
)

// A Windows process keeps its current directory in its own address space, not
// in a table the kernel hands out. Reading it means following three pointers
// through foreign memory:
//
//	PEB.ProcessParameters                  -> RTL_USER_PROCESS_PARAMETERS
//	...CurrentDirectory.DosPath            -> UNICODE_STRING
//	UNICODE_STRING.Buffer                  -> the UTF-16 path
//
// Only the offsets depend on the target ABI. Everything else works on plain
// byte slices, so this file builds and is tested on every platform;
// cwd_windows.go supplies the remote reads.

// winDirGeometry is where one target ABI keeps those three structures.
// Offsets are bytes from the start of the structure named by the field.
type winDirGeometry struct {
	wordsize   int     // pointer width in the target process
	params     uintptr // PEB.ProcessParameters
	currentDir uintptr // RTL_USER_PROCESS_PARAMETERS.CurrentDirectory.DosPath
}

// The two Windows ABIs. A 32-bit target has the same structures with
// half-width pointers; the target's word size, not this binary's, picks the
// numbers, so they cannot both be derived from one compiled layout.
var (
	winDir64 = winDirGeometry{wordsize: 8, params: 0x20, currentDir: 0x38}
	winDir32 = winDirGeometry{wordsize: 4, params: 0x10, currentDir: 0x24}
)

// bufferOffset is UNICODE_STRING.Buffer: two 16-bit lengths and then the
// pointer, aligned to the target's word size.
func (g winDirGeometry) bufferOffset() uintptr { return uintptr(g.wordsize) }

// headerSize is sizeof(UNICODE_STRING) in the target: the two lengths are
// padded out to the pointer width, which halves the structure on a 32-bit
// target.
func (g winDirGeometry) headerSize() int { return 2 * g.wordsize }

// selfGeometry is the ABI this binary reads natively. A 64-bit build reads
// 64-bit PEBs, including those of WOW64 processes, which keep a populated
// 64-bit PEB next to their 32-bit one.
func selfGeometry() winDirGeometry {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return winDir64
	}
	return winDir32
}

// uintptrValue decodes one little-endian pointer of the given width. A short
// buffer is how a refused or truncated remote read arrives here: half a
// pointer would otherwise decode to a plausible-looking wrong address.
func uintptrValue(b []byte, at uintptr, wordsize int) (uint64, bool) {
	if at > uintptr(len(b)) || uintptr(len(b))-at < uintptr(wordsize) {
		return 0, false
	}
	switch wordsize {
	case 8:
		return binary.LittleEndian.Uint64(b[at:]), true
	case 4:
		return uint64(binary.LittleEndian.Uint32(b[at:])), true
	default:
		return 0, false
	}
}

// dosPath decodes the UNICODE_STRING header at the start of b: how many bytes
// the path occupies and where its buffer starts. Length counts bytes, not
// characters, so an odd value is not UTF-16; a zero length or a null buffer
// means the target has no directory to report.
func (g winDirGeometry) dosPath(b []byte) (size int, data uint64, ok bool) {
	if len(b) < g.headerSize() {
		return 0, 0, false
	}
	n := int(binary.LittleEndian.Uint16(b[:2]))
	addr, ok := uintptrValue(b, g.bufferOffset(), g.wordsize)
	if !ok || addr == 0 || n <= 0 || n%2 != 0 {
		return 0, 0, false
	}
	return n, addr, true
}

// utf16Text decodes the UTF-16LE bytes of a DosPath. The PEB usually pads the
// buffer with a NUL even though Length excludes it; the terminator is dropped
// so it cannot travel into a group name. A trailing odd byte has no character
// to decode and is ignored.
func utf16Text(b []byte) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	return string(utf16.Decode(units))
}

// normalizeWindowsCwd turns the PEB's spelling of a directory into the key the
// rest of option-berth compares. Two decorations have to go: the trailing
// separator the PEB always stores (groups.Canonical never produces one, so it
// would split one project into two groups), and the extended-length `\\?\`
// prefix that long-path launches carry (`\\?\UNC\host\share` unfolds back to
// `\\host\share`). A drive root keeps its separator: `C:\` is fixed, while a
// bare `C:` means wherever that drive happens to be parked.
func normalizeWindowsCwd(raw string) string {
	s := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(s, `\\?\UNC\`):
		s = `\\` + s[len(`\\?\UNC\`):]
	case strings.HasPrefix(s, `\\?\`):
		s = s[len(`\\?\`):]
	}
	if isDriveRoot(s) || len(s) < 2 {
		return s
	}
	trimmed := strings.TrimRight(s, `\/`)
	switch {
	case trimmed == s:
		return s
	case trimmed == "":
		return `\`
	case len(trimmed) == 2 && trimmed[1] == ':':
		// A root written with redundant separators keeps being a root.
		return trimmed + `\`
	}
	return trimmed
}

// isDriveRoot reports whether s is exactly `X:\` or `X:/`.
func isDriveRoot(s string) bool {
	if len(s) != 3 || s[1] != ':' || (s[2] != '\\' && s[2] != '/') {
		return false
	}
	c := s[0] | 0x20
	return c >= 'a' && c <= 'z'
}
