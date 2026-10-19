[CmdletBinding()]
param(
    [string]$Config = "config.json",
    [switch]$Build
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

function Resolve-GatewaySecret {
    $value = $env:API_GATEWAY_ADMIN_PASSWORD
    if ([string]::IsNullOrWhiteSpace($value)) { $value = $env:PANEL_PASSWORD }
    if (-not [string]::IsNullOrWhiteSpace($value)) { return $value.Trim() }
    $secure = Read-Host "请输入工作台统一访问密码（输入内容不会显示）" -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr).Trim() }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
}

$gatewaySecret = Resolve-GatewaySecret
if ([string]::IsNullOrWhiteSpace($gatewaySecret)) { throw "统一访问密码不能为空。" }
$env:API_GATEWAY_ADMIN_PASSWORD = $gatewaySecret
$env:PANEL_PASSWORD = $gatewaySecret
$env:GATEWAY_API_KEY = $gatewaySecret

if ($Build -or -not (Test-Path "bin\api-gateway.exe")) {
    New-Item -ItemType Directory -Force -Path "bin" | Out-Null
    go build -o "bin\api-gateway.exe" ".\cmd\server"
}

if (-not (Test-Path $Config)) {
    throw "找不到配置文件：$Config。先复制 config.example.json 为 config.json。"
}

& ".\bin\api-gateway.exe" -config $Config
