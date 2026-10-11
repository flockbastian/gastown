//go:build !windows

package cmd

import (
	"github.com/steveyegge/gastown/internal/pidalive"
)

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return pidalive.Alive(pid)
}
