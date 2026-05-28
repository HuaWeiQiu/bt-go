$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root

New-Item -ItemType Directory -Force -Path "bin" | Out-Null

$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -tags desktop -ldflags "-s -w" -o "bin\bt-go-desktop-windows-amd64.exe" .

Write-Host "Built bin\bt-go-desktop-windows-amd64.exe"
Write-Host "Windows 10/11 requirement: Microsoft Edge or WebView2 Runtime for app window mode."
