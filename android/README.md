# bt-go Android

Android uses a native Java shell and calls the Go download core through `gomobile bind`.

There is no login, registration, remote account, or cloud user system. Tasks and settings are local to the device.

## Build

```bash
./build-android.sh
```

Output:

```text
dist/bt-go-android-debug.apk
dist/bt-go-android-debug.apk.sha256
```

Required local tools:

- JDK 17
- Android SDK platform 36 and build-tools
- Android NDK 28.2.13676358
- `gomobile`
- `gobind`

Install mobile tools:

```bash
GOBIN=$(go env GOPATH)/bin go install golang.org/x/mobile/cmd/gomobile@latest
GOBIN=$(go env GOPATH)/bin go install golang.org/x/mobile/cmd/gobind@latest
```
