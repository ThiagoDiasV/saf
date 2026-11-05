//go:build desktop

package main

import (
	"os"
	"time"
)

// waitForExit waits, up to limit, for the SAF that restarted into this one to close, so its
// window has let go before this one opens.
func waitForExit(pid int, limit time.Duration) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return // already closed
	}
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}
