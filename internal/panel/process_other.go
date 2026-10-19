//go:build !windows

package panel

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func setHidden(cmd *exec.Cmd) {}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func stopProcessTree(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}
