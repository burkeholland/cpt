//go:build windows

package main

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP       = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP             = kernel32.NewProc("SetConsoleCP")
	procGetConsoleMode           = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode           = kernel32.NewProc("SetConsoleMode")
	procGetStdHandle             = kernel32.NewProc("GetStdHandle")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

const (
	cpUTF8                        = 65001
	stdOutputHandle               = ^uintptr(0) - 11 + 1 // STD_OUTPUT_HANDLE = -11
	enableVirtualTerminalProcessing = 0x0004
)

type coord struct {
	x, y int16
}

type smallRect struct {
	left, top, right, bottom int16
}

type consoleScreenBufferInfo struct {
	size              coord
	cursorPosition    coord
	attributes        uint16
	window            smallRect
	maximumWindowSize coord
}

func init() {
	// Set console code pages to UTF-8 so box-drawing and emoji render correctly
	procSetConsoleOutputCP.Call(uintptr(cpUTF8))
	procSetConsoleCP.Call(uintptr(cpUTF8))

	// Enable virtual terminal processing for ANSI escape sequences
	handle, _, _ := procGetStdHandle.Call(stdOutputHandle)
	if handle != 0 && handle != ^uintptr(0) {
		var mode uint32
		r, _, _ := procGetConsoleMode.Call(handle, uintptr(unsafe.Pointer(&mode)))
		if r != 0 {
			procSetConsoleMode.Call(handle, uintptr(mode|enableVirtualTerminalProcessing))
		}
	}
}

func openTTYOut() (*os.File, error) {
	return os.OpenFile("CONOUT$", os.O_WRONLY, 0)
}

// consoleWindowWidth returns the visible window width of the console.
// On Windows, the buffer width can be larger than the visible window.
func consoleWindowWidth() int {
	handle, _, _ := procGetStdHandle.Call(stdOutputHandle)
	if handle == 0 || handle == ^uintptr(0) {
		return 0
	}
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(handle, uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0
	}
	return int(info.window.right - info.window.left + 1)
}
