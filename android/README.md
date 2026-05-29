# bt-go Android

Android uses a native Java shell and calls the Go download core through `gomobile bind`.

There is no login, registration, remote account, or cloud user system. Tasks and settings are local to the device.

## Build

```bash
./build-android.sh
```

Output:

```text
android/app/build/outputs/apk/debug/app-debug.apk
```

Required local tools:

- JDK 17
- Android SDK platform 36 and build-tools
- `gomobile`
- `gobind`

Install mobile tools:

```bash
GOBIN=$(go env GOPATH)/bin go install golang.org/x/mobile/cmd/gomobile@latest
GOBIN=$(go env GOPATH)/bin go install golang.org/x/mobile/cmd/gobind@latest
```
