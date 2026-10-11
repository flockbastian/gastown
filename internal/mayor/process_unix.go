//go:build !windows

package mayor

import (
	"os"

	"github.com/steveyegge/gastown/internal/pidalive"
)

func acpProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	return pidalive.Proc(process)
}
