# any2api

把 WorkBuddy、Qoder、OpenCode、Trae、GCLI 五个反代项目聚合到同一个 Web 工作台和统一 API 入口。仓库同时包含网关、Vue 3 + Element Plus 管理面板，以及五个上游项目的源码快照。

## 仓库包含什么

```text
api-gateway/
├─ cmd/server/          统一网关入口
├─ internal/            路由、协议转换、管理面板
├─ web/                 Vue 3 + Element Plus 前端
├─ scripts/             安装、启动、控制脚本
└─ upstreams/
   ├─ workbuddy/        WorkBuddy 反代源码
   ├─ qoder/            Qoder 反代源码
   ├─ opencode/         OpenCode 反代源码
   ├─ trae/             Trae 反代源码
   └─ gcli/             GCLI 反代源码
```

五个上游项目的源码已一起放在 `upstreams/`，不是子模块。克隆仓库后运行安装脚本即可构建全部项目。

**不会上传或复制的内容：**账号登录状态、OAuth Token、Cookie、API Key、数据库、日志、虚拟环境、`node_modules`、真实 `config.json`。因此源码可以共享，但每个使用者仍需在自己的机器上重新登录各项目。

## 统一路由

| 项目 | 本地调用入口 |
|---|---|
| WorkBuddy | `http://127.0.0.1:7860/workbuddy/v1` |
| Qoder | `http://127.0.0.1:7860/qoder/v1` |
| OpenCode | `http://127.0.0.1:7860/opencode/v1` |
| Trae | `http://127.0.0.1:7860/trae/v1` |
| GCLI | `http://127.0.0.1:7860/gcli/v1` |

每个前缀尽量同时提供：

- `POST /<project>/v1/chat/completions`：Chat Completions
- `POST /<project>/v1/responses`：OpenAI Responses
- `POST /<project>/v1/messages`：Anthropic Messages
- `POST /<project>/v1/messages/count_tokens`

上游原生支持时直接转发；只支持 Chat 时，由网关转换 Responses / Anthropic 请求和 SSE 响应。

## 首次安装

要求：Windows 10/11、PowerShell 7、Go 1.22+、Node.js 20+、Python 3.12+。

```powershell
git clone https://github.com/feicai922/any2api.git
cd any2api
pwsh -NoProfile -File .\scripts\install.ps1
```

安装脚本会：

1. 构建 `upstreams/workbuddy` 和 `upstreams/qoder`
2. 为 `upstreams/opencode` 安装 npm 依赖，并安装 `opencode-ai@1.19.0`
3. 为 `upstreams/trae` 和 `upstreams/gcli` 创建虚拟环境并安装 Python 依赖
4. 构建 Vue 管理面板和统一网关
5. 只在文件不存在时复制 `config.example.json` 为 `config.json`

只检查、不安装：

```powershell
pwsh -NoProfile -File .\scripts\install.ps1 -CheckOnly
```

## 启动

```powershell
pwsh -NoProfile -File .\scripts\start.ps1
```

启动时会在终端安全提示输入统一访问密码，不会写入脚本、日志或 Git 文件。也可以用环境变量传入：

```powershell
$env:API_GATEWAY_ADMIN_PASSWORD = "自行设置的密码"
pwsh -NoProfile -File .\scripts\start.ps1
```

管理面板默认地址：

| 用途 | 地址 |
|---|---|
| 管理面板 | `http://127.0.0.1:7862/` |
| 文档说明 | `http://127.0.0.1:7862/docs` |
| 聚合 API | `http://127.0.0.1:7860` |

左侧切换五个项目；右侧可启动、停止、查看健康状态和打开各自控制台。控制台独立打开，不嵌入工作台。账号登录、OAuth、Cookie 和 API Key 由各上游自己的控制台处理。

## 公网部署

通过反向代理或内网穿透只公开网关和管理入口，不要直接公开 3588、7861、7863、8000、8765、8963、8964、10010 等上游控制台端口。

设置公网基地址后，管理页会显示公网调用地址：

```powershell
$env:GATEWAY_PUBLIC_BASE_URL = "https://你的域名或公网地址"
$env:API_GATEWAY_ADMIN_PASSWORD = "自行设置的密码"
pwsh -NoProfile -File .\scripts\start.ps1
```

公网管理页仍要求登录，登录后可执行启动、停止和查看状态；未登录只显示公开信息，不能管理项目。建议在前面加 HTTPS。

## 调用示例

以下示例用环境变量保存网关密钥，避免把密钥写进命令历史以外的文件：

```powershell
$env:GATEWAY_API_KEY = "你的网关密钥"

curl.exe http://127.0.0.1:7860/trae/v1/chat/completions `
  -H "Authorization: Bearer $env:GATEWAY_API_KEY" `
  -H "Content-Type: application/json" `
  -d '{"model":"your-model","messages":[{"role":"user","content":"hi"}]}'
```

Responses：

```powershell
curl.exe http://127.0.0.1:7860/trae/v1/responses `
  -H "Authorization: Bearer $env:GATEWAY_API_KEY" `
  -H "Content-Type: application/json" `
  -d '{"model":"your-model","input":"hi"}'
```

Anthropic Messages：

```powershell
curl.exe http://127.0.0.1:7860/gcli/v1/messages `
  -H "Authorization: Bearer $env:GATEWAY_API_KEY" `
  -H "Content-Type: application/json" `
  -H "anthropic-version: 2023-06-01" `
  -d '{"model":"your-model","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}'
```

## 默认端口

| 项目 | 端口 |
|---|---:|
| 网关 API | 7860 |
| 本机管理 | 7862 |
| WorkBuddy | 7863 |
| Qoder 控制台 | 3588 |
| Qoder Bridge | 8963 / 8964 |
| OpenCode | 10010 |
| Trae | 8000 |
| Trae 授权助手 | 8765 |
| GCLI | 7861 |

## 配置

完整示例见 [`config.example.json`](./config.example.json)。项目目录默认指向仓库内的 `upstreams/*`，也可用环境变量覆盖：

| 变量 | 默认值 |
|---|---|
| `WORKBUDDY_DIR` | `<repo>/upstreams/workbuddy` |
| `QODER_DIR` | `<repo>/upstreams/qoder` |
| `OPENCODE_DIR` | `<repo>/upstreams/opencode` |
| `TRAE_DIR` | `<repo>/upstreams/trae` |
| `GCLI_DIR` | `<repo>/upstreams/gcli` |

`native` 声明上游原生协议；`chat` 是所有项目的转换回退协议。`api_key_env` 只保存环境变量名，不保存密钥值。

## 开发

```powershell
go test ./...
go vet ./...
npm run build --prefix web
go build ./cmd/server
```

前端构建产物位于 `internal/panel/static/`，通过 Go `embed` 打进二进制。修改 Vue 后先构建前端，再编译 Go。

## 许可证与来源

五个上游项目保留各自许可证，详见 [`THIRD_PARTY_NOTICES.md`](./THIRD_PARTY_NOTICES.md) 及 `upstreams/*/LICENSE`。其中 Qoder 为 GPL-3.0，GCLI 为 CNC-1.0（含非商业限制）。顶层组合仓库不声称使用单一许可证。
