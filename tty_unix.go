//go:build !windows

package main

import "os"

func openTTYOut() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_WRONLY, 0)
}

// consoleWindowWidth returns 0 on non-Windows platforms (not needed).
func consoleWindowWidth() int {
	return 0
}
