#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
mkdir -p bin

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -tags desktop -ldflags "-s -w" -o bin/bt-go-desktop-windows-amd64.exe .

echo "Built bin/bt-go-desktop-windows-amd64.exe"
echo "Windows 10/11 requirement: Microsoft Edge or WebView2 Runtime for app window mode."
