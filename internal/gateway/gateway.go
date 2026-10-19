package gateway

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"api-gateway/internal/config"
)

const maxErrorBody = 2 << 20

type Gateway struct {
	projects       map[string]config.ResolvedProject
	defaultProject string
	maxBodyBytes   int64
	client         *http.Client
	auth           string
}

func New(projects map[string]config.ResolvedProject, defaultProject string, maxBodyBytes int64, timeout time.Duration, auth string) (*Gateway, error) {
	if len(projects) == 0 {
		return nil, errors.New("no projects")
	}
	if defaultProject == "" {
		return nil, errors.New("default project is empty")
	}
	if _, ok := projects[defaultProject]; !ok {
		return nil, fmt.Errorf("default project %q is not configured", defaultProject)
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = 64 << 20
	}
	if timeout <= 0 {
		timeout = 300 * time.Second
	}
	return &Gateway{
		projects:       projects,
		defaultProject: defaultProject,
		maxBodyBytes:   maxBodyBytes,
		client: &http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          100,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: timeout,
			},
		},
		auth: auth,
	}, nil
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", g.handleHealth)
	mux.HandleFunc("/", g.handle)
	return mux
}

type route struct {
	project string
	rest    []string
	legacy  bool
}

func (g *Gateway) route(path string) (route, bool) {
	if path == "/" {
		return route{}, false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 {
		return route{}, false
	}
	if _, ok := g.projects[parts[0]]; ok {
		return route{project: parts[0], rest: parts[1:]}, true
	}
	// Backward compatibility: /v1/... remains the default project (workbuddy).
	if parts[0] == "v1" {
		return route{project: g.defaultProject, rest: parts, legacy: true}, true
	}
	return route{}, false
}

func (g *Gateway) handle(w http.ResponseWriter, r *http.Request) {
	rte, ok := g.route(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{
			"type": "not_found_error", "message": "unknown gateway path",
		}})
		return
	}
	project, ok := g.projects[rte.project]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "unknown project"}})
		return
	}
	if g.auth != "" && !constantEqual(bearer(r), g.auth) && !constantEqual(r.Header.Get("x-api-key"), g.auth) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{
			"type": "authentication_error", "message": "invalid gateway API key",
		}})
		return
	}

	upstreamPath := strings.Join(rte.rest, "/")
	if upstreamPath != "" {
		upstreamPath = "/" + upstreamPath
	}
	// Each project has a different health endpoint. /<project>/health and
	// /<project>/healthz are normalized to that project's configured path.
	if len(rte.rest) > 0 && (rte.rest[0] == "health" || rte.rest[0] == "healthz") {
		upstreamPath = project.HealthPath
	}

	protocol := classify(upstreamPath)
	native := false
	if protocol != "" {
		native = project.Native[config.Protocol(protocol)]
	}
	if protocol == "" || native {
		g.proxy(w, r, project, upstreamPath)
		return
	}

	switch protocol {
	case "responses":
		g.responsesFromChat(w, r, project)
	case "anthropic":
		g.anthropicFromChat(w, r, project)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"message": "unsupported path"}})
	}
}

func classify(path string) string {
	switch path {
	case "/v1/chat/completions", "/chat/completions":
		return "chat"
	case "/v1/responses", "/responses":
		return "responses"
	case "/v1/messages", "/messages", "/v1/messages/count_tokens", "/messages/count_tokens":
		return "anthropic"
	default:
		return ""
	}
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	projects := make([]map[string]any, 0, len(g.projects))
	for name := range g.projects {
		projects = append(projects, map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"service":  "api-gateway",
		"default":  g.defaultProject,
		"projects": projects,
	})
}

func (g *Gateway) proxy(w http.ResponseWriter, r *http.Request, project config.ResolvedProject, path string) {
	u, err := url.Parse(project.Upstream)
	if err != nil {
		writeUpstreamError(w, http.StatusBadGateway, "invalid upstream URL")
		return
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	u.RawQuery = r.URL.RawQuery
	target := u.String()
	body, err := g.readBody(r)
	if err != nil {
		writeUpstreamError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeUpstreamError(w, http.StatusBadRequest, "create upstream request failed")
		return
	}
	copyHeaders(req.Header, r.Header)
	stripHopHeaders(req.Header)
	req.Host = ""
	protocol := protocolForPath(path)
	if g.auth != "" {
		if !normalizeGatewayAuth(r.Header.Get("Authorization"), r.Header.Get("x-api-key"), g.auth) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]any{"type": "authentication_error", "message": "invalid gateway API key"}})
			return
		}
		clearClientCredentials(req.Header)
	}
	g.injectAuth(req.Header, project, protocol)

	resp, err := g.client.Do(req)
	if err != nil {
		writeUpstreamError(w, http.StatusBadGateway, "upstream unavailable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	stripHopHeaders(w.Header())
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("proxy stream copy: %v", err)
	}
}

func (g *Gateway) readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, g.maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}
	if int64(len(body)) > g.maxBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", g.maxBodyBytes)
	}
	return body, nil
}

func joinURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path, "/")
	return u.String(), nil
}

func copyHeaders(dst, src http.Header) {
	for k, values := range src {
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func stripHopHeaders(h http.Header) {
	for _, k := range []string{
		"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Content-Length",
	} {
		h.Del(k)
	}
}

func bearer(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(v), "bearer ") {
		return strings.TrimSpace(v[7:])
	}
	return ""
}

func constantEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func protocolForPath(path string) config.Protocol {
	switch classify(path) {
	case "chat":
		return config.ProtocolChat
	case "responses":
		return config.ProtocolResponses
	case "anthropic":
		return config.ProtocolAnthropic
	default:
		return ""
	}
}

func (g *Gateway) injectAuth(h http.Header, project config.ResolvedProject, protocol config.Protocol) {
	// Prefer project-specific credentials. Values are read from env at startup
	// and are never logged or returned by this service.
	if protocol == config.ProtocolAnthropic {
		if project.AnthropicAPIKey != "" {
			h.Set("x-api-key", project.AnthropicAPIKey)
			h.Del("Authorization")
			return
		}
		if project.APIKey != "" {
			h.Set("x-api-key", project.APIKey)
			h.Del("Authorization")
			return
		}
		return
	}
	if project.APIKey != "" {
		h.Set("Authorization", "Bearer "+project.APIKey)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeUpstreamError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"type": "upstream_error", "message": msg,
	}})
}

func readJSON(r *http.Request, limit int64) (map[string]any, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(body)) > limit {
		return nil, nil, fmt.Errorf("request body exceeds %d bytes", limit)
	}
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, body, err
	}
	return obj, body, nil
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func sseWrite(w http.ResponseWriter, event, data string) error {
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func setSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func isStream(r *http.Request, obj map[string]any) bool {
	if v, ok := obj["stream"].(bool); ok && v {
		return true
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	return strings.Contains(accept, "text/event-stream")
}

func normalizeGatewayAuth(auth, xAPIKey, expected string) bool {
	if expected == "" {
		return false
	}
	b := ""
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(auth)), "bearer ") {
		b = strings.TrimSpace(strings.TrimSpace(auth)[7:])
	}
	return constantEqual(b, expected) || constantEqual(strings.TrimSpace(xAPIKey), expected)
}
func (g *Gateway) sendChat(r *http.Request, project config.ResolvedProject, payload map[string]any, extra http.Header) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	target, err := joinURL(project.Upstream, "/v1/chat/completions")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	stripHopHeaders(req.Header)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	prepareConvertedChatAuth(req.Header, g.auth, r.Header, project)
	return g.client.Do(req)
}

func usageFromChat(obj map[string]any) map[string]any {
	if u, ok := obj["usage"].(map[string]any); ok {
		return u
	}
	return nil
}

func parseSSE(r io.Reader, handler func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	event := ""
	var data []string
	flush := func() error {
		if len(data) == 0 {
			event = ""
			return nil
		}
		err := handler(event, strings.Join(data, "\n"))
		event = ""
		data = nil
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

func copyClientAuth(r *http.Request, extra http.Header) {
	for _, k := range []string{"Authorization", "x-api-key", "anthropic-version", "OpenAI-Beta", "X-Request-ID"} {
		if v := r.Header.Get(k); v != "" {
			extra.Set(k, v)
		}
	}
}

func modelName(obj map[string]any) string {
	if s, ok := obj["model"].(string); ok && s != "" {
		return s
	}
	return "auto"
}

func newID(prefix string) string {
	return prefix + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func contentText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var b strings.Builder
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

func responseError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorEnvelope("invalid_request_error", msg))
}
