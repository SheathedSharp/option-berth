//go:build windows

package ports

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

// batchGetCwds resolves the working directory of every pid it can, and omits
// the rest.
//
// Windows has neither lsof nor /proc: another process's current directory is
// only readable out of that process's own memory, so a pid can refuse at any
// of four steps. Protected and cross-user processes deny the handle, and a
// process may exit between the scan that listed it and this call. The batch
// keeps going: naming nine listeners out of ten is worth more than naming none
// because a system process said no, and an omitted pid stays unknown.
//
// Limitation: a 32-bit option-berth cannot read a 64-bit process. Its
// NtQueryInformationProcess is the WOW64 thunk that reports the 32-bit view,
// and the target's structures sit above the 4 GB its pointers can address.
// Those pids are skipped and get no entry — the same answer they had before
// this file existed. A 64-bit option-berth reads 64-bit processes and WOW64
// 32-bit ones alike.
func batchGetCwds(pids []int) map[int]string {
	return batchGetCwdsContext(context.Background(), pids)
}

func batchGetCwdsContext(ctx context.Context, pids []int) map[int]string {
	out := make(map[int]string, len(pids))
	if len(pids) == 0 || ctx.Err() != nil {
		return out
	}
	g := selfGeometry()
	selfIsWow64 := isWow64Self()
	for _, pid := range pids {
		if ctx.Err() != nil {
			return nil
		}
		if pid <= 0 {
			continue
		}
		if dir := remoteCwd(uint32(pid), g, selfIsWow64); dir != "" {
			out[pid] = dir
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return out
}

// isWow64Self reports whether this binary is 32-bit code running on 64-bit
// Windows. A 32-bit build on native 32-bit Windows has no 64-bit process to
// fail on, and a 64-bit build is never WOW64; only that one combination has
// the limitation described above.
func isWow64Self() bool {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		return false
	}
	var wow64 bool
	if err := windows.IsWow64Process(windows.CurrentProcess(), &wow64); err != nil {
		return false
	}
	return wow64
}

// remoteCwd walks one process's PEB, returning "" if any hop fails.
func remoteCwd(pid uint32, g winDirGeometry, selfIsWow64 bool) string {
	h, err := openProcessForRead(pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	if selfIsWow64 && !isWow64Process(h) {
		// 32-bit reader, 64-bit target: unsupported, see the limitation above.
		return ""
	}

	peb, ok := pebAddress(h)
	if !ok {
		return ""
	}
	params, ok := readPointer(h, peb+g.params, g.wordsize)
	if !ok {
		return ""
	}

	header := make([]byte, g.headerSize())
	if !readRemoteMemory(h, params+g.currentDir, header) {
		return ""
	}
	size, data, ok := g.dosPath(header)
	if !ok {
		return ""
	}

	path := make([]byte, size)
	if !readRemoteMemory(h, uintptr(data), path) {
		return ""
	}
	dir := utf16Text(path)
	if dir == "" {
		return ""
	}
	return normalizeWindowsCwd(longPathName(dir))
}

// openProcessForRead opens the handle the walk needs. The documented right is
// PROCESS_QUERY_INFORMATION, but from Vista on PROCESS_QUERY_LIMITED_INFORMATION
// is enough and is granted in more places, so it is worth one retry.
func openProcessForRead(pid uint32) (windows.Handle, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err == nil {
		return h, nil
	}
	return windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, pid)
}

// pebAddress asks the kernel where the target's PEB begins. The call returns
// the address even when reading it later will be refused, so the walk must
// still treat every following hop as fallible.
func pebAddress(h windows.Handle) (uintptr, bool) {
	var info windows.PROCESS_BASIC_INFORMATION
	var written uint32
	size := uint32(unsafe.Sizeof(info))
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&info), size, &written); err != nil {
		return 0, false
	}
	if info.PebBaseAddress == nil {
		return 0, false
	}
	return uintptr(unsafe.Pointer(info.PebBaseAddress)), true
}

// readPointer reads one pointer-sized field out of the target's memory.
func readPointer(h windows.Handle, at uintptr, wordsize int) (uintptr, bool) {
	buf := make([]byte, wordsize)
	if !readRemoteMemory(h, at, buf) {
		return 0, false
	}
	value, ok := uintptrValue(buf, 0, wordsize)
	if !ok || value == 0 {
		return 0, false
	}
	return uintptr(value), true
}

// isWow64Process reports whether h is a 32-bit process on 64-bit Windows. A
// handle that cannot answer counts as 64-bit, the direction this build must
// not read.
func isWow64Process(h windows.Handle) bool {
	var wow64 bool
	if err := windows.IsWow64Process(h, &wow64); err != nil {
		return false
	}
	return wow64
}

// readRemoteMemory fills buf completely or reports failure: a partial read
// means the address was wrong, and half a pointer decodes to a lie that looks
// plausible.
func readRemoteMemory(h windows.Handle, at uintptr, buf []byte) bool {
	if len(buf) == 0 {
		return false
	}
	var read uintptr
	if err := windows.ReadProcessMemory(h, at, &buf[0], uintptr(len(buf)), &read); err != nil {
		return false
	}
	return int(read) == len(buf)
}

// longPathName expands an 8.3 short name (`C:\PROGRA~1\api`) to its long
// spelling. A process keeps whatever it was launched with, while
// groups.Canonical resolves symlinks and reports the long form on Windows, so
// without this the same directory would key the group index twice. A path
// that no longer exists, or that GetLongPathName refuses, is returned as it
// was: a short-name group is still better than no group.
func longPathName(path string) string {
	wide, convErr := windows.UTF16PtrFromString(path)
	if convErr != nil {
		return path
	}
	needed, sizeErr := windows.GetLongPathName(wide, nil, 0)
	if sizeErr != nil || needed == 0 {
		return path
	}
	long := make([]uint16, needed)
	written, fillErr := windows.GetLongPathName(wide, &long[0], needed)
	switch {
	case fillErr != nil, written == 0, written > needed:
		return path
	}
	return windows.UTF16ToString(long[:written])
}
