//go:build !windows

package main

import (
	"fmt"
	"os"
)

// attachConsole is for Windows' GUI build only (console_windows.go).
func attachConsole() {}

// showError prints why SAF didn't open; on a Mac, run SAF.app/Contents/MacOS/SAF in Terminal to see it.
func showError(err error) {
	fmt.Fprintln(os.Stderr, "O SAF não abriu:", err)
}
