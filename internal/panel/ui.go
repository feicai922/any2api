package panel

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static
var staticFiles embed.FS

type loginPageData struct {
	Next  string
	Error string
}

func (h *Handler) renderPanel(w http.ResponseWriter, r *http.Request, local bool) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.renderPanelPage(w, http.StatusOK, local, nil)
}

func (h *Handler) renderPanelPage(w http.ResponseWriter, status int, local bool, login *loginPageData) {
	body, err := fs.ReadFile(staticFiles, "static/index.html")
	if err != nil {
		http.Error(w, "panel assets unavailable", http.StatusInternalServerError)
		return
	}

	attributes := `data-manage="` + boolText(login == nil) + `" data-local="` + boolText(local) +
		`" data-default-project="` + template.HTMLEscapeString(h.firstProjectName()) + `"`
	if login != nil {
		attributes += ` data-login="true" data-next="` + template.HTMLEscapeString(login.Next) +
			`" data-login-error="` + template.HTMLEscapeString(login.Error) + `"`
	}
	baseURL := h.localURL
	if !local {
		baseURL = h.publicURL
	}
	attributes += ` data-api-base="` + template.HTMLEscapeString(baseURL) + `"`
	attributes += ` data-admin-base="` + template.HTMLEscapeString(firstNonEmpty(h.localAdminURL, baseURL)) + `"`
	if local {
		attributes += ` data-token="` + template.HTMLEscapeString(h.token) + `"`
	}

	title := "反代项目工作台"
	if login != nil {
		title = "管理员登录 · 反代项目工作台"
	}
	html := strings.Replace(string(body), `<title>反代项目工作台</title>`, `<title>`+title+`</title>`, 1)
	html = strings.Replace(html, `<html lang="zh-CN">`, `<html lang="zh-CN" `+attributes+`>`, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(html))
}

func (h *Handler) staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(sub))
}

func (h *Handler) firstProjectName() string {
	if len(h.specs) == 0 {
		return ""
	}
	return h.specs[0].Name
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
