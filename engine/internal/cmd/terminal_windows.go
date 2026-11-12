//go:build windows

package cmd

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	k32                     = syscall.NewLazyDLL("kernel32.dll")
	queryConsoleMode        = k32.NewProc("GetConsoleMode")
	applyConsoleMode        = k32.NewProc("SetConsoleMode")
	vtOutputModeFlag uint32 = 0x0004
)

// vtReady records that this process already asked the console for VT support;
// the request is made at most once.
var vtReady bool

// enableVTProcessing turns on ANSI escape code support for a Windows console.
func enableVTProcessing() {
	if vtReady {
		return
	}
	vtReady = true

	out := os.Stdout.Fd()
	var mode uint32
	ok, _, _ := queryConsoleMode.Call(out, uintptr(unsafe.Pointer(&mode)))
	if ok == 0 {
		return
	}
	applyConsoleMode.Call(out, uintptr(mode|vtOutputModeFlag))
}
