//go:build windows

package main

import "os"

func openTTYOut() (*os.File, error) {
	return os.OpenFile("CONOUT$", os.O_WRONLY, 0)
}
