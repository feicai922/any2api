package gateway

import (
	"html/template"
	"net/http"
	"sort"
	"strings"

	"api-gateway/internal/config"
)

type landingProject struct {
	Name      string
	Base      string
	Chat      bool
	Responses bool
	Anthropic bool
}

// WithLandingPage serves a small read-only directory page at "/" while leaving
// every API route untouched. It intentionally contains no credentials.
func WithLandingPage(next http.Handler, projects []config.Project, defaultProject string) http.Handler {
	items := make([]landingProject, 0, len(projects))
	for _, p := range projects {
		native := map[string]bool{}
		for _, n := range p.Native {
			native[strings.ToLower(strings.TrimSpace(n))] = true
		}
		// Chat is always available through the gateway's conversion fallback.
		native["chat"] = true
		items = append(items, landingProject{
			Name:      p.Name,
			Base:      "/" + p.Name + "/v1",
			Chat:      native["chat"],
			Responses: native["responses"],
			Anthropic: native["anthropic"],
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	page := template.Must(template.New("landing").Parse(landingHTML))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = page.Execute(w, struct {
			Default  string
			Projects []landingProject
		}{Default: defaultProject, Projects: items})
	})
}

const landingHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>API Gateway</title>
<style>
:root { color-scheme: light dark; --bg:#f6f7f9; --card:#fff; --fg:#17202a; --muted:#667085; --line:#e4e7ec; --accent:#2563eb; }
@media (prefers-color-scheme: dark) { :root { --bg:#0f141b; --card:#171e27; --fg:#e7edf5; --muted:#98a2b3; --line:#2b3542; --accent:#7aa2ff; } }
* { box-sizing:border-box; }
body { margin:0; background:var(--bg); color:var(--fg); font-family:system-ui,-apple-system,"Segoe UI","Microsoft YaHei",sans-serif; line-height:1.55; }
main { max-width:1080px; margin:0 auto; padding:40px 20px 64px; }
h1 { margin:0 0 6px; font-size:28px; }
.lead { color:var(--muted); margin:0 0 24px; }
.note { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:14px 16px; margin:0 0 24px; color:var(--muted); }
.grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(280px,1fr)); gap:14px; }
.card { background:var(--card); border:1px solid var(--line); border-radius:14px; padding:18px; }
.card h2 { margin:0 0 8px; font-size:20px; }
.url { display:block; overflow-wrap:anywhere; color:var(--accent); font-family:ui-monospace,SFMono-Regular,Consolas,monospace; font-size:13px; margin:8px 0 12px; }
.tags { display:flex; gap:6px; flex-wrap:wrap; }
.tag { font-size:12px; border:1px solid var(--line); border-radius:999px; padding:2px 8px; color:var(--muted); }
.tag.yes { color:var(--fg); border-color:var(--accent); }
.legacy { margin-top:22px; color:var(--muted); font-size:13px; }
code { font-family:ui-monospace,SFMono-Regular,Consolas,monospace; }
</style>
</head>
<body>
<main>
<h1>API Gateway</h1>
<p class="lead">统一入口 · 按项目前缀路由 · 三种协议适配</p>
<p class="note">当前默认项目：<code>{{.Default}}</code>。旧地址 <code>/v1/...</code> 兼容并路由到默认项目。上游凭据只从服务端环境变量读取，页面不显示任何密钥。</p>
<div class="grid">
{{range .Projects}}
<section class="card">
<h2>{{.Name}}</h2>
<code class="url">{{.Base}}</code>
<div class="tags">
{{if .Chat}}<span class="tag yes">Chat</span>{{end}}
{{if .Responses}}<span class="tag yes">Responses</span>{{else}}<span class="tag">Responses 经转换</span>{{end}}
{{if .Anthropic}}<span class="tag yes">Anthropic</span>{{else}}<span class="tag">Anthropic 经转换</span>{{end}}
</div>
</section>
{{end}}
</div>
<p class="legacy">健康检查：<code>/healthz</code> · 项目健康别名：<code>/&lt;project&gt;/health</code></p>
</main>
</body>
</html>
`
