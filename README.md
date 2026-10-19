# API Gateway

把多个 OpenAI、Responses 与 Anthropic 兼容反代聚合到同一个入口，按项目前缀路由。支持本地独立运行，也支持在服务器或内网穿透环境部署。

> 本仓库只包含网关与 Vue 3 管理面板，不包含 WorkBuddy、Qoder、OpenCode、Trae、GCLI 的账号、配置、日志或凭证。上游项目需要另行准备。

## 功能

- 统一路由：`/workbuddy/v1`、`/qoder/v1`、`/opencode/v1`、`/trae/v1`、`/gcli/v1`
- 三种协议：Chat Completions、Responses、Anthropic Messages
- 协议转换：上游只支持 Chat 时，由网关转换 Responses / Anthropic 请求与 SSE 响应
- Vue 3 + Element Plus 管理面板：项目状态、启停、协议能力、文档说明
- 本地与远程管理：浏览器登录、会话 Cookie、登录限速
- 账号控制台独立打开，不在面板内嵌 iframe
- 所有项目目录、公网地址和监听端口均可配置

## 本地快速开始

要求：Go 1.23+、Node.js 20+、PowerShell 7（Windows）。

```powershell
Copy-Item config.example.json config.json
$env:API_GATEWAY_ADMIN_PASSWORD = "请自行设置一个强密码"

# Windows 可保留上游项目在同级目录：
# ..\workbuddy2api-panel
# ..\qoder2api
# ..\opencode2api
# ..\trae2api
# ..\gcli2api

go run ./cmd/server -config config.json -start-all=false
```

本地入口：

| 用途 | 默认地址 |
|---|---|
| 管理面板 | `http://127.0.0.1:7862/` |
| 文档说明 | `http://127.0.0.1:7862/docs` |
| 聚合 API | `http://127.0.0.1:7860` |

`config.json` 不会提交到 Git。统一访问密码只在运行时通过环境变量传递，不写入配置文件、脚本、日志或命令行参数。

## 页面管理

左侧切换项目，右侧显示运行状态、端口、协议能力、调用地址与“打开控制台”按钮。控制台在独立窗口打开，工作台不读取账号密码或 API Key。

常用操作：

- 启动全部项目
- 单独启动或停止项目
- 查看上游健康状态
- 打开各项目自己的登录控制台
- 查看 `/docs` 中的路由与调用示例

## 配置

完整示例见 `config.example.json`。

```json
{
  "listen": "127.0.0.1:7860",
  "default_project": "workbuddy",
  "max_body_bytes": 67108864,
  "upstream_timeout_seconds": 300,
  "projects": [
    {
      "name": "workbuddy",
      "upstream": "http://127.0.0.1:7863",
      "api_key_env": "WORKBUDDY_API_KEY",
      "native": ["chat"],
      "health_path": "/healthz"
    }
  ]
}
```

`native` 声明上游原生协议；`chat` 是所有项目的转换回退协议。`api_key_env` 只保存环境变量名，不保存密钥值。

### 环境变量

| 变量 | 用途 | 默认值 |
|---|---|---|
| `API_GATEWAY_ADMIN_PASSWORD` | 管理登录密码，同时作为默认网关 API Key | 必填 |
| `GATEWAY_API_KEY` | 聚合 API 鉴权密钥 | 未设置时不校验 |
| `GATEWAY_PUBLIC_BASE_URL` | 对外展示的 API 根地址 | 按请求 Host 动态生成 |
| `GATEWAY_LOCAL_API_URL` | 本机 API 展示地址 | `http://127.0.0.1:7860` |
| `GATEWAY_LOCAL_ADMIN_URL` | 本机管理展示地址 | `http://127.0.0.1:7862` |
| `WORKBUDDY_DIR` | WorkBuddy 项目目录 | `../workbuddy2api-panel` |
| `QODER_DIR` | Qoder 项目目录 | `../qoder2api` |
| `OPENCODE_DIR` | OpenCode 项目目录 | `../opencode2api` |
| `TRAE_DIR` | Trae 项目目录 | `../trae2api` |
| `GCLI_DIR` | GCLI 项目目录 | `../gcli2api` |

部署到反向代理或内网穿透后，建议明确设置 `GATEWAY_PUBLIC_BASE_URL`。未设置时，面板会从当前请求的 Host 和 `X-Forwarded-Proto` 推断，不包含固定公网 IP 或端口。

## 调用示例

以下示例假设网关 API Key 已设置为本地环境变量。

### Chat Completions

```powershell
curl.exe http://127.0.0.1:7860/trae/v1/chat/completions `
  -H "Authorization: Bearer $env:GATEWAY_API_KEY" `
  -H "Content-Type: application/json" `
  -d '{"model":"your-model","messages":[{"role":"user","content":"hi"}]}'
```

### Responses

```powershell
curl.exe http://127.0.0.1:7860/trae/v1/responses `
  -H "Authorization: Bearer $env:GATEWAY_API_KEY" `
  -H "Content-Type: application/json" `
  -d '{"model":"your-model","input":"hi"}'
```

### Anthropic Messages

```powershell
curl.exe http://127.0.0.1:7860/gcli/v1/messages `
  -H "Authorization: Bearer $env:GATEWAY_API_KEY" `
  -H "Content-Type: application/json" `
  -H "anthropic-version: 2023-06-01" `
  -d '{"model":"your-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'
```

流式请求（`stream: true` 或 `Accept: text/event-stream`）会以 SSE 转发或转换。

## 路由

- `/<project>/v1/chat/completions`
- `/<project>/v1/responses`
- `/<project>/v1/messages`
- `/<project>/v1/messages/count_tokens`
- `/<project>/health`、`/<project>/healthz`
- `/healthz` 只检查网关自身

未知项目返回 `404`。设置 `GATEWAY_API_KEY` 后，客户端必须携带 `Authorization: Bearer <key>` 或 `x-api-key`。网关密钥不会发送给上游。

## 部署与安全

- 公网只暴露聚合 API 端口；管理监听默认仅绑定 `127.0.0.1:7862`。
- 若通过反向代理或内网穿透公开管理页，请在网关前启用 HTTPS，并保留登录鉴权。
- 不要将上游项目的控制台端口直接暴露到公网。
- 不要把密码、Token、API Key、OAuth 凭证、账号文件、日志或真实 `config.json` 提交到 Git。
- 本项目不会读取或导出第三方账号凭证。
- 首次部署后建议撤销曾公开发送的 OAuth 凭证，并重新授权。

## 开发与构建

```powershell
npm install --prefix web
npm run build --prefix web
go test ./...
go vet ./...
go build ./cmd/server
```

前端构建产物位于 `internal/panel/static/`，通过 Go `embed` 打包进二进制。修改 Vue 后必须先构建前端，再编译 Go。

## 项目结构

```text
cmd/server/          网关入口
internal/config/     配置解析
internal/gateway/    协议路由与转换
internal/panel/      管理面板、项目控制与静态资源
web/                 Vue 3 + Element Plus
scripts/             本地安装、启动与控制脚本
```
