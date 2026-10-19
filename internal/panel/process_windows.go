//go:build windows

package panel

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func setHidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist.exe", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	// CSV rows look like: "image.exe","1234","Console","1","12,345 K"
	return strings.Contains(string(out), `,"`+strconv.Itoa(pid)+`",`)
}

func stopProcessTree(pid int) error {
	return exec.Command("taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

type windowsProcess struct {
	ProcessID   int    `json:"ProcessId"`
	CommandLine string `json:"CommandLine"`
}

// findTraeLauncherPID locates the PowerShell launcher for Trae. Stopping it
// with taskkill /T guarantees that the uvicorn child and the 8765 web-login
// helper are terminated together, even after a gateway restart.
func findTraeLauncherPID(root string) int {
	script, err := filepath.Abs(filepath.Join(root, "scripts", "start-trae.ps1"))
	if err != nil {
		return 0
	}
	script = strings.ToLower(script)

	command := `$items = Get-CimInstance Win32_Process -Filter "Name='pwsh.exe'" | Select-Object ProcessId,CommandLine; @($items) | ConvertTo-Json -Compress`
	out, err := exec.Command(
		powerShellCommand(),
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		command,
	).Output()
	if err != nil {
		return 0
	}

	var processes []windowsProcess
	if err := json.Unmarshal(out, &processes); err != nil {
		var single windowsProcess
		if err := json.Unmarshal(out, &single); err != nil {
			return 0
		}
		processes = []windowsProcess{single}
	}
	for _, process := range processes {
		if process.ProcessID > 0 && strings.Contains(strings.ToLower(process.CommandLine), script) {
			return process.ProcessID
		}
	}
	return 0
}
