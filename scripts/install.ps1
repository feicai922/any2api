[CmdletBinding()]
param(
    [switch]$CheckOnly
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$workspace = Split-Path -Parent $root
$workbuddy = if ($env:WORKBUDDY_DIR) { $env:WORKBUDDY_DIR } else { Join-Path $workspace "workbuddy2api-panel" }
$qoder = Join-Path $workspace "qoder2api"
$opencode = Join-Path $workspace "opencode2api"
$trae = Join-Path $workspace "trae2api"
$gcli = Join-Path $workspace "gcli2api"

$checks = [System.Collections.Generic.List[object]]::new()
function Add-Check {
    param([string]$Name, [bool]$OK, [string]$Detail, [string]$Fix = "")
    $script:checks.Add([pscustomobject]@{ Name = $Name; OK = $OK; Detail = $Detail; Fix = $Fix })
}
function Test-Tool {
    param([string]$Name)
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if ($cmd) { Add-Check $Name $true $cmd.Source; return $true }
    Add-Check $Name $false "未找到" "安装 $Name 并加入 PATH"
    return $false
}
function Test-PythonVenv {
    param([string]$Project, [string]$Dir)
    $python = Join-Path $Dir ".venv\Scripts\python.exe"
    if (-not (Test-Path -LiteralPath $python)) {
        Add-Check "$Project/.venv" $false "虚拟环境不存在" "在 $Dir 创建 .venv 并安装 requirements.txt"
        return
    }
    Add-Check "$Project/.venv" $true $python
    $result = & $python -m pip check 2>&1
    $ok = $LASTEXITCODE -eq 0
    Add-Check "$Project/pip-check" $ok ($result -join " ") "在该虚拟环境执行 python -m pip install -r requirements.txt"
}

Write-Host "== 依赖检查 ==" -ForegroundColor Cyan
$goOK = Test-Tool "go"
$nodeOK = Test-Tool "node"
$npmOK = Test-Tool "npm"
$pyOK = Test-Tool "python"

if ($goOK) { Add-Check "Go 版本" $true ((& go version) -join "") }
if ($nodeOK) { Add-Check "Node 版本" $true ((& node --version) -join "") }
if ($npmOK) { Add-Check "npm 版本" $true ((& npm --version) -join "") }
if ($pyOK) { Add-Check "Python 版本" $true ((& python --version) -join "") }

$wbExe = Join-Path $workbuddy "wb2api.exe"
$wbConfig = Join-Path $workbuddy "config.json"
Add-Check "workbuddy/wb2api.exe" (Test-Path -LiteralPath $wbExe) $(if (Test-Path -LiteralPath $wbExe) { $wbExe } else { "未找到" }) "在该目录执行 .\start-panel.ps1 或准备 wb2api.exe"
Add-Check "workbuddy/config.json" (Test-Path -LiteralPath $wbConfig) $(if (Test-Path -LiteralPath $wbConfig) { $wbConfig } else { "未找到" }) "首次运行 wb2api.exe 会生成配置；不要在终端打印配置内容"
$qoderExe = Join-Path $qoder "bin\qoder2api.exe"
Add-Check "qoder/bin/qoder2api.exe" (Test-Path -LiteralPath $qoderExe) $(if (Test-Path -LiteralPath $qoderExe) { $qoderExe } else { "未找到" }) "在 qoder2api 目录执行 go build -o bin\qoder2api.exe ."

$nodeModules = Join-Path $opencode "node_modules"
Add-Check "opencode/node_modules" (Test-Path -LiteralPath $nodeModules) $(if (Test-Path -LiteralPath $nodeModules) { $nodeModules } else { "未找到" }) "在 opencode2api 目录执行 npm ci"
$openCodeCLI = Join-Path $env:APPDATA "npm\opencode.cmd"
Add-Check "OpenCode CLI" (Test-Path -LiteralPath $openCodeCLI) $(if (Test-Path -LiteralPath $openCodeCLI) { $openCodeCLI } else { "未找到" }) "执行 npm install -g @opencode-ai/cli"

Test-PythonVenv "trae" $trae
Test-PythonVenv "gcli" $gcli
Add-Check "PowerShell 7" ($PSVersionTable.PSVersion.Major -ge 7) $PSVersionTable.PSVersion.ToString() "安装 PowerShell 7"

$failed = @($checks | Where-Object { -not $_.OK })
$checks | Format-Table -AutoSize | Out-String | Write-Host

if ($CheckOnly) {
    if ($failed.Count -gt 0) {
        Write-Host "存在 $($failed.Count) 项未就绪。" -ForegroundColor Yellow
        exit 1
    }
    Write-Host "全部依赖已就绪。" -ForegroundColor Green
    exit 0
}

if ($failed.Count -eq 0) {
    Write-Host "全部依赖已就绪，无需安装。" -ForegroundColor Green
    exit 0
}

# 只修复能明确、幂等处理的依赖；项目配置和凭据永远不修改。
$repairFailed = $false
if (-not (Test-Path -LiteralPath $nodeModules)) {
    Write-Host "安装 opencode2api npm 依赖..." -ForegroundColor Cyan
    Push-Location $opencode
    try { npm ci; if ($LASTEXITCODE -ne 0) { $repairFailed = $true } } finally { Pop-Location }
}
foreach ($entry in @(@{Name="trae";Dir=$trae}, @{Name="gcli";Dir=$gcli})) {
    $python = Join-Path $entry.Dir ".venv\Scripts\python.exe"
    if (-not (Test-Path -LiteralPath $python)) {
        Write-Host "创建 $($entry.Name) 虚拟环境..." -ForegroundColor Cyan
        python -m venv (Join-Path $entry.Dir ".venv")
        if ($LASTEXITCODE -ne 0) { $repairFailed = $true; continue }
    }
    $req = Join-Path $entry.Dir "requirements.txt"
    if (Test-Path -LiteralPath $req) {
        Write-Host "安装 $($entry.Name) Python 依赖..." -ForegroundColor Cyan
        & $python -m pip install -r $req
        if ($LASTEXITCODE -ne 0) { $repairFailed = $true }
    }
}

if (-not (Test-Path -LiteralPath $openCodeCLI)) {
    Write-Host "提示：OpenCode CLI 需要单独安装，脚本不会猜包名或修改全局 npm 配置。" -ForegroundColor Yellow
}
if (-not (Test-Path -LiteralPath $wbExe)) {
    Write-Host "提示：WorkBuddy 可执行文件缺失；请按项目说明编译或放置 wb2api.exe。" -ForegroundColor Yellow
}
if (-not (Test-Path -LiteralPath $qoderExe)) {
    Write-Host "提示：Qoder 可执行文件缺失；请在 qoder2api 目录执行 go build -o bin\qoder2api.exe ." -ForegroundColor Yellow
}

if ($repairFailed) { exit 1 }
Write-Host "可自动修复的依赖已处理完毕；请重新运行 -CheckOnly 检查剩余项。" -ForegroundColor Green
