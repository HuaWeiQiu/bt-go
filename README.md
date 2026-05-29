# bt-go

`bt-go` is a small HTTP service for adding magnet downloads and checking progress.

It uses `github.com/anacrolix/torrent` for BitTorrent protocol handling. The service does not set a download speed limit; actual speed depends on your bandwidth, peer/seed availability, tracker/DHT reachability, NAT/firewall, ISP policy and disk I/O.

The app runs each task with its own watcher goroutine while the torrent library handles DHT,
tracker announces, peer connections, and piece requests internally. Runtime settings let you cap
how many tasks actively download file data at the same time and optionally apply a global download
rate limit. Set the rate limit to `0` for unlimited speed.

Tasks and settings are persisted in `bt-go-state.db` under the download directory. Restarting the
service restores the saved magnet tasks and runtime settings, then resumes metadata lookup or file
downloading according to the active-download queue.

Metadata-ready tasks expose file-level progress. Multi-file torrents can be narrowed to selected
files without adding accounts, login, or any remote service.

## Can Users Run It Directly?

There are two ways to publish this project:

1. Source code repository
   - Users need Go installed.
   - They run `go run ./cmd/bt-go` or build it locally.

2. GitHub Release binaries
   - Users download the built file and run it directly.
   - Windows users download `bt-go-desktop-windows-amd64.exe`.
   - macOS users download `bt-go-desktop` or a packaged `.app` if you create one.
   - Android users download `bt-go-android-debug.apk` and install it manually.

Do not commit `bin/` or `downloads/` to the source repository. They are ignored by `.gitignore`. Put built binaries in GitHub Releases instead.

## Run

HTTP version:

```bash
cd /Users/tanye/test/bt-go
go mod tidy
go run ./cmd/bt-go
```

Open:

```text
http://127.0.0.1:8088
```

Default values:

| Name | Default | Purpose |
| --- | --- | --- |
| `BT_GO_ADDR` | `:8088` | HTTP listen address |
| `BT_GO_DIR` | `./downloads` | Download directory |
| `BT_GO_LISTEN_PORT` | `42069` | BitTorrent TCP/uTP listen port |

Equivalent flags:

```bash
go run ./cmd/bt-go -addr :8088 -dir ./downloads -port 42069
```

Desktop version on macOS:

```bash
cd /Users/tanye/test/bt-go
go run -tags desktop ./cmd/bt-go
```

Build desktop binary:

```bash
go build -tags desktop -o bin/bt-go-desktop ./cmd/bt-go
./bin/bt-go-desktop
```

The desktop version opens a native WebView window and starts its local HTTP service on a random `127.0.0.1` port, so it will not conflict with the HTTP version on `8088`. The BitTorrent listen port still defaults to `42069`.

Desktop version on Windows 10 / Windows 11:

```powershell
cd C:\path\to\bt-go
powershell -ExecutionPolicy Bypass -File .\build-windows.ps1
.\bin\bt-go-desktop-windows-amd64.exe
```

Build from macOS/Linux:

```bash
cd /Users/tanye/test/bt-go
./build-windows.sh
```

Windows output:

```text
bin/bt-go-desktop-windows-amd64.exe
dist/bt-go-desktop-windows-amd64.zip
dist/bt-go-desktop-windows-amd64.zip.sha256
```

The Windows desktop build starts the same local download service on a random `127.0.0.1` port and opens Microsoft Edge in app-window mode. If Edge app mode is unavailable, it falls back to opening the default browser. Windows 10 and Windows 11 should keep Microsoft Edge or Microsoft WebView2 Runtime installed.

For better speed, allow inbound TCP and UDP on the BitTorrent listen port in your firewall/router.

Release artifacts include raw desktop executables, zipped desktop copies, and the Android debug APK:

```text
bt-go-desktop-windows-amd64.exe
bt-go-desktop-windows-amd64.zip
bt-go-desktop-darwin-arm64
bt-go-desktop-darwin-arm64.zip
bt-go-desktop-linux-amd64
bt-go-desktop-linux-amd64.zip
bt-go-android-debug.apk
```

GitHub Actions release packaging is available in `.github/workflows/release.yml`. Push a tag like
`v0.1.0` or run the workflow manually to build desktop artifacts, the Android debug APK, zipped
copies, and SHA256 files.

## API

Parse a magnet link:

```bash
curl -s http://127.0.0.1:8088/api/parse \
  -H 'Content-Type: application/json' \
  -d '{"magnet":"magnet:?xt=urn:btih:..."}'
```

Add a download:

```bash
curl -s http://127.0.0.1:8088/api/tasks \
  -H 'Content-Type: application/json' \
  -d '{"magnet":"magnet:?xt=urn:btih:..."}'
```

Upload a `.torrent` file and start it as a download task:

```bash
curl -s http://127.0.0.1:8088/api/torrents \
  -F 'file=@ubuntu-26.04-desktop-amd64.iso.torrent;type=application/x-bittorrent'
```

List tasks:

```bash
curl -s http://127.0.0.1:8088/api/tasks
```

Get one task:

```bash
curl -s http://127.0.0.1:8088/api/tasks/{infoHash}
```

Pause or resume a task:

```bash
curl -X POST http://127.0.0.1:8088/api/tasks/{infoHash}/pause
curl -X POST http://127.0.0.1:8088/api/tasks/{infoHash}/resume
```

Pause or resume all tasks:

```bash
curl -X POST http://127.0.0.1:8088/api/tasks/pause-all
curl -X POST http://127.0.0.1:8088/api/tasks/resume-all
```

Move a task in the queue:

```bash
curl -s -X POST http://127.0.0.1:8088/api/tasks/{infoHash}/move \
  -H 'Content-Type: application/json' \
  -d '{"direction":"top"}'
```

`direction` can be `top`, `up`, `down`, or `bottom`.

Refresh task discovery sources:

```bash
curl -X POST http://127.0.0.1:8088/api/tasks/{infoHash}/refresh-discovery
```

This re-adds the task trackers and starts a short DHT announce pass. It is useful when a task has
metadata but no active peers, or when it sits at `0 B/s` for a while.
The task status exposes `discoveryStatus`, `discoveryPeers`, `discoverySeeders`,
`discoveryTrackers`, and `discoveryCheckedAt` so the UI can show whether the resource was confirmed
from tracker/DHT discovery.

Select files in a metadata-ready task:

```bash
curl -s -X PUT http://127.0.0.1:8088/api/tasks/{infoHash}/files \
  -H 'Content-Type: application/json' \
  -d '{"files":["video.mp4","subtitle.srt"]}'
```

Set file priorities in a metadata-ready task:

```bash
curl -s -X PUT http://127.0.0.1:8088/api/tasks/{infoHash}/files \
  -H 'Content-Type: application/json' \
  -d '{"priorities":{"video.mp4":"high","sample.txt":"skip","subtitle.srt":"normal"}}'
```

Priority values are `high`, `normal`, and `skip`. The older `files` list is still accepted and now
treats the listed files as `high` priority by default. Send an empty file list or an empty priority
map to skip all files until another selection is saved.

Open the download directory or a task/file path on the local machine:

```bash
curl -X POST http://127.0.0.1:8088/api/open-download-dir
curl -s -X POST http://127.0.0.1:8088/api/tasks/{infoHash}/open \
  -H 'Content-Type: application/json' \
  -d '{"path":"video.mp4"}'
```

The open endpoint only accepts paths belonging to the task and refuses paths outside the configured
download directory.

Remove a task from memory and the persisted task list:

```bash
curl -X DELETE http://127.0.0.1:8088/api/tasks/{infoHash}
```

Deleting a task also removes it from the local persisted task list. Existing downloaded files are
not deleted unless `deleteFiles=true` is set:

```bash
curl -X DELETE 'http://127.0.0.1:8088/api/tasks/{infoHash}?deleteFiles=true'
```

Read runtime settings:

```bash
curl -s http://127.0.0.1:8088/api/settings
```

Update runtime settings:

```bash
curl -s -X PUT http://127.0.0.1:8088/api/settings \
  -H 'Content-Type: application/json' \
  -d '{"maxActiveDownloads":3,"downloadRateLimitBytes":0,"waitForFileSelection":false}'
```

`maxActiveDownloads` controls how many metadata-ready tasks actively pull file data. Metadata
lookup may still run for queued magnet tasks so they can be ready when a slot opens.
`downloadRateLimitBytes` is a global byte-per-second cap. Use `0` for unlimited.
`waitForFileSelection` keeps metadata-ready tasks in `awaiting_selection` until a file selection is
saved, which is useful for multi-file torrents.

Settings are persisted automatically after updates.

Task status includes `diagnostic`, `diagnosticCode`, `dhtEnabled`, `dhtServers`, `listenAddrs`,
`knownPeers`, `discoveryStatus`, `discoveryPeers`, `etaSeconds`, `stalled`, `stalledSeconds`, and
`files` when metadata is ready. The diagnostic fields explain whether a task is waiting for
metadata, has no peers, is connecting to peers, has no seeders, is stalled, or is downloading
normally. The aggregate size/progress is calculated from the selected files, so skipped files do not
inflate the ETA.

## Verification

Local checks run with Go 1.26.3:

```bash
GOTOOLCHAIN=go1.26.3 go test ./...
GOTOOLCHAIN=go1.26.3 go test -tags desktop ./...
GOTOOLCHAIN=go1.26.3 go vet ./...
GOTOOLCHAIN=go1.26.3 go vet -tags desktop ./...
```

Live HTTP verification was also run against the WebTorrent public fixture `leaves.torrent`. The
single selected file `Leaves of Grass by Walt Whitman.epub` completed successfully with
`totalBytes=362017`, `completedBytes=362017`, default `high` priority, and SHA256
`d6d19f442444b9cd84e64441772f113e7b6844058dcee08238093c99faab083d`.

## Notes

- This is a local prototype, not a public production service.
- Do not expose it directly to the internet without authentication and access control.
- Only download content you have the legal right to download.
- Tasks and settings are persisted locally in the download directory.
- Upload is not disabled because BitTorrent health and download speed usually depend on sharing with peers.
