//go:build !windows

package nudge

import (
	"os"

	"github.com/steveyegge/gastown/internal/pidalive"
)

func pollerProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return pidalive.Proc(proc)
}
