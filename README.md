# bt-go

`bt-go` is a small HTTP service for adding magnet downloads and checking progress.

It uses `github.com/anacrolix/torrent` for BitTorrent protocol handling. The service does not set a download speed limit; actual speed depends on your bandwidth, peer/seed availability, tracker/DHT reachability, NAT/firewall, ISP policy and disk I/O.

## Can Users Run It Directly?

There are two ways to publish this project:

1. Source code repository
   - Users need Go installed.
   - They run `go run .` or build it locally.

2. GitHub Release binaries
   - Users download the built file and run it directly.
   - Windows users download `bt-go-desktop-windows-amd64.exe`.
   - macOS users download `bt-go-desktop` or a packaged `.app` if you create one.

Do not commit `bin/` or `downloads/` to the source repository. They are ignored by `.gitignore`. Put built binaries in GitHub Releases instead.

## Run

HTTP version:

```bash
cd /Users/tanye/test/bt-go
go mod tidy
go run .
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
go run . -addr :8088 -dir ./downloads -port 42069
```

Desktop version on macOS:

```bash
cd /Users/tanye/test/bt-go
go run -tags desktop .
```

Build desktop binary:

```bash
go build -tags desktop -o bin/bt-go-desktop .
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
```

The Windows desktop build starts the same local download service on a random `127.0.0.1` port and opens Microsoft Edge in app-window mode. If Edge app mode is unavailable, it falls back to opening the default browser. Windows 10 and Windows 11 should keep Microsoft Edge or Microsoft WebView2 Runtime installed.

For better speed, allow inbound TCP and UDP on the BitTorrent listen port in your firewall/router.

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

List tasks:

```bash
curl -s http://127.0.0.1:8088/api/tasks
```

Get one task:

```bash
curl -s http://127.0.0.1:8088/api/tasks/{infoHash}
```

Stop and remove a task from memory:

```bash
curl -X DELETE http://127.0.0.1:8088/api/tasks/{infoHash}
```

## Notes

- This is a local prototype, not a public production service.
- Do not expose it directly to the internet without authentication and access control.
- Only download content you have the legal right to download.
- Tasks are kept in memory. Restarting the service clears the task list, but downloaded files remain in the download directory.
- The current version does not cap download speed. It also does not disable upload because BitTorrent health and download speed usually depend on sharing with peers.
