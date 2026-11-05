//go:build desktop && !windows

package main

import "time"

// waitForExit is for restarting into an update, which only the Windows build does.
func waitForExit(int, time.Duration) {}
