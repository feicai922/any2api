[CmdletBinding()]
param(
    [switch]$CheckOnly,
    [switch]$SkipWebBuild
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
$upstreams = Join-Path $root "upstreams"

$projects = [ordered]@{
    workbuddy = Join-Path $upstreams "workbuddy"
    qoder      = Join-Path $upstreams "qoder"
    opencode   = Join-Path $upstreams "opencode"
    trae       = Join-Path $upstreams "trae"
    gcli       = Join-Path $upstreams "gcli"
}

$checks = [System.Collections.Generic.List[object]]::new()
function Add-Check {
    param([string]$Name, [bool]$OK, [string]$Detail, [string]$Fix = "")
    $script:checks.Add([pscustomobject]@{ Name = $Name; OK = $OK; Detail = $Detail; Fix = $Fix })
}

function Test-Tool {
    param([string]$Name, [string]$VersionArg = "--version")
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if (-not $cmd) {
        Add-Check $Name $false "not found" "install $Name and add it to PATH"
        return $false
    }
    $version = (& $cmd.Source $VersionArg 2>&1 | Select-Object -First 1)
    Add-Check $Name $true (($version -join " ").Trim())
    return $true
}

function Test-PythonVenv {
    param([string]$Project, [string]$Dir)
    $python = Join-Path $Dir ".venv\Scripts\python.exe"
    if (-not (Test-Path -LiteralPath $python)) {
        Add-Check "$Project/.venv" $false "missing" "run scripts\install.ps1"
        return $false
    }
    $result = & $python -m pip check 2>&1
    $ok = $LASTEXITCODE -eq 0
    Add-Check "$Project/pip-check" $ok (($result -join " ").Trim()) "run scripts\install.ps1"
    return $ok
}

function Invoke-Step {
    param([string]$Name, [scriptblock]$Action)
    Write-Host "==> $Name" -ForegroundColor Cyan
    & $Action
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE"
    }
}

function Ensure-FileFromExample {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path) {
        return
    }
    $example = "$Path.example"
    if (-not (Test-Path -LiteralPath $example)) {
        throw "missing template: $example"
    }
    Copy-Item -LiteralPath $example -Destination $Path
}

Write-Host "== any2api dependency check ==" -ForegroundColor Cyan
$goOK = Test-Tool "go" "version"
$nodeOK = Test-Tool "node" "--version"
$npmOK = Test-Tool "npm" "--version"
$pyOK = Test-Tool "python" "--version"
$pwshOK = $PSVersionTable.PSVersion.Major -ge 7
Add-Check "PowerShell 7" $pwshOK $PSVersionTable.PSVersion.ToString() "install PowerShell 7"

foreach ($entry in $projects.GetEnumerator()) {
    Add-Check "upstream/$($entry.Key)" (Test-Path -LiteralPath $entry.Value) $entry.Value "run git submodule update --init --recursive (source is bundled in this repository)"
}

$workbuddyExe = Join-Path $projects.workbuddy "wb2api.exe"
$qoderExe = Join-Path $projects.qoder "bin\qoder2api.exe"
$openCodeCLI = if ($env:APPDATA) { Join-Path $env:APPDATA "npm\opencode.cmd" } else { "opencode" }
$checks.Add([pscustomobject]@{ Name = "workbuddy/wb2api.exe"; OK = (Test-Path -LiteralPath $workbuddyExe); Detail = $workbuddyExe; Fix = "run scripts\install.ps1" })
$checks.Add([pscustomobject]@{ Name = "qoder/bin/qoder2api.exe"; OK = (Test-Path -LiteralPath $qoderExe); Detail = $qoderExe; Fix = "run scripts\install.ps1" })
$checks.Add([pscustomobject]@{ Name = "OpenCode CLI"; OK = (Test-Path -LiteralPath $openCodeCLI); Detail = $openCodeCLI; Fix = "run scripts\install.ps1" })
$checks.Add([pscustomobject]@{ Name = "opencode/node_modules"; OK = (Test-Path -LiteralPath (Join-Path $projects.opencode "node_modules")); Detail = (Join-Path $projects.opencode "node_modules"); Fix = "run scripts\install.ps1" })
Test-PythonVenv "trae" $projects.trae | Out-Null
Test-PythonVenv "gcli" $projects.gcli | Out-Null
$webBuild = Join-Path $root "internal\panel\static\index.html"
$checks.Add([pscustomobject]@{ Name = "web build"; OK = (Test-Path -LiteralPath $webBuild); Detail = $webBuild; Fix = "run scripts\install.ps1" })
$checks.Add([pscustomobject]@{ Name = "gateway binary"; OK = (Test-Path -LiteralPath (Join-Path $root "bin\api-gateway.exe")); Detail = (Join-Path $root "bin\api-gateway.exe"); Fix = "run scripts\install.ps1" })

$checks | Format-Table -AutoSize | Out-String | Write-Host
$failed = @($checks | Where-Object { -not $_.OK })
if ($CheckOnly) {
    if ($failed.Count -gt 0) {
        Write-Host "$($failed.Count) item(s) are not ready." -ForegroundColor Yellow
        exit 1
    }
    Write-Host "All dependencies are ready." -ForegroundColor Green
    exit 0
}

if (-not ($goOK -and $nodeOK -and $npmOK -and $pyOK -and $pwshOK)) {
    throw "Required tools are missing. Install Go 1.22+, Node.js 20+, Python 3.12+, and PowerShell 7 first."
}

New-Item -ItemType Directory -Force -Path (Join-Path $root "bin") | Out-Null
Ensure-FileFromExample (Join-Path $root "config.json")

# WorkBuddy: build the executable. The service generates a real config on first start.
Push-Location $projects.workbuddy
try {
    Invoke-Step "build WorkBuddy" { go build -trimpath -ldflags="-s -w" -o wb2api.exe ./cmd/server }
} finally {
    Pop-Location
}

# Qoder: build the bridge and console.
Push-Location $projects.qoder
try {
    New-Item -ItemType Directory -Force -Path "bin" | Out-Null
    Invoke-Step "build Qoder" { go build -trimpath -ldflags="-s -w" -o bin\qoder2api.exe . }
} finally {
    Pop-Location
}

# OpenCode: local npm dependencies plus the CLI the proxy invokes.
Push-Location $projects.opencode
try {
    Invoke-Step "install OpenCode dependencies" { npm ci }
} finally {
    Pop-Location
}
Invoke-Step "install OpenCode CLI" { npm install -g opencode-ai@1.19.0 }

# Python projects use isolated virtual environments.
foreach ($entry in @(@{ Name = "Trae"; Dir = $projects.trae }, @{ Name = "GCLI"; Dir = $projects.gcli })) {
    $python = Join-Path $entry.Dir ".venv\Scripts\python.exe"
    if (-not (Test-Path -LiteralPath $python)) {
        Invoke-Step "create $($entry.Name) virtual environment" { python -m venv (Join-Path $entry.Dir ".venv") }
    }
    $requirements = Join-Path $entry.Dir "requirements.txt"
    Invoke-Step "upgrade $($entry.Name) pip" { & $python -m pip install --upgrade pip }
    Invoke-Step "install $($entry.Name) Python dependencies" { & $python -m pip install -r $requirements }
}

if (-not $SkipWebBuild) {
    Push-Location (Join-Path $root "web")
    try {
        Invoke-Step "install panel dependencies" { npm ci }
        Invoke-Step "build panel" { npm run build }
    } finally {
        Pop-Location
    }
}

Push-Location $root
try {
    Invoke-Step "build gateway" { go build -trimpath -ldflags="-s -w" -o bin\api-gateway.exe ./cmd/server }
} finally {
    Pop-Location
}

Write-Host "Install complete. Start with scripts\start.ps1; the administrator password is requested at runtime and is never stored by this script." -ForegroundColor Green
