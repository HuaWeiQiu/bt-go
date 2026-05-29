#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

export JAVA_HOME="${JAVA_HOME:-/opt/homebrew/opt/openjdk@17/libexec/openjdk.jdk/Contents/Home}"
export ANDROID_HOME="${ANDROID_HOME:-/opt/homebrew/share/android-commandlinetools}"
export ANDROID_SDK_ROOT="${ANDROID_SDK_ROOT:-$ANDROID_HOME}"
export PATH="$(go env GOPATH)/bin:$PATH"

if [ -z "${ANDROID_NDK_HOME:-}" ] && [ -n "${ANDROID_HOME:-}" ] && [ -d "$ANDROID_HOME/ndk/28.2.13676358" ]; then
  export ANDROID_NDK_HOME="$ANDROID_HOME/ndk/28.2.13676358"
fi

if [ -z "${BT_GO_VERSION_NAME:-}" ] && [ "${GITHUB_REF_TYPE:-}" = "tag" ] && [ -n "${GITHUB_REF_NAME:-}" ]; then
  export BT_GO_VERSION_NAME="${GITHUB_REF_NAME#v}"
fi
export BT_GO_VERSION_NAME="${BT_GO_VERSION_NAME:-0.1.9}"
export BT_GO_VERSION_CODE="${BT_GO_VERSION_CODE:-9}"

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

if [ -z "${ANDROID_NDK_HOME:-}" ]; then
  echo "ANDROID_NDK_HOME is required to package libc++_shared.so" >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin)
    NDK_HOST_TAG="darwin-x86_64"
    ;;
  Linux)
    NDK_HOST_TAG="linux-x86_64"
    ;;
  *)
    echo "Unsupported host OS for Android NDK: $(uname -s)" >&2
    exit 1
    ;;
esac
NDK_SYSROOT_LIB="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$NDK_HOST_TAG/sysroot/usr/lib"
if [ ! -d "$NDK_SYSROOT_LIB" ]; then
  echo "Cannot find NDK sysroot libs at $NDK_SYSROOT_LIB" >&2
  exit 1
fi

copy_cxx_shared() {
  local abi="$1"
  local triple="$2"
  local src="$NDK_SYSROOT_LIB/$triple/libc++_shared.so"
  local dst_dir="android/app/src/main/jniLibs/$abi"

  if [ ! -f "$src" ]; then
    echo "Cannot find $src" >&2
    exit 1
  fi
  mkdir -p "$dst_dir"
  cp "$src" "$dst_dir/libc++_shared.so"
}

copy_cxx_shared "arm64-v8a" "aarch64-linux-android"
copy_cxx_shared "x86_64" "x86_64-linux-android"

gradle :app:assembleDebug --no-daemon

mkdir -p dist
cp android/app/build/outputs/apk/debug/app-debug.apk dist/bt-go-android-debug.apk
(
  cd dist
  shasum -a 256 bt-go-android-debug.apk > bt-go-android-debug.apk.sha256
)

echo "Built dist/bt-go-android-debug.apk"
