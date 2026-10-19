package panel

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func (h *Handler) probe(ctx context.Context, spec ProjectSpec) (bool, string) {
	path := spec.ReadyProbe
	if path == "" {
		path = "/v1/models"
	}
	u, err := url.Parse(strings.TrimRight(spec.Upstream, "/") + path)
	if err != nil {
		return false, "上游地址无效"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, "无法创建探测请求"
	}
	// Console-only projects can have no bridge yet (for example Qoder before
	// OAuth/PAT login). Probe the console path in that state.
	if !portOpen(spec.Upstream) {
		if consoleOpen(spec) {
			return true, "控制台已启动，等待账号登录 / API Bridge"
		}
		return false, "端口未响应"
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false, "端口未响应"
	}
	defer resp.Body.Close()
	// 401/403 still prove that the HTTP service is listening.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return true, "HTTP " + strconv.Itoa(resp.StatusCode) + "（需鉴权）"
	}
	if resp.StatusCode >= 500 {
		return false, "HTTP " + strconv.Itoa(resp.StatusCode)
	}
	return true, "HTTP " + strconv.Itoa(resp.StatusCode)
}

func (h *Handler) findExternalPID(spec ProjectSpec) int {
	// Console and API ports can be separate processes/listeners (notably Qoder).
	// A live console still means the project has something for us to manage.
	if runtime.GOOS == "windows" {
		if pid := findWindowsPID(spec); pid > 0 {
			return pid
		}
	}
	// Never return a synthetic PID. The old fallback returned 1, which made
	// stop requests target an unrelated Windows process when a project was
	// started outside this gateway instance.
	return 0
}

func consoleOpen(spec ProjectSpec) bool {
	if spec.ConsolePort <= 0 {
		return false
	}
	return portOpen("http://127.0.0.1:" + strconv.Itoa(spec.ConsolePort))
}

func portOpen(upstream string) bool {
	u, err := url.Parse(upstream)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func findWindowsPID(spec ProjectSpec) int {
	// netstat is part of Windows and avoids relying on Windows PowerShell 5.1.
	// The command line for some launchers (notably OpenCode's `node index.js`)
	// does not contain the project directory, so listener ownership is the
	// reliable way to find the process after a gateway restart.
	out, err := exec.Command("netstat.exe", "-ano", "-p", "tcp").Output()
	if err != nil {
		return 0
	}
	ports := make([]int, 0, 2)
	if port := urlPort(spec.Upstream); port > 0 {
		ports = append(ports, port)
	}
	if spec.ConsolePort > 0 && (len(ports) == 0 || spec.ConsolePort != ports[0]) {
		ports = append(ports, spec.ConsolePort)
	}
	return listeningPID(out, ports...)
}

func urlPort(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	port, _ := strconv.Atoi(u.Port())
	return port
}

// listeningPID parses the stable table emitted by netstat.exe -ano. It checks
// ports in priority order so the API listener is preferred over a separate
// console listener when both belong to the same project.
func listeningPID(out []byte, ports ...int) int {
	wanted := make(map[int]bool, len(ports))
	for _, port := range ports {
		if port > 0 {
			wanted[port] = true
		}
	}
	found := make(map[int]int, len(wanted))
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		_, portText, err := net.SplitHostPort(fields[1])
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil || !wanted[port] {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err == nil && pid > 0 {
			found[port] = pid
		}
	}
	for _, port := range ports {
		if pid := found[port]; pid > 0 {
			return pid
		}
	}
	return 0
}
