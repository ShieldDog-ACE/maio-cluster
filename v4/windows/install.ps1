# maio v4 Windows installer.
#
#   .\install.ps1                 # per-user coordinator + agent
#   .\install.ps1 -Worker         # per-user agent only
#   .\install.ps1 -System         # machine-wide service (run as Administrator)
#   .\install.ps1 -System -Worker -JoinCode "MAIO1-..."
#   .\install.ps1 -Web                 # also install the optional web console
param(
    [switch]$System,
    [switch]$Worker,
    [switch]$NoStart,
    [switch]$Web,
    [string]$JoinCode = ""
)
$ErrorActionPreference = "Stop"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path

$scopeArgs = @()
$pathScope = "User"
if ($System) {
    $root = Join-Path $env:ProgramFiles "maio"
    $scopeArgs = @("--system")
    $pathScope = "Machine"
} else {
    $root = Join-Path $env:LOCALAPPDATA "maio"
}
$exe = Join-Path $root "maio.exe"

New-Item -ItemType Directory -Force -Path $root, (Join-Path $root "bin") | Out-Null
Copy-Item (Join-Path $here "maio.exe") $exe -Force
if (Test-Path (Join-Path $here "bin")) {
    Copy-Item (Join-Path $here "bin\*") (Join-Path $root "bin") -Recurse -Force
}

# Put maio on PATH for the selected scope.
$currentPath = [Environment]::GetEnvironmentVariable("Path", $pathScope)
if ($currentPath -notlike "*$root*") {
    [Environment]::SetEnvironmentVariable("Path", "$currentPath;$root", $pathScope)
}

if ($JoinCode) {
    & $exe @scopeArgs cluster join $JoinCode
    if ($LASTEXITCODE -ne 0) { throw "join failed" }
} else {
    if ($System) {
        $cfg = Join-Path $env:ProgramData "maio\cluster.toml"
    } else {
        $cfg = Join-Path $env:APPDATA "maio\cluster.toml"
    }
    if (-not (Test-Path $cfg)) {
        & $exe @scopeArgs init --force
        if ($LASTEXITCODE -ne 0) { throw "init failed" }
    }
}

if ($Web) {
    & $exe @scopeArgs web install
    if ($LASTEXITCODE -ne 0) { Write-Warning "web install failed; run: $exe $($scopeArgs -join ' ') web install" }
}

if (-not $NoStart) {
    $installArgs = @("service", "install")
    if ($Worker) { $installArgs += "--worker" }
    & $exe @scopeArgs @installArgs
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "service install failed; run: $exe $($scopeArgs -join ' ') cluster up"
    }
}

Write-Host ""
Write-Host "maio installed in $root"
Write-Host "  status:  & '$exe' $($scopeArgs -join ' ') cluster nodes"
Write-Host "  join:    & '$exe' $($scopeArgs -join ' ') cluster join-code"
Write-Host "  config:  & '$exe' $($scopeArgs -join ' ') config path"
if (-not $Worker) {
    Write-Host ""
    Write-Host "Join code:"
    & $exe @scopeArgs cluster join-code
}
