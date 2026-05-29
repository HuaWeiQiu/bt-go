$ErrorActionPreference = "Stop"

$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root

New-Item -ItemType Directory -Force -Path "bin" | Out-Null
New-Item -ItemType Directory -Force -Path "dist" | Out-Null

$env:GOOS = "windows"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -tags desktop -trimpath -ldflags "-s -w" -o "bin\bt-go-desktop-windows-amd64.exe" ./cmd/bt-go

$zipPath = "dist\bt-go-desktop-windows-amd64.zip"
$shaPath = "$zipPath.sha256"
Remove-Item -Force -ErrorAction SilentlyContinue $zipPath, $shaPath
Compress-Archive -Path "bin\bt-go-desktop-windows-amd64.exe" -DestinationPath $zipPath
$hash = Get-FileHash -Algorithm SHA256 $zipPath
"$($hash.Hash.ToLower())  bt-go-desktop-windows-amd64.zip" | Set-Content -Encoding ascii $shaPath

Write-Host "Built bin\bt-go-desktop-windows-amd64.exe"
Write-Host "Packaged dist\bt-go-desktop-windows-amd64.zip"
Write-Host "Checksum dist\bt-go-desktop-windows-amd64.zip.sha256"
Write-Host "Windows 10/11 requirement: Microsoft Edge or WebView2 Runtime for app window mode."
