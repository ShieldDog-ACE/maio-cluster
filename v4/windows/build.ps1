# Build the Windows product (run on Windows with Go + PowerShell).
#
#   .\windows\build.ps1
#   .\windows\build.ps1 -Arch arm64
#   .\windows\build.ps1 -RpcBin C:\llama\ggml-rpc-server.exe `
#                       -LlamaBin C:\llama\llama-server.exe
#
# The Go binary cross-compiles from any OS, but the llama.cpp runtime must be
# built for Windows (Visual Studio or the prebuilt release zips). If no
# -RpcBin/-LlamaBin is given the package still installs and can join as a
# control/route node.
param(
    [string]$Version = "4.0.0",
    [string]$Arch = "amd64",
    [string]$RpcBin = "",
    [string]$LlamaBin = ""
)
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)

$out = "dist\windows-$Arch"
if (Test-Path $out) { Remove-Item -Recurse -Force $out }
New-Item -ItemType Directory -Force -Path "$out\bin" | Out-Null

Write-Host "building maio $Version for windows/$Arch"
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = $Arch
go build -trimpath `
    -ldflags "-s -w -X github.com/ShieldDog-ACE/maio-cluster/v4/core/version.Version=$Version" `
    -o "$out\maio.exe" .\windows\cmd\maio
if ($LASTEXITCODE -ne 0) { throw "go build failed" }

if ($RpcBin -and (Test-Path $RpcBin)) {
    Copy-Item $RpcBin "$out\bin\ggml-rpc-server.exe"
    Write-Host "bundled RPC server: $RpcBin"
} else {
    Write-Warning "no -RpcBin: distributed shards need ggml-rpc-server.exe"
}
if ($LlamaBin -and (Test-Path $LlamaBin)) {
    Copy-Item $LlamaBin "$out\bin\llama-server.exe"
    Write-Host "bundled llama-server: $LlamaBin"
} else {
    Write-Warning "no -LlamaBin: local/frontend inference needs llama-server.exe"
}

Copy-Item .\windows\install.ps1 "$out\install.ps1"
Copy-Item .\windows\README.md "$out\README.md"
Copy-Item .\README.md "$out\README.root.md" -ErrorAction SilentlyContinue

$zip = "dist\maio-windows-$Arch.zip"
if (Test-Path $zip) { Remove-Item -Force $zip }
Compress-Archive -Path "$out\*" -DestinationPath $zip
Write-Host "built: $zip"
Get-ChildItem $out | Format-Table Name, Length
