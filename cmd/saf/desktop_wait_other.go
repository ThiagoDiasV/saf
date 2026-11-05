//go:build desktop && !windows

package main

import (
	"syscall"
	"time"
)

// waitForExit waits, up to limit, for the SAF that restarted into this one to close, so its
// window has let go before this one opens. Signal 0 only asks whether the process still exists.
func waitForExit(pid int, limit time.Duration) {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
	}
}
