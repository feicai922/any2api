[CmdletBinding()]
param(
    [ValidateSet("status", "restart")]
    [string]$Action = "status",
    [string]$Config = "config.json"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root
if (-not (Test-Path $Config)) { throw "config not found: $Config" }

function Resolve-GatewaySecret {
    $value = $env:API_GATEWAY_ADMIN_PASSWORD
    if ([string]::IsNullOrWhiteSpace($value)) { $value = $env:PANEL_PASSWORD }
    if (-not [string]::IsNullOrWhiteSpace($value)) { return $value.Trim() }
    $secure = Read-Host "请输入工作台统一访问密码（输入内容不会显示）" -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr).Trim() }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }
}

function Set-GatewaySecret {
    $script:gatewaySecret = Resolve-GatewaySecret
    if ([string]::IsNullOrWhiteSpace($script:gatewaySecret)) { throw "统一访问密码不能为空。" }
    $env:API_GATEWAY_ADMIN_PASSWORD = $script:gatewaySecret
    $env:PANEL_PASSWORD = $script:gatewaySecret
    $env:GATEWAY_API_KEY = $script:gatewaySecret
}

function Initialize-PanelSession {
    if ($null -eq $script:gatewaySecret) { Set-GatewaySecret }
    $session = New-Object Microsoft.PowerShell.Commands.WebRequestSession
    Invoke-WebRequest -Uri "http://127.0.0.1:7862/admin" -Method Post -Body @{ password = $script:gatewaySecret; next = "/" } -WebSession $session -MaximumRedirection 0 -SkipHttpErrorCheck | Out-Null
    $script:panelSession = $session
}

function Get-PanelState {
    Initialize-PanelSession
    Invoke-RestMethod -Uri "http://127.0.0.1:7862/api/projects" -WebSession $script:panelSession -TimeoutSec 5
}
function Restart-Gateway {
    Set-GatewaySecret
    $exe = Join-Path $root "bin\api-gateway.exe"
    $processes = Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -ieq $exe }
    foreach ($process in $processes) { Stop-Process -Id $process.ProcessId -Force }
    Start-Sleep -Milliseconds 500
    $stdout = Join-Path $root "work\gateway.log"
    $stderr = Join-Path $root "work\gateway.err.log"
    Start-Process -FilePath $exe -ArgumentList @("-config", (Join-Path $root $Config)) -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr | Out-Null
    Start-Sleep -Seconds 3
}
switch ($Action) {
    "status" { Get-PanelState | ConvertTo-Json -Depth 8 }
    "restart" { Restart-Gateway; Get-PanelState | ConvertTo-Json -Depth 8 }
}
