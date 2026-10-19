[CmdletBinding()]
param(
    [string]$BindAddress = "127.0.0.1",
    [int]$ApiPort = 8000,
    [int]$HelperPort = 8765
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$traeDir = if ($env:TRAE_DIR) { $env:TRAE_DIR } else { Join-Path $root "upstreams\trae" }
$python = Join-Path $traeDir ".venv\Scripts\python.exe"
$helper = Join-Path $traeDir "web_login.py"
$logDir = Join-Path $root "work\panel-logs"

if (-not (Test-Path -LiteralPath $python)) { throw "Trae Python virtual environment not found: $python" }
if (-not (Test-Path -LiteralPath $helper)) { throw "Trae authorization helper not found: $helper" }

New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$helperPid = 0

function Test-LocalPort {
    param([int]$Port)
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $result = $client.BeginConnect("127.0.0.1", $Port, $null, $null)
        return $result.AsyncWaitHandle.WaitOne(500) -and $client.Connected
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

if (-not (Test-LocalPort -Port $HelperPort)) {
    $helperStdout = Join-Path $logDir "trae-auth.log"
    $helperStderr = Join-Path $logDir "trae-auth.err.log"
    $helperProcess = Start-Process -FilePath $python -ArgumentList @($helper, "--relay", "http://127.0.0.1:$ApiPort", "--port", "$HelperPort", "--no-open") -WorkingDirectory $traeDir -WindowStyle Hidden -RedirectStandardOutput $helperStdout -RedirectStandardError $helperStderr -PassThru
    $helperPid = $helperProcess.Id

    $ready = $false
    for ($i = 0; $i -lt 20; $i++) {
        Start-Sleep -Milliseconds 250
        if (Test-LocalPort -Port $HelperPort) { $ready = $true; break }
        if ($helperProcess.HasExited) { break }
    }
    if (-not $ready) {
        if (-not $helperProcess.HasExited) { Stop-Process -Id $helperProcess.Id -Force -ErrorAction SilentlyContinue }
        throw "Trae authorization helper failed to listen on 127.0.0.1:$HelperPort. See $helperStderr"
    }
}

try {
    Push-Location -LiteralPath $traeDir
    & $python -m uvicorn src.main:app --host $BindAddress --port $ApiPort
    $exitCode = $LASTEXITCODE
    Pop-Location
    exit $exitCode
} finally {
    if ($helperPid -gt 0) {
        $helperProcess = Get-Process -Id $helperPid -ErrorAction SilentlyContinue
        if ($null -ne $helperProcess) { Stop-Process -Id $helperPid -Force -ErrorAction SilentlyContinue }
    }
}
