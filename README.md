# Hydra

Hydra is a Linux-focused, multi-threaded download manager written in Go. It
provides a headless daemon, a Bubble Tea terminal interface, a command-line
client, and a Firefox extension that forwards browser downloads to Hydra.

The daemon owns downloading, queueing, persistence, authentication, and
filesystem operations. The TUI and CLI are clients of the daemon and do not
access the SQLite database directly.

## Features

- Parallel ranged downloads with adaptive chunk work-stealing.
- Retry handling, configurable speed limits, checksum verification, and ETA reporting.
- Linux-optimized file allocation and positional writes using `fallocate` and `pwrite`.
- SQLite persistence with WAL mode.
- Queue management and scheduled downloads.
- Bubble Tea TUI with keyboard and mouse support.
- TUI job selection, scrolling, pause, resume, delete, confirmation, refresh, and connection-error states.
- Firefox download interception with browser cookies, referer, user-agent, and authorization headers preserved for authenticated links such as Overleaf.
- Token-authenticated HTTP API.
- Unix socket IPC for the CLI.
- Debian/RPM packaging configuration and optional systemd user service.

## Architecture

```text
Firefox extension --\
                      +--> HTTP API --> hydra daemon --> downloader/storage
Hydra TUI -----------+
Hydra CLI -----------+
                      +--> SQLite database
```

### Binaries

- `hydra-daemon` - headless download service and HTTP server.
- `hydra-tui` - interactive terminal interface built with Bubble Tea.
- `hydra` - CLI client for status and job control through the Unix socket.

### Project layout

```text
cmd/
   hydra-cli/              CLI client
   hydra-tui/              Bubble Tea TUI entry point
extension/                Firefox WebExtension
pkg/downloader/           Handshake, workers, chunks, rate limiting, checksums
pkg/models/               Shared job and chunk models
pkg/storage/              SQLite, queue, HTTP API, IPC, paths, notifications
pkg/tui/                  Bubble Tea model and daemon client
packaging/                Desktop, icon, script, and systemd files
main.go                   Hydra daemon entry point
build.sh                  Release build and package script
nfpm.yaml                 Debian/RPM package definition
```

## Requirements

- Go 1.25 or newer.
- Linux for the current daemon implementation.
- Firefox for browser interception.
- `zip` to package the extension.
- `nfpm` only when building Debian/RPM packages.

The TUI itself does not require GTK, Qt, a graphical desktop, or systemd.

## Build

Build the daemon:

```bash
go build -o bin/hydra-daemon main.go
```

Build the TUI:

```bash
go build -o bin/hydra-tui ./cmd/hydra-tui
```

Build the CLI:

```bash
go build -o bin/hydra ./cmd/hydra-cli
```

Build all release binaries and packages:

```bash
./build.sh
```

The script writes binaries to `bin/` and packages to `dist/`. Generated build
artifacts are ignored by Git.

## Run Hydra

Start the daemon from the repository:

```bash
go run .
```

Or build and run it:

```bash
go build -o /tmp/hydra-daemon main.go
/tmp/hydra-daemon
```

The HTTP API listens on:

```text
http://localhost:9000
```

The daemon also creates a Unix socket under `$XDG_RUNTIME_DIR`, falling back to
`/tmp/hydra.sock`.

Start the TUI in a second terminal:

```bash
go run ./cmd/hydra-tui
```

Optional TUI flags:

```bash
go run ./cmd/hydra-tui \
   --daemon-url http://localhost:9000 \
   --token hydra_secure_token_bf1f753e
```

Start the CLI:

```bash
go run ./cmd/hydra-cli status
go run ./cmd/hydra-cli pause JOB_ID
go run ./cmd/hydra-cli resume JOB_ID
go run ./cmd/hydra-cli delete JOB_ID
```

## TUI Controls

```text
Up/Down or j/k       Select a job
PageUp/PageDown      Scroll by page
Mouse click          Select a job row
Mouse wheel          Scroll and select
p                    Pause selected job
e                    Resume selected job
d                    Start delete confirmation
y or Enter           Confirm deletion
n or Escape          Cancel deletion
r                    Refresh jobs
?                    Toggle help
q or Ctrl+C          Quit
```

Mouse input depends on terminal support. Every important action also has a
keyboard shortcut, so the TUI remains usable over SSH and on headless systems.

## HTTP API

The stable TUI API uses the `X-Hydra-Token` header. The current development
token is hard-coded in the daemon and extension; change this before a public
deployment.

List jobs:

```bash
curl -H 'X-Hydra-Token: hydra_secure_token_bf1f753e' \
   http://localhost:9000/api/jobs
```

Create a download:

```bash
curl -X POST \
   -H 'Content-Type: application/json' \
   -H 'X-Hydra-Token: hydra_secure_token_bf1f753e' \
   -d '{
      "url": "https://example.com/file.zip",
      "save_path": "/home/user/Downloads/file.zip",
      "headers": {}
   }' \
   http://localhost:9000/api/jobs
```

Control jobs:

```bash
curl -X POST -H 'X-Hydra-Token: hydra_secure_token_bf1f753e' \
   http://localhost:9000/api/jobs/JOB_ID/pause

curl -X POST -H 'X-Hydra-Token: hydra_secure_token_bf1f753e' \
   http://localhost:9000/api/jobs/JOB_ID/resume

curl -X DELETE -H 'X-Hydra-Token: hydra_secure_token_bf1f753e' \
   http://localhost:9000/api/jobs/JOB_ID
```

`DEFAULT` save paths are resolved by the daemon to the configured default or
category directory. Browser-supplied filenames are preserved.

## Firefox Extension

The extension is a persistent Firefox Manifest V2 WebExtension. It detects
download responses, preserves authenticated browser request headers, cancels
Firefox's native transfer, and creates a Hydra job.

Package it:

```bash
zip -j /home/$USER/Projects/extension.zip \
   extension/background.js extension/manifest.json
```

Install it temporarily:

1. Open `about:debugging` in Firefox.
2. Select **This Firefox**.
3. Remove any older Hydra temporary extension.
4. Click **Load Temporary Add-on**.
5. Select `extension.zip`.
6. Click **Inspect** to view the background script logs.

The extension console should show:

```text
[Hydra] background interceptor started
```

For authenticated services such as Overleaf, load a fresh download link while
logged in. Download URLs can expire and may return HTTP 403 after some time.

The extension intentionally ignores Hydra's own `localhost:9000` endpoint, but
it can intercept local test servers on other ports such as `9100`.

## Local Download Test

Create a 500 MB test file:

```bash
dd if=/dev/zero of=/tmp/hydra-progress-500mb.bin bs=1M count=500
```

Serve it in another terminal:

```bash
python3 -m http.server 9100 --directory /tmp
```

Open this URL in Firefox after loading the extension:

```text
http://127.0.0.1:9100/hydra-progress-500mb.bin
```

Or submit it directly through the API:

```bash
curl -X POST \
   -H 'Content-Type: application/json' \
   -H 'X-Hydra-Token: hydra_secure_token_bf1f753e' \
   -d '{
      "url": "http://127.0.0.1:9100/hydra-progress-500mb.bin",
      "save_path": "/tmp/hydra-progress-500mb.bin",
      "headers": {}
   }' \
   http://localhost:9000/api/jobs
```

## Configuration and Data

Hydra follows XDG locations:

```text
Configuration: $XDG_CONFIG_HOME/hydra/config.json
Data/database: $XDG_DATA_HOME/hydra/hydra.db
Socket:        $XDG_RUNTIME_DIR/hydra.sock
Fallback:      /tmp/hydra.sock
```

When XDG variables are not set, Hydra uses the standard locations under the
user's home directory. The default download directory is `~/Downloads`.

## Testing and Validation

Run the Go test suite:

```bash
go test ./...
```

Run static analysis:

```bash
go vet ./...
```

Validate the extension:

```bash
node --check extension/background.js
python3 -m json.tool extension/manifest.json >/dev/null
```

## Packaging

The repository includes `nfpm.yaml`, desktop integration, icons, maintainer
scripts, and a systemd user service. `build.sh` creates `.deb` and `.rpm`
packages when `nfpm` is installed.

The current package definition installs the daemon and CLI. The TUI binary is
built by `build.sh` and can be distributed alongside them; update `nfpm.yaml`
when you want the TUI included in native packages.

## Linux Compatibility

Hydra targets common 64-bit Linux distributions. The daemon uses Linux-specific
I/O functionality, while the TUI is terminal-based and avoids desktop GUI
dependencies.

Test release builds on representative systems such as Debian/Ubuntu, Fedora,
Arch, openSUSE, and Alpine. Compatibility can still vary with CPU
architecture, libc implementation, terminal emulator, permissions, proxy
configuration, certificates, and init system.

## Current Limitations

- The current browser integration targets Firefox.
- The extension uses Manifest V2 for a persistent background script; Firefox's
   future Manifest V2 support may change.
- The TUI shows jobs tracked in Hydra's database, not arbitrary files already
   present in a download directory.
- Browser interception depends on the extension being loaded and active.
- Download links requiring expired sessions or missing authentication headers
   can return HTTP 401/403.
- The daemon currently uses a development API token that should be replaced by
   configurable authentication before public deployment.

## License

See the repository license information before distributing Hydra.
