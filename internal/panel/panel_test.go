package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"api-gateway/internal/config"
)

const testAdminPassword = "test-admin-password"

func testHandler(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("API_GATEWAY_ADMIN_PASSWORD", testAdminPassword)
	root := t.TempDir()
	h, err := New(root, []config.Project{
		{Name: "workbuddy", Upstream: "http://127.0.0.1:1", Native: []string{"chat"}, HealthPath: "/healthz"},
		{Name: "qoder", Upstream: "http://127.0.0.1:2", Native: []string{"chat", "responses", "anthropic"}, HealthPath: "/v1/models"},
		{Name: "opencode", Upstream: "http://127.0.0.1:3", Native: []string{"chat"}, HealthPath: "/health"},
		{Name: "trae", Upstream: "http://127.0.0.1:4", Native: []string{"chat"}, HealthPath: "/healthz"},
		{Name: "gcli", Upstream: "http://127.0.0.1:5", Native: []string{"chat", "anthropic"}, HealthPath: "/v1/models"},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func login(t *testing.T, client *http.Client, baseURL string) *http.Response {
	t.Helper()
	form := url.Values{"password": {testAdminPassword}, "next": {"/"}}
	resp, err := client.PostForm(baseURL+"/admin", form)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestPublicManagementRequiresLogin(t *testing.T) {
	h := testHandler(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := httptest.NewServer(h.PublicHandler(next))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/projects/workbuddy/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("public POST status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestAdminLoginAndManagementSession(t *testing.T) {
	h := testHandler(t)
	srv := httptest.NewServer(h.PublicHandler(http.NotFoundHandler()))
	defer srv.Close()
	jar := newTestJar()
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	bad, err := client.PostForm(srv.URL+"/admin", url.Values{"password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	defer bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want %d", bad.StatusCode, http.StatusUnauthorized)
	}

	ok := login(t, client, srv.URL)
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", ok.StatusCode, http.StatusSeeOther)
	}
	cookies := ok.Cookies()
	if len(cookies) != 1 || cookies[0].Name != adminSessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}

	projects, err := client.Get(srv.URL + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer projects.Body.Close()
	var body struct {
		Manage   bool          `json:"manage"`
		Projects []ProjectView `json:"projects"`
	}
	if err := json.NewDecoder(projects.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Manage || len(body.Projects) != 5 {
		t.Fatalf("logged-in projects response manage=%v len=%d", body.Manage, len(body.Projects))
	}
	for _, project := range body.Projects {
		if !strings.HasPrefix(project.ConsoleURL, "/console/") {
			t.Fatalf("project %s console URL = %q", project.Name, project.ConsoleURL)
		}
	}

	logout, err := client.Post(srv.URL+"/admin/logout", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer logout.Body.Close()
	if logout.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout status = %d", logout.StatusCode)
	}
	after, err := client.Get(srv.URL + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer after.Body.Close()
	var afterBody struct {
		Manage bool `json:"manage"`
	}
	if err := json.NewDecoder(after.Body).Decode(&afterBody); err != nil {
		t.Fatal(err)
	}
	if afterBody.Manage {
		t.Fatal("logout did not invalidate management session")
	}
}

func TestAdminLoginPageAndSafeNext(t *testing.T) {
	h := testHandler(t)
	srv := httptest.NewServer(h.PublicHandler(http.NotFoundHandler()))
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Get(srv.URL + "/admin?next=" + url.QueryEscape("//evil.example/x"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page status = %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	body := buf.String()
	for _, want := range []string{`id="app"`, `assets/`, `data-login="true"`, `data-next="/"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("login shell missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "evil.example") {
		t.Fatalf("login shell retained unsafe next path: %s", body)
	}
}

func TestAdminLoginFailureRendersVueShell(t *testing.T) {
	h := testHandler(t)
	srv := httptest.NewServer(h.PublicHandler(http.NotFoundHandler()))
	defer srv.Close()
	resp, err := http.PostForm(srv.URL+"/admin", url.Values{"password": {"wrong"}, "next": {"/docs"}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	body := buf.String()
	for _, want := range []string{`id="app"`, `assets/`, `data-login="true"`, `data-next="/docs"`, `data-login-error="密码错误。"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("login failure shell missing %q: %s", want, body)
		}
	}
}

func TestAdminPanelRequiresToken(t *testing.T) {
	h := testHandler(t)
	srv := httptest.NewServer(h.AdminHandler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/projects/workbuddy/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin POST without token status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestAdminPanelTokenStillWorks(t *testing.T) {
	h := testHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects", nil)
	req.Header.Set("X-Panel-Token", h.Token())
	h.AdminHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token request status = %d", rec.Code)
	}
}

func TestIndexUsesVueStaticShellWithoutIframe(t *testing.T) {
	h := testHandler(t)
	rec := httptest.NewRecorder()
	h.pageIndex(true, rec, httptest.NewRequest(http.MethodGet, "/?project=qoder", nil), false)
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`id="app"`, `assets/`, `data-default-project="workbuddy"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("index missing %q", want)
		}
	}
	if strings.Contains(strings.ToLower(body), "<iframe") || strings.Contains(body, "consoleHost") {
		t.Fatal("index still embeds a project console")
	}
}

func TestIndexExposesManagementTokenOnlyLocally(t *testing.T) {
	h := testHandler(t)
	local := httptest.NewRecorder()
	h.pageIndex(true, local, httptest.NewRequest(http.MethodGet, "/", nil), true)
	if !strings.Contains(local.Body.String(), `data-token="`+h.Token()+`"`) {
		t.Fatal("local index missing management token")
	}
	public := httptest.NewRecorder()
	h.pageIndex(true, public, httptest.NewRequest(http.MethodGet, "/", nil), false)
	if strings.Contains(public.Body.String(), h.Token()) {
		t.Fatal("public index exposes internal management token")
	}
}

func TestDefaultProjectDirectoriesStayInsideRepository(t *testing.T) {
	root := t.TempDir()
	specs := defaultSpecs(root)
	want := map[string]string{
		"workbuddy": filepath.Join(root, "upstreams", "workbuddy"),
		"qoder":     filepath.Join(root, "upstreams", "qoder"),
		"opencode":  filepath.Join(root, "upstreams", "opencode"),
		"trae":      filepath.Join(root, "upstreams", "trae"),
		"gcli":      filepath.Join(root, "upstreams", "gcli"),
	}
	if len(specs) != len(want) {
		t.Fatalf("spec count = %d, want %d", len(specs), len(want))
	}
	for _, spec := range specs {
		expected, ok := want[spec.Name]
		if !ok {
			t.Fatalf("unexpected project %q", spec.Name)
		}
		if spec.Directory != expected {
			t.Fatalf("%s directory = %q, want %q", spec.Name, spec.Directory, expected)
		}
	}
}

func TestOpenCodeStartsWithWebPanelEnabled(t *testing.T) {
	specs := defaultSpecs(t.TempDir())
	for _, spec := range specs {
		if spec.Name != "opencode" {
			continue
		}
		if !strings.Contains(strings.Join(spec.Env, "\n"), "OPENCODE_WEB_PANEL_ENABLED=true") {
			t.Fatalf("OpenCode env does not enable the web panel: %v", spec.Env)
		}
		return
	}
	t.Fatal("OpenCode spec not found")
}
func TestTraeStartsInWebLoginMode(t *testing.T) {
	specs := defaultSpecs(t.TempDir())
	for _, spec := range specs {
		if spec.Name != "trae" {
			continue
		}
		env := strings.Join(spec.Env, "\n")
		for _, want := range []string{"TRAE_AUTH_SOURCE=web-login", "WEB_LOGIN_LISTENER_PORT=8765"} {
			if !strings.Contains(env, want) {
				t.Fatalf("Trae env missing %q: %v", want, spec.Env)
			}
		}
		if filepath.Base(spec.Command) != "pwsh.exe" && filepath.Base(spec.Command) != "pwsh" {
			t.Fatalf("Trae command = %q, want PowerShell 7 launcher", spec.Command)
		}
		if spec.ConsoleURL != "/console/trae/#accounts" {
			t.Fatalf("Trae console URL = %q, want accounts landing", spec.ConsoleURL)
		}
		if !strings.Contains(spec.LoginHint, "账号与签到") {
			t.Fatalf("Trae login hint = %q", spec.LoginHint)
		}
		if !strings.Contains(strings.Join(spec.Args, " "), "start-trae.ps1") {
			t.Fatalf("Trae args do not use start-trae.ps1: %v", spec.Args)
		}
		return
	}
	t.Fatal("Trae spec not found")
}

func TestDocsPageUsesVueStaticShell(t *testing.T) {
	t.Setenv("GATEWAY_PUBLIC_BASE_URL", "http://gateway.test")
	h := testHandler(t)
	local := httptest.NewRecorder()
	h.pageDocs(true, local, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7862/docs", nil), true)
	if local.Code != http.StatusOK {
		t.Fatalf("local docs status = %d, want %d: %s", local.Code, http.StatusOK, local.Body.String())
	}
	for _, want := range []string{`id="app"`, `assets/`, `data-api-base="http://127.0.0.1:7860"`} {
		if !strings.Contains(local.Body.String(), want) {
			t.Fatalf("local docs page missing %q", want)
		}
	}

	public := httptest.NewRecorder()
	h.pageDocs(true, public, httptest.NewRequest(http.MethodGet, "http://gateway.test/docs", nil), false)
	if public.Code != http.StatusOK {
		t.Fatalf("public docs status = %d, want %d: %s", public.Code, http.StatusOK, public.Body.String())
	}
	for _, want := range []string{`id="app"`, `assets/`, `data-api-base="http://gateway.test"`} {
		if !strings.Contains(public.Body.String(), want) {
			t.Fatalf("public docs page missing %q", want)
		}
	}
	for _, body := range []string{local.Body.String(), public.Body.String()} {
		if strings.Contains(strings.ToLower(body), "<iframe") {
			t.Fatal("docs still contains iframe")
		}
	}
}

func TestPanelEmbeddedAssetsAreServed(t *testing.T) {
	h := testHandler(t)
	entries, err := fs.ReadDir(staticFiles, "static/assets")
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".js") && !strings.HasSuffix(entry.Name(), ".css")) {
			continue
		}
		checked++
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/assets/"+entry.Name(), nil)
		h.AdminHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("asset %s status = %d", entry.Name(), rec.Code)
		}
	}
	if checked == 0 {
		t.Fatal("no embedded JavaScript or CSS assets found")
	}
}

func TestProjectViewsExposeConsoleWhenManageable(t *testing.T) {
	h := testHandler(t)
	admin := h.views(context.Background(), true, "http://127.0.0.1:7860")
	for _, p := range admin {
		if !strings.HasPrefix(p.ConsoleURL, "/console/") {
			t.Fatalf("project %s console URL = %q", p.Name, p.ConsoleURL)
		}
	}
}

func TestProjectViewsHideManagementWhenAnonymous(t *testing.T) {
	h := testHandler(t)
	public := h.views(context.Background(), false, "http://127.0.0.1:7860")
	admin := h.views(context.Background(), true, "http://127.0.0.1:7860")
	if len(public) != 5 || len(admin) != 5 {
		t.Fatalf("views lengths = %d/%d, want 5/5", len(public), len(admin))
	}
	for _, p := range public {
		if p.Manage || p.ConsoleDirect != "" || p.ConsoleURL != "" || p.ConsolePort != 0 {
			t.Fatalf("anonymous view for %s exposes management details", p.Name)
		}
	}
	for _, p := range admin {
		if !p.Manage || p.ConsoleURL == "" {
			t.Fatalf("admin view for %s lacks management console", p.Name)
		}
	}
}

func TestProjectAPIUsesConfiguredOrder(t *testing.T) {
	h := testHandler(t)
	rec := httptest.NewRecorder()
	h.apiProjects(false, false)(rec, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("projects status = %d", rec.Code)
	}
	var body struct {
		Projects []ProjectView `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []string{"workbuddy", "qoder", "opencode", "trae", "gcli"}
	if len(body.Projects) != len(want) {
		t.Fatalf("projects length = %d, want %d", len(body.Projects), len(want))
	}
	for i, name := range want {
		if body.Projects[i].Name != name {
			t.Fatalf("projects[%d] = %q, want %q", i, body.Projects[i].Name, name)
		}
	}
}

func TestProjectAPIDoesNotExposeLauncherEnvironment(t *testing.T) {
	h := testHandler(t)
	rec := httptest.NewRecorder()
	h.apiProjects(true, true)(rec, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("projects status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "WB2A_API_KEY") || strings.Contains(rec.Body.String(), testAdminPassword) {
		t.Fatal("project API exposed launcher environment or a credential")
	}
}

func TestNewCreatesLogDirectory(t *testing.T) {
	h := testHandler(t)
	if _, err := os.Stat(h.logDir); err != nil {
		t.Fatalf("log dir: %v", err)
	}
	if filepath.Base(h.logDir) != "panel-logs" {
		t.Fatalf("log dir = %q", filepath.Base(h.logDir))
	}
}

func TestNewRequiresAdminPassword(t *testing.T) {
	t.Setenv("API_GATEWAY_ADMIN_PASSWORD", "")
	t.Setenv("PANEL_PASSWORD", "")
	_, err := New(t.TempDir(), []config.Project{{Name: "workbuddy", Upstream: "http://127.0.0.1:1"}}, true)
	if err == nil {
		t.Fatal("New succeeded without admin password")
	}
}

func TestListeningPIDUsesPreferredPort(t *testing.T) {
	out := []byte("  TCP    127.0.0.1:8963         0.0.0.0:0              LISTENING       111\n  TCP    127.0.0.1:3588         0.0.0.0:0              LISTENING       222\n  TCP    127.0.0.1:10010        0.0.0.0:0              LISTENING       333\n")
	if got := listeningPID(out, 10010, 3588); got != 333 {
		t.Fatalf("listeningPID = %d, want 333", got)
	}
	if got := listeningPID(out, 8963, 3588); got != 111 {
		t.Fatalf("listeningPID preferred API port = %d, want 111", got)
	}
	if got := listeningPID(out, 9999); got != 0 {
		t.Fatalf("listeningPID missing port = %d, want 0", got)
	}
}

func TestFindExternalPIDNeverReturnsSyntheticPID(t *testing.T) {
	spec := ProjectSpec{Upstream: "http://127.0.0.1:1"}
	if got := (&Handler{}).findExternalPID(spec); got != 0 {
		t.Fatalf("findExternalPID = %d, want 0", got)
	}
}

func TestPublicConsoleRedirectsToLogin(t *testing.T) {
	h := testHandler(t)
	srv := httptest.NewServer(h.PublicHandler(http.NotFoundHandler()))
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/console/qoder/?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("console redirect status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); !strings.HasPrefix(got, "/admin?next=%2Fconsole%2Fqoder%2F") {
		t.Fatalf("redirect location = %q", got)
	}
}

func newTestJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}
