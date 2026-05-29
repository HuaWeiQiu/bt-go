#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
mkdir -p bin
mkdir -p dist

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -tags desktop -trimpath -ldflags "-s -w" -o bin/bt-go-desktop-windows-amd64.exe ./cmd/bt-go

rm -f dist/bt-go-desktop-windows-amd64.zip dist/bt-go-desktop-windows-amd64.zip.sha256
(cd bin && zip -q ../dist/bt-go-desktop-windows-amd64.zip bt-go-desktop-windows-amd64.exe)
(cd dist && shasum -a 256 bt-go-desktop-windows-amd64.zip > bt-go-desktop-windows-amd64.zip.sha256)

echo "Built bin/bt-go-desktop-windows-amd64.exe"
echo "Packaged dist/bt-go-desktop-windows-amd64.zip"
echo "Checksum dist/bt-go-desktop-windows-amd64.zip.sha256"
echo "Windows 10/11 requirement: Microsoft Edge or WebView2 Runtime for app window mode."
