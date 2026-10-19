package panel

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

const consoleRoutePrefix = "/console/"

type consoleTarget struct {
	Base    string
	Landing string
	Prefix  string
}

// Console ports are not always the API bridge ports. Qoder, for example,
// exposes its account console on 3588 and starts the API bridge on 8963 only
// after a successful login.
var consoleTargets = map[string]consoleTarget{
	"workbuddy": {Base: "http://127.0.0.1:7863", Landing: "/panel/", Prefix: "/panel"},
	"qoder":     {Base: "http://127.0.0.1:3588", Landing: "/"},
	"opencode":  {Base: "http://127.0.0.1:10010", Landing: "/panel", Prefix: "/panel"},
	"trae":      {Base: "http://127.0.0.1:8000", Landing: "/web/login"},
	"gcli":      {Base: "http://127.0.0.1:7861", Landing: "/"},
}

func (h *Handler) consoleHandler(w http.ResponseWriter, r *http.Request) {
	// WebSocket upgrades bypass net/http's normal response path. A reverse
	// proxy cannot switch protocols unless the connection is available to it.
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		h.consoleWebSocketHandler(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, consoleRoutePrefix) {
		http.NotFound(w, r)
		return
	}
	name, tail, ok := splitConsolePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	target, ok := consoleTargets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	base, err := url.Parse(target.Base)
	if err != nil || base.Scheme == "" || base.Host == "" {
		http.Error(w, "invalid console target", http.StatusBadGateway)
		return
	}
	if tail == "" {
		// Keep a canonical trailing slash. Relative assets and WebSockets then
		// resolve under /console/<project>/ inside the iframe.
		local := consoleRoutePrefix + name + "/"
		http.Redirect(w, r, local+querySuffix(r.URL.RawQuery), http.StatusTemporaryRedirect)
		return
	}
	upstreamPath := tail
	if tail == "/" {
		upstreamPath = target.Landing
	} else if target.Prefix != "" &&
		(tail == target.Prefix || strings.HasPrefix(tail, target.Prefix+"/")) {
		// Some consoles already link to /panel/... while other assets stay
		// root-relative. Do not mount the same prefix twice.
		upstreamPath = tail
	} else {
		upstreamPath = joinURLPath(target.Prefix, tail)
	}
	// The reverse proxy joins base.Path with the request path. Strip the
	// local /console/<project> prefix and keep only the upstream-relative path.
	r.URL.Path = upstreamPath
	r.URL.RawPath = ""
	r.URL.RawQuery = sanitizeConsoleQuery(name, upstreamPath, r.URL.RawQuery)

	proxy := httputil.NewSingleHostReverseProxy(base)
	proxy.FlushInterval = -1
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = base.Host
		req.Header.Set("Accept-Encoding", "identity")
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		stripFrameRestrictions(resp.Header)
		rewriteLocation(resp.Header, name)
		rewriteCookies(resp.Header, name)
		if isRewriteable(resp.Header.Get("Content-Type")) {
			return rewriteConsoleBody(resp, name)
		}
		return nil
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "项目控制台未启动或不可访问", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// Trae resolves the authorization helper against the upstream request host.
// When its login page is mounted behind /console/trae, the browser supplies
// that public address as a relay query parameter while this proxy intentionally
// forwards the loopback Host. Trae rejects the mismatch. Drop only this
// proxy-injected hint for the two helper endpoints; Trae then reports the
// loopback relay that the local helper is actually using.
func sanitizeConsoleQuery(project, upstreamPath, rawQuery string) string {
	if project != "trae" || (upstreamPath != "/web/login/helper-info" && upstreamPath != "/web/login/download") {
		return rawQuery
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	if _, exists := values["relay"]; !exists {
		return rawQuery
	}
	values.Del("relay")
	return values.Encode()
}

func (h *Handler) consoleWebSocketHandler(w http.ResponseWriter, r *http.Request) {
	name, tail, ok := splitConsolePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	target, ok := consoleTargets[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	base, err := url.Parse(target.Base)
	if err != nil || base.Scheme == "" || base.Host == "" {
		http.Error(w, "invalid console target", http.StatusBadGateway)
		return
	}
	if tail == "" {
		tail = "/"
	}
	upstreamPath := tail
	if tail == "/" {
		upstreamPath = target.Landing
	} else if target.Prefix != "" &&
		(tail == target.Prefix || strings.HasPrefix(tail, target.Prefix+"/")) {
		upstreamPath = tail
	} else {
		upstreamPath = joinURLPath(target.Prefix, tail)
	}
	r.URL.Path = upstreamPath
	r.URL.RawPath = ""
	r.URL.RawQuery = sanitizeConsoleQuery(name, upstreamPath, r.URL.RawQuery)

	proxy := httputil.NewSingleHostReverseProxy(base)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = base.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "项目 WebSocket 未启动或不可访问", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// splitConsolePath returns the project name and the upstream-relative path.
// The canonical root /console/<project> has an empty tail.
func splitConsolePath(p string) (string, string, bool) {
	rest := strings.TrimPrefix(p, consoleRoutePrefix)
	if rest == "" {
		return "", "", false
	}
	name, tail, found := strings.Cut(rest, "/")
	if name == "" {
		return "", "", false
	}
	if !found {
		return name, "", true
	}
	return name, "/" + tail, true
}

func joinURLPath(basePath, tail string) string {
	if tail == "" {
		if basePath == "" {
			return "/"
		}
		return basePath
	}
	if !strings.HasPrefix(tail, "/") {
		tail = "/" + tail
	}
	if basePath == "" || basePath == "/" {
		return tail
	}
	return strings.TrimRight(basePath, "/") + tail
}

func querySuffix(raw string) string {
	if raw == "" {
		return ""
	}
	return "?" + raw
}

func stripFrameRestrictions(h http.Header) {
	h.Del("X-Frame-Options")
	for _, key := range []string{"Content-Security-Policy", "Content-Security-Policy-Report-Only"} {
		values := h.Values(key)
		h.Del(key)
		for _, value := range values {
			value = removeCSPDirective(value, "frame-ancestors")
			if strings.TrimSpace(value) != "" {
				h.Add(key, value)
			}
		}
	}
	h.Set("X-Frame-Options", "SAMEORIGIN")
}

func removeCSPDirective(value, directive string) string {
	fields := strings.Split(value, ";")
	out := fields[:0]
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" || strings.EqualFold(strings.Fields(trimmed)[0], directive) {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "; ")
}

func rewriteLocation(h http.Header, project string) {
	values := h.Values("Location")
	if len(values) == 0 {
		return
	}
	h.Del("Location")
	prefix := consoleRoutePrefix + project
	for _, value := range values {
		u, err := url.Parse(value)
		if err != nil {
			h.Add("Location", value)
			continue
		}
		if u.Host == "" && (strings.HasPrefix(u.Path, "/") || u.Path != "") &&
			!strings.HasPrefix(u.Path, prefix+"/") && u.Path != prefix {
			u.Path = joinURLPath(prefix, u.Path)
		}
		h.Add("Location", u.String())
	}
}

func rewriteCookies(h http.Header, project string) {
	values := h.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}
	h.Del("Set-Cookie")
	prefix := consoleRoutePrefix + project + "/"
	for _, value := range values {
		parts := strings.Split(value, ";")
		found := false
		for i := 1; i < len(parts); i++ {
			part := strings.TrimSpace(parts[i])
			if len(part) < 5 || !strings.EqualFold(part[:5], "Path=") {
				continue
			}
			old := strings.TrimSpace(part[5:])
			if old == "" || old == "/" || !strings.HasPrefix(old, prefix) {
				parts[i] = " Path=" + prefix
			} else {
				parts[i] = " Path=" + old
			}
			found = true
			break
		}
		if !found {
			parts = append(parts, " Path="+prefix)
		}
		h.Add("Set-Cookie", strings.Join(parts, ";"))
	}
}

func isRewriteable(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return strings.HasPrefix(contentType, "text/html") ||
		strings.HasPrefix(contentType, "text/javascript") ||
		strings.HasPrefix(contentType, "application/javascript")
}

func rewriteConsoleBody(resp *http.Response, project string) error {
	const maxRewritableBody = 8 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRewritableBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxRewritableBody {
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), resp.Body))
		return nil
	}
	_ = resp.Body.Close()
	rewritten := rewriteRootPaths(body, project)
	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	return nil
}

func rewriteRootPaths(body []byte, project string) []byte {
	prefix := consoleRoutePrefix + project
	out := string(body)

	// Rewrite root-relative asset/API paths, but never rewrite the project's
	// own /console path again when a page is reloaded through the proxy.
	paths := []string{
		"/api/", "/auth/", "/creds/", "/config/", "/logs/", "/usage/",
		"/version/", "/front/", "/panel/", "/favicon.", "/login.html",
	}
	quotePrefixes := []string{`"`, `'`, "`", "(", "=", " "}
	for _, rawPath := range paths {
		for _, quote := range quotePrefixes {
			old := quote + rawPath
			if strings.Contains(out, old+prefix+rawPath) {
				continue
			}
			out = strings.ReplaceAll(out, old, quote+prefix+rawPath)
		}
	}

	// Some panels are mounted under /panel and use bare relative assets such as
	// <script src="app.js">. Prefix those document-relative paths as well, while
	// leaving hashes, data URLs and absolute URLs untouched.
	out = strings.ReplaceAll(out, `src="app.js"`, `src="`+prefix+`/app.js"`)
	out = strings.ReplaceAll(out, `src='app.js'`, `src='`+prefix+`/app.js'`)
	out = strings.ReplaceAll(out, `href="app.css"`, `href="`+prefix+`/app.css"`)
	out = strings.ReplaceAll(out, `href='app.css'`, `href='`+prefix+`/app.css'`)
	// Trae uses absolute links in its login page. Do not rewrite
	// helperEndpointPath('/web/login/...'): that helper adds the current
	// /console/<project> prefix itself.
	out = strings.ReplaceAll(out, `href="/web/login`, `href="`+prefix+`/web/login`)
	out = strings.ReplaceAll(out, `href='/web/login`, `href='`+prefix+`/web/login`)
	// Qoder uses location.replace('/') after login. Keep that redirect inside
	// the project namespace instead of sending the iframe to the gateway root.
	out = strings.ReplaceAll(out, "location.replace('/')", "location.replace('"+prefix+"/')")
	out = strings.ReplaceAll(out, `location.replace("/")`, `location.replace("`+prefix+`/")`)
	out = strings.ReplaceAll(out, "window.location.href = '/web/login'", "window.location.href = '"+prefix+"/web/login'")
	out = strings.ReplaceAll(out, `window.location.href = "/web/login"`, `window.location.href = "`+prefix+`/web/login"`)

	// CSP meta tags must not carry frame-ancestors: none. Response headers are
	// handled separately in stripFrameRestrictions.
	out = removeMetaFrameAncestors(out)
	return []byte(out)
}

func removeMetaFrameAncestors(s string) string {
	needle := "frame-ancestors 'none'"
	for {
		i := strings.Index(strings.ToLower(s), needle)
		if i < 0 {
			return s
		}
		s = s[:i] + s[i+len(needle):]
	}
}
