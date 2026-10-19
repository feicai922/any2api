package panel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"api-gateway/internal/config"
)

type ProtocolInfo struct {
	Name   string `json:"name"`
	Native bool   `json:"native"`
}

type ProjectSpec struct {
	Name        string         `json:"name"`
	Directory   string         `json:"directory"`
	PublicBase  string         `json:"public_base"`
	Upstream    string         `json:"upstream"`
	Description string         `json:"description"`
	Command     string         `json:"command"`
	Args        []string       `json:"args,omitempty"`
	Env         []string       `json:"-"`
	ReadyProbe  string         `json:"ready_probe,omitempty"`
	ConsoleURL  string         `json:"console_url,omitempty"`
	ConsolePort int            `json:"console_port,omitempty"`
	Protocols   []ProtocolInfo `json:"protocols"`
	InstallHint string         `json:"install_hint,omitempty"`
	LoginHint   string         `json:"login_hint,omitempty"`
}

type ProjectView struct {
	ProjectSpec
	Running       bool   `json:"running"`
	Ready         bool   `json:"ready"`
	PID           int    `json:"pid,omitempty"`
	Detail        string `json:"detail"`
	LastError     string `json:"last_error,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	Installed     bool   `json:"installed"`
	Manage        bool   `json:"manage"`
	ConsoleDirect string `json:"console_direct"`
}

type processState struct {
	cmd       *exec.Cmd
	startedAt time.Time
	lastError string
	exited    bool
}

type processSnapshot struct {
	pid       int
	startedAt time.Time
	lastError string
	exited    bool
}

type StartResult struct {
	Project string `json:"project"`
	Action  string `json:"action"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type Handler struct {
	specs         []ProjectSpec
	byName        map[string]ProjectSpec
	root          string
	token         string
	password      string
	mu            sync.Mutex
	state         map[string]*processState
	client        *http.Client
	logDir        string
	publicURL     string
	localURL      string
	localAdminURL string

	sessionMu sync.Mutex
	sessions  map[string]adminSession

	loginMu           sync.Mutex
	loginFailures     int
	loginBlockedUntil time.Time
}

type adminSession struct {
	expires time.Time
}

const (
	adminSessionCookie = "api_gateway_admin"
	adminSessionTTL    = 12 * time.Hour
	maxLoginBodyBytes  = 4 << 10
)

func New(root string, projects []config.Project, manage bool) (*Handler, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if !manage {
		return nil, errors.New("panel management must be enabled for the shared handler")
	}
	all := defaultSpecs(root)
	configured := make(map[string]config.Project, len(projects))
	for _, p := range projects {
		configured[p.Name] = p
	}
	specByName := make(map[string]ProjectSpec, len(all))
	for _, s := range all {
		p, ok := configured[s.Name]
		if !ok {
			continue
		}
		native := make(map[string]bool, len(p.Native))
		for _, name := range p.Native {
			native[strings.ToLower(strings.TrimSpace(name))] = true
		}
		s.Protocols = []ProtocolInfo{
			{Name: "chat", Native: true},
			{Name: "responses", Native: native["responses"]},
			{Name: "anthropic", Native: native["anthropic"]},
		}
		s.Directory = publicProjectPath(s.Directory, root)
		specByName[s.Name] = s
	}
	for name := range configured {
		if _, ok := specByName[name]; !ok {
			return nil, fmt.Errorf("panel has no launcher spec for project %q", name)
		}
	}
	ordered := make([]ProjectSpec, 0, len(projects))
	for _, p := range projects {
		if s, ok := specByName[p.Name]; ok {
			ordered = append(ordered, s)
		}
	}
	logDir := filepath.Join(root, "work", "panel-logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create panel log directory: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate panel token: %w", err)
	}
	password := strings.TrimSpace(os.Getenv("API_GATEWAY_ADMIN_PASSWORD"))
	if password == "" {
		password = strings.TrimSpace(os.Getenv("PANEL_PASSWORD"))
	}
	if password == "" {
		return nil, errors.New("API_GATEWAY_ADMIN_PASSWORD is required for remote administration")
	}
	return &Handler{
		specs:         ordered,
		byName:        specByName,
		root:          root,
		token:         hex.EncodeToString(tokenBytes),
		password:      password,
		state:         make(map[string]*processState),
		client:        &http.Client{Timeout: 2500 * time.Millisecond},
		logDir:        logDir,
		publicURL:     configuredBaseURL("GATEWAY_PUBLIC_BASE_URL", ""),
		localURL:      configuredBaseURL("GATEWAY_LOCAL_API_URL", "http://127.0.0.1:7860"),
		localAdminURL: configuredBaseURL("GATEWAY_LOCAL_ADMIN_URL", "http://127.0.0.1:7862"),
		sessions:      make(map[string]adminSession),
	}, nil
}

func (h *Handler) Token() string { return h.token }

func (h *Handler) PublicHandler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin", h.adminHandler)
	mux.HandleFunc("/admin/logout", h.adminLogout)
	mux.HandleFunc("/api/projects", h.publicProjects)
	mux.HandleFunc("/api/projects/", h.publicProjectAction)
	mux.HandleFunc("/docs", h.publicDocs)
	mux.HandleFunc("/console/", h.publicConsole)
	mux.Handle("/assets/", h.staticHandler())
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			h.publicIndex(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
	return mux
}

func (h *Handler) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/projects", h.apiProjects(true, true))
	mux.HandleFunc("/api/projects/", h.apiProjectAction(true))
	mux.HandleFunc("/docs", func(w http.ResponseWriter, r *http.Request) { h.pageDocs(true, w, r, true) })
	mux.HandleFunc("/console/", h.consoleHandler)
	mux.HandleFunc("/admin/logout", h.adminLogout)
	mux.Handle("/assets/", h.staticHandler())
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		h.pageIndex(true, w, r, true)
	})
	return mux
}

func (h *Handler) publicProjects(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "administrator login required"})
		return
	}
	h.apiProjects(true, false)(w, r)
}

func (h *Handler) publicIndex(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Redirect(w, r, "/admin?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	h.pageIndex(true, w, r, false)
}

func (h *Handler) publicDocs(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Redirect(w, r, "/admin?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	h.pageDocs(true, w, r, false)
}

func (h *Handler) publicProjectAction(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "administrator login required"})
		return
	}
	h.apiProjectAction(true)(w, r)
}

func (h *Handler) publicConsole(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		next := url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, "/admin?next="+next, http.StatusSeeOther)
		return
	}
	h.consoleHandler(w, r)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) pageIndex(_ bool, w http.ResponseWriter, r *http.Request, local bool) {
	h.renderPanel(w, r, local)
}

func (h *Handler) pageDocs(_ bool, w http.ResponseWriter, r *http.Request, local bool) {
	h.renderPanel(w, r, local)
}

func (h *Handler) apiProjects(canManage, local bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "GET required"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"projects": h.views(r.Context(), canManage, h.requestBaseURL(r, local)),
			"manage":   canManage,
		})
	}
}

func (h *Handler) apiProjectAction(canManage bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !canManage {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "management is disabled on this listener"})
			return
		}
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "POST required"})
			return
		}
		if !h.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "administrator login required"})
			return
		}
		rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/projects/"), "/")
		if rest == "start-all" {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "results": h.StartAll(r.Context())})
			return
		}
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		name, action := parts[0], parts[1]
		if _, ok := h.byName[name]; !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown project"})
			return
		}
		switch action {
		case "start":
			if err := h.start(name); err != nil {
				writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
				return
			}
		case "stop":
			if err := h.stop(name); err != nil {
				writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
				return
			}
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown action"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "project": name, "action": action})
	}
}

func (h *Handler) authorized(r *http.Request) bool {
	return h.validSession(r) || h.validToken(r)
}

func (h *Handler) views(ctx context.Context, canManage bool, baseURL string) []ProjectView {
	h.mu.Lock()
	snapshots := make(map[string]processSnapshot, len(h.state))
	for name, st := range h.state {
		if st == nil || st.cmd == nil || st.cmd.Process == nil {
			continue
		}
		snapshots[name] = processSnapshot{
			pid:       st.cmd.Process.Pid,
			startedAt: st.startedAt,
			lastError: st.lastError,
			exited:    st.exited,
		}
	}
	h.mu.Unlock()

	out := make([]ProjectView, 0, len(h.specs))
	for _, spec := range h.specs {
		if baseURL != "" {
			spec.PublicBase = strings.TrimRight(baseURL, "/") + "/" + spec.Name + "/v1"
		}
		v := ProjectView{ProjectSpec: spec, Installed: h.installed(spec), Manage: canManage}
		if canManage {
			v.ConsoleDirect = directConsoleURL(spec.Name)
		} else {
			v.ConsoleURL = ""
			v.ConsolePort = 0
		}
		if st, ok := snapshots[spec.Name]; ok {
			v.PID = st.pid
			v.Running = !st.exited && processAlive(st.pid)
			v.LastError = st.lastError
			if !st.startedAt.IsZero() {
				v.StartedAt = st.startedAt.Format(time.RFC3339)
			}
		}
		if !v.Running {
			if pid := h.findExternalPID(spec); pid > 0 {
				v.Running = true
				v.PID = pid
				v.Detail = "端口已被项目进程监听"
			}
		}
		if v.Running {
			v.Ready, v.Detail = h.probe(ctx, spec)
		} else if !v.Installed {
			v.Detail = "依赖未安装"
		} else {
			v.Detail = "未运行"
		}
		out = append(out, v)
	}
	return out
}

func (h *Handler) installed(spec ProjectSpec) bool {
	if _, err := os.Stat(spec.Directory); err != nil {
		return false
	}
	if _, err := os.Stat(spec.Command); err != nil {
		if _, lookErr := exec.LookPath(spec.Command); lookErr != nil {
			return false
		}
	}
	if spec.Name == "trae" {
		launcher := filepath.Join(h.root, "scripts", "start-trae.ps1")
		if _, err := os.Stat(launcher); err != nil {
			return false
		}
	}
	switch spec.Name {
	case "workbuddy":
		_, exeErr := os.Stat(spec.Command)
		_, configErr := os.Stat(filepath.Join(spec.Directory, "config.json"))
		return exeErr == nil && configErr == nil
	case "qoder":
		_, err := os.Stat(spec.Command)
		return err == nil
	case "opencode":
		cli := opencodeCommand()
		if _, err := os.Stat(cli); err != nil {
			if _, err := exec.LookPath("opencode"); err != nil {
				return false
			}
		}
		return true
	case "trae":
		return pythonPackagesReady(spec.Directory, "fastapi", "uvicorn")
	case "gcli":
		return pythonPackagesReady(spec.Directory, "hypercorn", "fastapi")
	default:
		return true
	}
}

func (h *Handler) StartAll(ctx context.Context) []StartResult {
	results := make([]StartResult, 0, len(h.specs))
	for _, spec := range h.specs {
		select {
		case <-ctx.Done():
			results = append(results, StartResult{Project: spec.Name, Action: "start", Message: "启动流程已取消"})
			continue
		default:
		}
		err := h.start(spec.Name)
		if err == nil {
			results = append(results, StartResult{Project: spec.Name, Action: "started", OK: true, Message: "已启动"})
			continue
		}
		if strings.Contains(strings.ToLower(err.Error()), "already running") {
			results = append(results, StartResult{Project: spec.Name, Action: "skipped", OK: true, Message: "已在运行，跳过"})
			continue
		}
		results = append(results, StartResult{Project: spec.Name, Action: "failed", Message: err.Error()})
	}
	return results
}

func (h *Handler) start(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	spec, ok := h.byName[name]
	if !ok {
		return errors.New("unknown project")
	}
	if st := h.state[name]; st != nil && st.cmd != nil && st.cmd.Process != nil && !st.exited && processAlive(st.cmd.Process.Pid) {
		return fmt.Errorf("%s is already running (pid %d)", name, st.cmd.Process.Pid)
	}
	if pid := h.findExternalPID(spec); pid > 0 {
		return fmt.Errorf("%s is already running (pid %d)", name, pid)
	}
	if _, err := os.Stat(spec.Directory); err != nil {
		return fmt.Errorf("project directory not found: %s", spec.Directory)
	}
	if !h.installed(spec) {
		return fmt.Errorf("%s dependencies are not ready: %s", name, spec.InstallHint)
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Directory
	cmd.Env = mergeEnv(os.Environ(), spec.Env)
	setHidden(cmd)
	logPath := filepath.Join(h.logDir, name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start: %w", err)
	}
	st := &processState{cmd: cmd, startedAt: time.Now()}
	h.state[name] = st
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		h.mu.Lock()
		defer h.mu.Unlock()
		st.exited = true
		if err != nil {
			st.lastError = err.Error()
		}
	}()
	return nil
}

func (h *Handler) stop(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	spec, ok := h.byName[name]
	if !ok {
		return errors.New("unknown project")
	}
	pid := 0
	if st := h.state[name]; st != nil && st.cmd != nil && st.cmd.Process != nil && !st.exited && processAlive(st.cmd.Process.Pid) {
		pid = st.cmd.Process.Pid
	} else {
		pid = h.findExternalPID(spec)
	}
	if pid <= 0 {
		return errors.New("project is not running")
	}
	if spec.Name == "trae" && runtime.GOOS == "windows" {
		if launcherPID := findTraeLauncherPID(h.root); launcherPID > 0 {
			pid = launcherPID
		}
	}
	if err := stopProcessTree(pid); err != nil {
		return fmt.Errorf("stop: %w", err)
	}
	return nil
}

func defaultSpecs(root string) []ProjectSpec {
	workspace := filepath.Dir(root)
	workbuddyDir := projectDirectory("WORKBUDDY_DIR", filepath.Join(workspace, "workbuddy2api-panel"))
	qoderDir := projectDirectory("QODER_DIR", filepath.Join(workspace, "qoder2api"))
	opencodeDir := projectDirectory("OPENCODE_DIR", filepath.Join(workspace, "opencode2api"))
	traeDir := projectDirectory("TRAE_DIR", filepath.Join(workspace, "trae2api"))
	gcliDir := projectDirectory("GCLI_DIR", filepath.Join(workspace, "gcli2api"))
	gcliPython := filepath.Join(gcliDir, ".venv", "Scripts", "python.exe")
	if runtime.GOOS != "windows" {
		gcliPython = filepath.Join(gcliDir, ".venv", "bin", "python")
	}
	secrets := runtimeSecrets()
	projectEnv := func(name string, extra ...string) []string {
		env := append([]string{}, extra...)
		switch name {
		case "workbuddy":
			env = append(env, "WB2A_API_KEY="+secrets)
		case "qoder":
			env = append(env, "QODER2API_CONSOLE_PASSWORD="+secrets)
		case "opencode":
			env = append(env, "API_KEY="+secrets)
		case "trae":
			env = append(env, "RELAY_API_KEYS="+secrets)
		case "gcli":
			env = append(env,
				"API_PASSWORD="+secrets,
				"PANEL_PASSWORD="+secrets,
				"PASSWORD="+secrets,
			)
		}
		return env
	}
	return []ProjectSpec{
		{
			Name:        "workbuddy",
			Directory:   workbuddyDir,
			PublicBase:  "/workbuddy/v1",
			Upstream:    "http://127.0.0.1:7863",
			Description: "WorkBuddy 反代，支持 Chat Completions，并可由网关转换 Responses / Anthropic。",
			Command:     filepath.Join(workbuddyDir, "wb2api.exe"),
			Args:        []string{"-config", filepath.Join(workbuddyDir, "config.json")},
			Env:         projectEnv("workbuddy"),
			ReadyProbe:  "/healthz",
			ConsoleURL:  "/console/workbuddy/",
			ConsolePort: 7863,
			InstallHint: "需要 wb2api.exe 与 config.json。",
			LoginHint:   "输入 WorkBuddy 面板要求的 API Key 进入；需要新账号时在面板内点击“添加账号”完成 OAuth。",
		},
		{
			Name:        "qoder",
			Directory:   qoderDir,
			PublicBase:  "/qoder/v1",
			Upstream:    "http://127.0.0.1:8963",
			Description: "Qoder 反代，支持 OpenAI Chat、Responses 与 Anthropic Messages。",
			Command:     filepath.Join(qoderDir, "bin", "qoder2api.exe"),
			Args:        []string{"--web-port=3588", "--bridge-port=8963", "--bind=127.0.0.1"},
			Env:         projectEnv("qoder"),
			ReadyProbe:  "/v1/models",
			ConsoleURL:  "/console/qoder/",
			ConsolePort: 3588,
			InstallHint: "需要 Go 工具链，且 bin\\qoder2api.exe 必须已构建。",
			LoginHint:   "先输入 3588 控制台密码进入；再通过 OAuth 或 PAT 添加账号。只有 Bridge 启动后 /qoder/v1 才可用。",
		},
		{
			Name:        "opencode",
			Directory:   opencodeDir,
			PublicBase:  "/opencode/v1",
			Upstream:    "http://127.0.0.1:10010",
			Description: "OpenCode 反代，需要 Node 依赖与 OpenCode CLI。",
			Command:     "node",
			Args:        []string{"index.js"},
			Env: projectEnv("opencode",
				"OPENCODE_PROXY_PORT=10010",
				"BIND_HOST=127.0.0.1",
				"OPENCODE_PATH="+opencodeCommand(),
			),
			ReadyProbe:  "/health",
			ConsoleURL:  "/console/opencode/",
			InstallHint: "在 opencode2api 目录执行 npm install，并安装 opencode CLI。",
			LoginHint:   "通常可直接使用；控制台若要求鉴权，请填写项目代理 API Key，凭据仅保存在该页面本地。",
		},
		{
			Name:        "trae",
			Directory:   traeDir,
			PublicBase:  "/trae/v1",
			Upstream:    "http://127.0.0.1:8000",
			Description: "Trae 反代，包含 OpenAI、Responses 与 Anthropic 兼容接口。",
			Command:     powerShellCommand(),
			Args: []string{
				"-NoProfile",
				"-NonInteractive",
				"-ExecutionPolicy", "Bypass",
				"-File", filepath.Join(root, "scripts", "start-trae.ps1"),
			},
			Env: projectEnv("trae",
				"TRAE_AUTH_SOURCE=web-login",
				"TRAE_TRUST_PROXY_HEADERS=1",
				"WEB_LOGIN_LISTENER_PORT=8765",
			),
			ReadyProbe:  "/healthz",
			ConsoleURL:  "/console/trae/#accounts",
			ConsolePort: 8765,
			InstallHint: "需要 Python 虚拟环境、requirements.txt 依赖以及 PowerShell 7。",
			LoginHint:   "打开后进入“账号与签到”，点击“使用 Trae 网页授权登录”；本机 127.0.0.1:8765 授权助手必须在线，浏览器需运行在同一台 Windows 主机。",
		},
		{
			Name:        "gcli",
			Directory:   gcliDir,
			PublicBase:  "/gcli/v1",
			Upstream:    "http://127.0.0.1:7861",
			Description: "GCLI 反代，支持 OpenAI Chat 与 Anthropic Messages。",
			Command:     gcliPython,
			Args:        []string{"web.py"},
			Env: projectEnv("gcli",
				"HOST=127.0.0.1",
				"PORT=7861",
			),
			ReadyProbe:  "/v1/models",
			ConsoleURL:  "/console/gcli/",
			InstallHint: "需要 Python 虚拟环境和 requirements.txt 依赖。",
			LoginHint:   "输入 GCLI 控制面板访问密码。API Key 与面板密码可能不同；401 表示凭据错误或尚未登录。",
		},
	}
}

func publicProjectPath(path, root string) string {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if rel, err := filepath.Rel(root, path); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	if strings.TrimSpace(os.Getenv("GATEWAY_SHOW_ABSOLUTE_PATHS")) == "1" {
		return path
	}
	return filepath.Base(path)
}
func projectDirectory(envName, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		if abs, err := filepath.Abs(value); err == nil {
			return abs
		}
		return value
	}
	return filepath.Clean(fallback)
}

func configuredBaseURL(envName, fallback string) string {
	value := strings.TrimRight(strings.TrimSpace(os.Getenv(envName)), "/")
	if value == "" {
		value = strings.TrimRight(fallback, "/")
	}
	if value == "" {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return strings.TrimRight(fallback, "/")
	}
	return strings.TrimRight(value, "/")
}

func (h *Handler) requestBaseURL(r *http.Request, local bool) string {
	if local {
		if h.localURL != "" {
			return h.localURL
		}
		return "http://" + r.Host
	}
	if h.publicURL != "" {
		return h.publicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	if host := strings.TrimSpace(r.Host); host != "" {
		return scheme + "://" + host
	}
	return ""
}

func runtimeSecrets() string {
	for _, name := range []string{"API_GATEWAY_ADMIN_PASSWORD", "PANEL_PASSWORD"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func powerShellCommand() string {
	if runtime.GOOS == "windows" {
		candidates := []string{
			`C:\Program Files\WindowsApps\Microsoft.PowerShell_7.6.6.0_x64__8wekyb3d8bbwe\pwsh.exe`,
		}
		if pattern := filepath.Join(os.Getenv("ProgramFiles"), "WindowsApps", "Microsoft.PowerShell_*", "pwsh.exe"); pattern != "" {
			if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
				candidates = append(candidates, matches...)
			}
		}
		if path, err := exec.LookPath("pwsh.exe"); err == nil {
			candidates = append(candidates, path)
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
		return "pwsh.exe"
	}
	return "pwsh"
}

func opencodeCommand() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("APPDATA"), "npm", "opencode.cmd")
	}
	return "opencode"
}

func pythonPackagesReady(dir string, names ...string) bool {
	pythonPath := filepath.Join(dir, ".venv", "Scripts", "python.exe")
	if runtime.GOOS != "windows" {
		pythonPath = filepath.Join(dir, ".venv", "bin", "python")
	}
	if _, err := os.Stat(pythonPath); err != nil {
		return false
	}
	for _, name := range names {
		patterns := []string{
			filepath.Join(dir, ".venv", "Lib", "site-packages", name),
			filepath.Join(dir, ".venv", "lib", "python*", "site-packages", name),
		}
		found := false
		for _, pattern := range patterns {
			if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func mergeEnv(base, extra []string) []string {
	out := append([]string{}, base...)
	for _, kv := range extra {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		kept := out[:0]
		for _, item := range out {
			if !strings.HasPrefix(item, key+"=") {
				kept = append(kept, item)
			}
		}
		out = append(kept, kv)
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func directConsoleURL(project string) string {
	if target, ok := consoleTargets[project]; ok {
		return strings.TrimRight(target.Base, "/") + target.Landing
	}
	return ""
}
