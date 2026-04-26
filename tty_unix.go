//go:build !windows

package main

import "os"

func openTTYOut() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_WRONLY, 0)
}
