#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

export JAVA_HOME="${JAVA_HOME:-/opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home}"
export ANDROID_HOME="${ANDROID_HOME:-/opt/homebrew/share/android-commandlinetools}"
export ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-$ANDROID_HOME}"
export PATH="$(go env GOPATH)/bin:$PATH"

if ! command -v gomobile >/dev/null 2>&1; then
  echo "gomobile is required. Install with: GOBIN=$(go env GOPATH)/bin go install golang.org/x/mobile/cmd/gomobile@latest" >&2
  exit 1
fi

if ! command -v gobind >/dev/null 2>&1; then
  echo "gobind is required. Install with: GOBIN=$(go env GOPATH)/bin go install golang.org/x/mobile/cmd/gobind@latest" >&2
  exit 1
fi

if [ "${BT_GO_RUN_GOMOBILE_INIT:-0}" = "1" ]; then
  gomobile init
fi

mkdir -p android/app/libs
gomobile bind \
  -target=android/arm64,android/amd64 \
  -androidapi=26 \
  -o android/app/libs/btgo.aar \
  ./mobile/btgo

gradle :app:assembleDebug

echo "Built android/app/build/outputs/apk/debug/app-debug.apk"
