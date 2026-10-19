package panel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func withConsoleTarget(t *testing.T, name string, target consoleTarget) {
	t.Helper()
	old, existed := consoleTargets[name]
	consoleTargets[name] = target
	t.Cleanup(func() {
		if existed {
			consoleTargets[name] = old
		} else {
			delete(consoleTargets, name)
		}
	})
}

func TestConsoleRoutesAreAdminOnly(t *testing.T) {
	h := testHandler(t)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(h.PublicHandler(next))
	defer srv.Close()

	// The public API listener has no management routes. A control request is
	// still rejected by the action handler rather than reaching any console.
	resp, err := http.Post(srv.URL+"/api/projects/qoder/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("public management status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestPublicConsoleAllowsLoggedInAdmin(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	withConsoleTarget(t, "qoder", consoleTarget{Base: upstream.URL, Landing: "/"})

	h := testHandler(t)
	srv := httptest.NewServer(h.PublicHandler(http.NotFoundHandler()))
	defer srv.Close()
	jar := newTestJar()
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	loginResp := login(t, client, srv.URL)
	_ = loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login status = %d", loginResp.StatusCode)
	}
	resp, err := client.Get(srv.URL + "/console/qoder/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || gotPath != "/" {
		t.Fatalf("logged-in console status=%d path=%q", resp.StatusCode, gotPath)
	}
}

func TestConsoleProxyLandingAndPath(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	withConsoleTarget(t, "qoder", consoleTarget{Base: upstream.URL, Landing: "/"})

	h := testHandler(t)
	srv := httptest.NewServer(h.AdminHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/console/qoder/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("console status = %d", resp.StatusCode)
	}
	if gotPath != "/" {
		t.Fatalf("upstream path = %q, want /", gotPath)
	}
}

func TestConsoleProxyMapsWorkBuddyPanelPrefix(t *testing.T) {
	paths := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	withConsoleTarget(t, "workbuddy", consoleTarget{Base: upstream.URL, Landing: "/panel/", Prefix: "/panel"})

	h := testHandler(t)
	srv := httptest.NewServer(h.AdminHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/console/workbuddy/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("console asset status = %d", resp.StatusCode)
	}
	if got := <-paths; got != "/panel/app.js" {
		t.Fatalf("upstream asset path = %q, want /panel/app.js", got)
	}
}

func TestConsoleProxyDoesNotDuplicatePanelPrefix(t *testing.T) {
	tests := []struct {
		name        string
		project     string
		target      consoleTarget
		requestPath string
		wantPath    string
	}{
		{
			name:        "workbuddy API",
			project:     "workbuddy",
			target:      consoleTarget{Landing: "/panel/", Prefix: "/panel"},
			requestPath: "/console/workbuddy/panel/api/version",
			wantPath:    "/panel/api/version",
		},
		{
			name:        "opencode data",
			project:     "opencode",
			target:      consoleTarget{Landing: "/panel", Prefix: "/panel"},
			requestPath: "/console/opencode/panel/data.json",
			wantPath:    "/panel/data.json",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := make(chan string, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "ok")
			}))
			defer upstream.Close()
			tt.target.Base = upstream.URL
			withConsoleTarget(t, tt.project, tt.target)

			h := testHandler(t)
			srv := httptest.NewServer(h.AdminHandler())
			defer srv.Close()

			resp, err := http.Get(srv.URL + tt.requestPath)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if got := <-paths; got != tt.wantPath {
				t.Fatalf("upstream path = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestConsoleProxyLandingUsesPanelRoot(t *testing.T) {
	paths := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	withConsoleTarget(t, "workbuddy", consoleTarget{Base: upstream.URL, Landing: "/panel/", Prefix: "/panel"})

	h := testHandler(t)
	srv := httptest.NewServer(h.AdminHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/console/workbuddy/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := <-paths; got != "/panel/" {
		t.Fatalf("upstream landing = %q, want /panel/", got)
	}
}
func TestConsoleProxyRedirectsCanonicalRoot(t *testing.T) {
	h := testHandler(t)
	srv := httptest.NewServer(h.AdminHandler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/console/workbuddy?x=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTemporaryRedirect)
	}
	if got := resp.Header.Get("Location"); got != "/console/workbuddy/?x=1" {
		t.Fatalf("Location = %q", got)
	}
}

func TestConsoleProxyRewritesHTMLAndHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Set-Cookie", "sid=abc; Path=/; HttpOnly")
		_, _ = io.WriteString(w, `<script>fetch('/api/auth/me');location.replace('/');</script>`)
	}))
	defer upstream.Close()
	withConsoleTarget(t, "qoder", consoleTarget{Base: upstream.URL, Landing: "/"})

	h := testHandler(t)
	srv := httptest.NewServer(h.AdminHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/console/qoder/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, want := range []string{`fetch('/console/qoder/api/auth/me')`, `location.replace('/console/qoder/')`} {
		if !strings.Contains(text, want) {
			t.Fatalf("body missing %q: %s", want, text)
		}
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); strings.Contains(strings.ToLower(got), "frame-ancestors") {
		t.Fatalf("CSP still contains frame-ancestors: %q", got)
	}
	if got := resp.Header.Get("Set-Cookie"); !strings.Contains(got, "Path=/console/qoder/") {
		t.Fatalf("cookie path not rewritten: %q", got)
	}
}

func TestConsoleProxyLeavesJSONUntouched(t *testing.T) {
	const payload = `{"url":"/api/auth/me"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, payload)
	}))
	defer upstream.Close()
	withConsoleTarget(t, "qoder", consoleTarget{Base: upstream.URL, Landing: "/"})

	h := testHandler(t)
	srv := httptest.NewServer(h.AdminHandler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/console/qoder/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != payload {
		t.Fatalf("JSON body changed: %q", body)
	}
}

func TestConsoleRewriteSkipsExistingPrefix(t *testing.T) {
	got := string(rewriteRootPaths([]byte(`fetch('/console/qoder/api/x');`), "qoder"))
	if !strings.Contains(got, `fetch('/console/qoder/api/x')`) || strings.Contains(got, `/console/qoder/console/`) {
		t.Fatalf("double rewrite: %s", got)
	}
}

func TestConsoleRewriteTraeHelperPath(t *testing.T) {
	got := string(rewriteRootPaths([]byte(`<a href="/web/login">login</a><script>helperEndpointPath('/web/login/helper-info')</script>`), "trae"))
	if !strings.Contains(got, `href="/console/trae/web/login"`) {
		t.Fatalf("Trae href not rewritten: %s", got)
	}
	if strings.Contains(got, `/console/trae/web/login/console/trae`) {
		t.Fatalf("Trae helper path incorrectly rewritten: %s", got)
	}
}

func TestConsoleProxyStripsTraeHelperRelayQuery(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantPath  string
		wantQuery string
	}{
		{name: "helper info", path: "/console/trae/web/login/helper-info?relay=http%3A%2F%2Fgateway.test%2Fconsole%2Ftrae&x=1", wantPath: "/web/login/helper-info", wantQuery: "x=1"},
		{name: "download", path: "/console/trae/web/login/download?as=bat&relay=http%3A%2F%2Fgateway.test%2Fconsole%2Ftrae&x=1", wantPath: "/web/login/download", wantQuery: "as=bat&x=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type captured struct {
				path  string
				query string
			}
			got := make(chan captured, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got <- captured{path: r.URL.Path, query: r.URL.Query().Encode()}
				_, _ = io.WriteString(w, "ok")
			}))
			defer upstream.Close()
			withConsoleTarget(t, "trae", consoleTarget{Base: upstream.URL, Landing: "/web/login"})

			h := testHandler(t)
			srv := httptest.NewServer(h.AdminHandler())
			defer srv.Close()

			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			received := <-got
			if received.path != tt.wantPath || received.query != tt.wantQuery {
				t.Fatalf("upstream path=%q query=%q, want path=%q query=%q", received.path, received.query, tt.wantPath, tt.wantQuery)
			}
		})
	}
}

func TestConsoleProxyRelaySanitizationIsTraeHelperOnly(t *testing.T) {
	tests := []struct {
		name    string
		project string
		path    string
	}{
		{name: "other project", project: "qoder", path: "/console/qoder/web/login/helper-info?relay=keep&x=1"},
		{name: "other Trae path", project: "trae", path: "/console/trae/web/login?relay=keep&x=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got <- r.URL.Query().Encode()
				_, _ = io.WriteString(w, "ok")
			}))
			defer upstream.Close()
			withConsoleTarget(t, tt.project, consoleTarget{Base: upstream.URL, Landing: "/web/login"})

			h := testHandler(t)
			srv := httptest.NewServer(h.AdminHandler())
			defer srv.Close()

			resp, err := http.Get(srv.URL + tt.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if query := <-got; query != "relay=keep&x=1" {
				t.Fatalf("upstream query = %q, want relay preserved", query)
			}
		})
	}
}
