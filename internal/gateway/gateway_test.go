package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-gateway/internal/config"
)

type testGateway struct {
	handler http.Handler
	up      *httptest.Server
	seen    *http.Request
	body    []byte
}

func newTestGateway(t *testing.T, native []string, upstream http.HandlerFunc) *testGateway {

	tg := &testGateway{}
	t.Helper()
	tg.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		tg.seen = r.Clone(r.Context())
		tg.body = body
		r.Body = io.NopCloser(bytes.NewReader(body))
		upstream(w, r)
	}))
	projects := map[string]config.ResolvedProject{
		"workbuddy": {
			Project:    config.Project{Name: "workbuddy", Upstream: tg.up.URL, HealthPath: "/healthz"},
			Native:     map[config.Protocol]bool{config.ProtocolChat: true},
			HealthPath: "/healthz",
		},
		"qoder": {
			Project: config.Project{Name: "qoder", Upstream: tg.up.URL, HealthPath: "/health"},
			Native: map[config.Protocol]bool{
				config.ProtocolChat:      true,
				config.ProtocolResponses: true,
				config.ProtocolAnthropic: true,
			},
			HealthPath: "/health",
		},
	}
	g, err := New(projects, "workbuddy", 1<<20, 5*time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	tg.handler = g.Handler()
	return tg
}

func (tg *testGateway) do(method, path string, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	tg.handler.ServeHTTP(rec, req)
	return rec
}

func TestNativeChatProxyKeepsPathAndBody(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion"}`))
	})
	rec := tg.do("POST", "/workbuddy/v1/chat/completions", `{"model":"m","messages":[]}`, map[string]string{"Content-Type": "application/json"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if tg.seen.URL.Path != "/v1/chat/completions" || string(tg.body) != `{"model":"m","messages":[]}` {
		t.Fatalf("path=%s body=%s", tg.seen.URL.Path, tg.body)
	}
}

func TestNativeResponsesAndAnthropicProxy(t *testing.T) {
	tg := newTestGateway(t, []string{"chat", "responses", "anthropic"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"` + r.URL.Path + `"}`))
	})
	for _, p := range []string{"/qoder/v1/responses", "/qoder/v1/messages"} {
		rec := tg.do("POST", p, `{"model":"m"}`, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), tg.seen.URL.Path) {
			t.Fatalf("%s status=%d body=%s path=%s", p, rec.Code, rec.Body.String(), tg.seen.URL.Path)
		}
	}
}

func TestNativeProxyKeepsQuery(t *testing.T) {
	// Ensures /v1/models?refresh=true survives the proxy hop.
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":"` + r.URL.RawQuery + `"}`))
	})
	rec := tg.do("GET", "/workbuddy/v1/models?refresh=true&region=cn", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `refresh=true`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if tg.seen.URL.RawQuery != "refresh=true&region=cn" {
		t.Fatalf("query=%q", tg.seen.URL.RawQuery)
	}
}
func TestLegacyV1RoutesToDefaultProject(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("old-ok"))
	})
	rec := tg.do("GET", "/v1/models", "", nil)
	if rec.Code != 200 || rec.Body.String() != "old-ok" || tg.seen.URL.Path != "/v1/models" {
		t.Fatalf("status=%d body=%s path=%s", rec.Code, rec.Body.String(), tg.seen.URL.Path)
	}
}

func TestHealthAliasAndGatewayHealth(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	})
	rec := tg.do("GET", "/workbuddy/health", "", nil)
	if rec.Code != 200 || rec.Body.String() != "/healthz" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = tg.do("GET", "/healthz", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"api-gateway"`) {
		t.Fatalf("gateway health status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUnknownProjectAndMethod(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called")
	})
	if rec := tg.do("GET", "/unknown/v1/models", "", nil); rec.Code != 404 {
		t.Fatalf("unknown status=%d", rec.Code)
	}
	// /qoder is native for Responses, so GET is proxied and the mock handles it.
	if rec := tg.do("POST", "/workbuddy/v1/responses", `{}`, nil); rec.Code != 400 {
		t.Fatalf("invalid conversion request status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestResponsesConversionNonStreamIncludesTools(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			t.Fatal("unexpected stream")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-1", "object": "chat.completion",
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant", "content": "hello",
					"tool_calls": []any{map[string]any{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "lookup", "arguments": `{"q":"x"}`},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4},
		})
	})
	body := `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"function","name":"lookup","description":"d","parameters":{"type":"object"}}]}`
	rec := tg.do("POST", "/workbuddy/v1/responses", body, nil)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	output, _ := out["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("output=%v", output)
	}
	fc, _ := output[1].(map[string]any)
	if fc["type"] != "function_call" || fc["name"] != "lookup" {
		t.Fatalf("function call=%v", fc)
	}
	var sent map[string]any
	_ = json.Unmarshal(tg.body, &sent)
	if sent["messages"] == nil || sent["tools"] == nil {
		t.Fatalf("converted request=%s", tg.body)
	}
}

func TestResponsesConversionStream(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	rec := tg.do("POST", "/workbuddy/v1/responses", `{"model":"m","input":"hi","stream":true}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "response.created") || !strings.Contains(rec.Body.String(), "response.output_text.delta") || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicConversionNonStream(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		var sent map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		if sent["tools"] == nil {
			t.Fatalf("tools not converted: %s", raw)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "chatcmpl-2", "choices": []any{map[string]any{
				"message":       map[string]any{"role": "assistant", "content": "done"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 3},
		})
	})
	body := `{"model":"m","max_tokens":10,"system":"sys","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","description":"d","input_schema":{"type":"object"}}]}`
	rec := tg.do("POST", "/workbuddy/v1/messages", body, map[string]string{"x-api-key": "client"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"type":"message"`) || !strings.Contains(rec.Body.String(), `"text":"done"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicConversionStream(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	rec := tg.do("POST", "/workbuddy/v1/messages", `{"model":"m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "message_start") || !strings.Contains(body, "content_block_delta") || !strings.Contains(body, "message_stop") {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
}

func TestCountTokens(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("count_tokens should be local")
	})
	rec := tg.do("POST", "/workbuddy/v1/messages/count_tokens", `{"model":"m","messages":[{"role":"user","content":"hello world"}]}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "input_tokens") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpstreamErrorForwarding(t *testing.T) {
	tg := newTestGateway(t, []string{"chat"}, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit","message":"slow down"}}`))
	})
	rec := tg.do("POST", "/workbuddy/v1/responses", `{"model":"m","input":"hi"}`, nil)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "slow down") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGatewayAuth(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {Project: config.Project{Name: "workbuddy", Upstream: up.URL}, Native: map[config.Protocol]bool{config.ProtocolChat: true}},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unauthorized status=%d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("authorized status=%d", rec.Code)
	}
}
func TestLandingPageListsProjects(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("landing page should not call upstream")
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {Project: config.Project{Name: "workbuddy", Upstream: up.URL, Native: []string{"chat"}}, Native: map[config.Protocol]bool{config.ProtocolChat: true}},
		"qoder":     {Project: config.Project{Name: "qoder", Upstream: up.URL, Native: []string{"chat", "responses", "anthropic"}}, Native: map[config.Protocol]bool{config.ProtocolChat: true, config.ProtocolResponses: true, config.ProtocolAnthropic: true}},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := WithLandingPage(g.Handler(), []config.Project{
		{Name: "workbuddy", Native: []string{"chat"}},
		{Name: "qoder", Native: []string{"chat", "responses", "anthropic"}},
	}, "workbuddy")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/qoder/v1") || !strings.Contains(rec.Body.String(), "API Gateway") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestGatewayAuthIsNotForwardedWithoutProjectKey(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("gateway auth leaked upstream: %q", got)
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {Project: config.Project{Name: "workbuddy", Upstream: up.URL}, Native: map[config.Protocol]bool{config.ProtocolChat: true}},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/workbuddy/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
func TestGatewayAuthWithProjectKeyInjectsProjectCredential(t *testing.T) {
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {
			Project: config.Project{Name: "workbuddy", Upstream: up.URL},
			Native:  map[config.Protocol]bool{config.ProtocolChat: true},
			APIKey:  "project-secret",
		},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "gateway-secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/workbuddy/v1/models", nil)
	req.Header.Set("Authorization", "Bearer gateway-secret")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || got != "Bearer project-secret" {
		t.Fatalf("status=%d auth=%q body=%s", rec.Code, got, rec.Body.String())
	}
}

func TestGatewayAuthNotLeakedInResponsesConversion(t *testing.T) {
	var gotAuth, gotAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {
			Project: config.Project{Name: "workbuddy", Upstream: up.URL},
			Native:  map[config.Protocol]bool{config.ProtocolChat: true},
			APIKey:  "project-secret",
		},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "gateway-secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/workbuddy/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer gateway-secret")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer project-secret" || gotAPIKey != "" {
		t.Fatalf("upstream auth=%q x-api-key=%q", gotAuth, gotAPIKey)
	}
}

func TestGatewayXAPIKeyNotLeakedInAnthropicConversion(t *testing.T) {
	var gotAuth, gotAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {
			Project: config.Project{Name: "workbuddy", Upstream: up.URL},
			Native:  map[config.Protocol]bool{config.ProtocolChat: true},
			APIKey:  "project-secret",
		},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "gateway-secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/workbuddy/v1/messages", strings.NewReader(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "gateway-secret")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer project-secret" || gotAPIKey != "" {
		t.Fatalf("upstream auth=%q x-api-key=%q", gotAuth, gotAPIKey)
	}
}

func TestNoProjectKeyStillPassesClientKeyWithoutGatewayAuth(t *testing.T) {
	var gotAuth, gotAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {
			Project: config.Project{Name: "workbuddy", Upstream: up.URL},
			Native:  map[config.Protocol]bool{config.ProtocolChat: true},
		},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/workbuddy/v1/messages", strings.NewReader(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", "client-upstream-key")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAPIKey != "client-upstream-key" {
		t.Fatalf("client key was not passed through: %q", gotAPIKey)
	}
	if gotAuth != "" {
		t.Fatalf("unexpected Authorization: %q", gotAuth)
	}
}

func TestGatewayXAPIKeyNotLeakedInResponsesConversion(t *testing.T) {
	var gotAuth, gotAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {
			Project: config.Project{Name: "workbuddy", Upstream: up.URL},
			Native:  map[config.Protocol]bool{config.ProtocolChat: true},
		},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "gateway-secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/workbuddy/v1/responses", strings.NewReader(`{"model":"m","input":"hi"}`))
	req.Header.Set("x-api-key", "gateway-secret")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "" || gotAPIKey != "" {
		t.Fatalf("gateway key leaked: auth=%q x-api-key=%q", gotAuth, gotAPIKey)
	}
}

func TestGatewayAuthNotLeakedInAnthropicConversionWithProjectKey(t *testing.T) {
	var gotAuth, gotAPIKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer up.Close()
	projects := map[string]config.ResolvedProject{
		"workbuddy": {
			Project: config.Project{Name: "workbuddy", Upstream: up.URL},
			Native:  map[config.Protocol]bool{config.ProtocolChat: true},
			APIKey:  "project-secret",
		},
	}
	g, err := New(projects, "workbuddy", 1<<20, time.Second, "gateway-secret")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/workbuddy/v1/messages", strings.NewReader(`{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer gateway-secret")
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gotAuth != "Bearer project-secret" || gotAPIKey != "" {
		t.Fatalf("upstream auth=%q x-api-key=%q", gotAuth, gotAPIKey)
	}
}
