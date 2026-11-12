# Hydra Download Manager

Hydra is a native Linux, multi-threaded download manager written in Go with a
GTK 4 / Libadwaita desktop app (`hydra-gui`), system tray controller
(`hydra-tray`), Bubble Tea terminal UI (`hydra-tui`), CLI (`hydra`), and
Firefox WebExtension.

The background daemon (`hydra-daemon`) owns downloading, queueing, persistence,
authentication, and filesystem operations. Client interfaces talk to the daemon
over its local token-authenticated HTTP API or Unix domain socket IPC and do
not write to SQLite directly.

See [CHANGELOG.md](CHANGELOG.md) for release notes and version history.

## Features

- Parallel ranged downloads with adaptive chunk work-stealing.
- Native GTK 4 + Libadwaita desktop manager (`hydra-gui`) with live chunk maps, speed graphs, batch downloads, speed caps, scheduling windows, and file-type routing rules.
- Background tray indicator (`hydra-tray`) and `systemd --user` / XDG autostart integration.
- Retry handling with exponential backoff, non-retryable disk-full / auth fail-fast guards, checksum verification, and smoothed ETA reporting.
- Linux-optimized space preallocation (`fallocate`) and positional writes (`pwrite`), plus SQLite persistence in WAL mode.
- Crash and restart recovery: interrupted downloads automatically resume from saved byte offsets on daemon restart.
- Firefox Manifest V3 WebExtension (`v4.1.0`) with IDM-style confirmation dialog, category presets, browser header preservation, and zero-terminal native messaging pairing (`hydra-native-host`).
- Strict per-user `X-Hydra-Token` API authentication with cross-origin redirect credential stripping and `O_NOFOLLOW` symlink/path-traversal protection.
- Bubble Tea TUI (`hydra-tui`) and Unix-socket CLI (`hydra`) for SSH and headless workflows.

## Architecture

```text
Firefox extension ----(native-host + HTTP API)----\
Hydra GUI (GTK 4) ---(HTTP API + SSE)--------------+--> hydra-daemon ---> downloader/storage
Hydra Tray ----------(HTTP API)--------------------+         |
Hydra TUI -----------(HTTP API + SSE)--------------+         v
Hydra CLI -----------(Unix socket IPC)-------------/    SQLite DB (WAL)
```

### Binaries

- `hydra-daemon` — headless download engine, queue manager, HTTP/SSE server, and Unix IPC listener.
- `hydra-gui` — primary GTK 4 + Libadwaita desktop application (**Hydra Downloader**).
- `hydra-tray` — system tray and quick-actions controller.
- `hydra-tui` — optional Bubble Tea interactive terminal interface.
- `hydra` — optional CLI client for scripting and terminal control.
- `hydra-native-host` — Firefox native messaging bridge (`org.hydra.downloader`) for token pairing, daemon startup recovery, and GUI launch.

### Project Layout

```text
cmd/
   hydra-cli/              CLI client and tests
   hydra-tui/              Bubble Tea TUI entry point
extension/                 Firefox Manifest V3 WebExtension (v4.1.0)
pkg/
   downloader/             Handshake, redirect guards, workers, chunk tracker, rate limiter, checksums
   models/                 Shared job and chunk models
   storage/                SQLite store, queue, HTTP API, IPC, path/symlink guards, notifications
   tui/                    Bubble Tea model and daemon client
packaging/
   autostart/              XDG autostart desktop files
   desktop/                Application menu `.desktop` launchers and MIME configuration
   extension/              `hydra-native-host` wrapper and Mozilla native messaging manifest
   gui/                    GTK 4 desktop app (`hydra-gui`, `hydra_gui/` package, and unit tests)
   icons/                  Scalable SVG application icon
   scripts/                `.deb`/`.rpm` maintainer `postinst` / `prerm` scripts
   systemd/                `systemd --user` service unit (`hydra.service`)
   tray/                   System tray controller (`hydra-tray` and unit tests)
scripts/
   validate-package.sh     Automated `.deb` metadata, permission, install, upgrade, and removal checks
docs/
   architecture.md         Internal system architecture notes
   performance.md          Performance tuning notes
   roadmap.md              Development roadmap
main.go                    Hydra daemon entry point
build.sh                   Production build, extension archive, `.deb`/`.rpm`, and `SHA256SUMS` script
install.sh                 Per-user installer, uninstaller, and package helper
nfpm.yaml                  Debian/RPM package definition
CHANGELOG.md               Release notes
```

## Requirements

- **Go 1.25+** (to build the daemon, TUI, and CLI).
- **Linux** (64-bit; daemon uses Linux `fallocate`/`statfs`/`O_NOFOLLOW` primitives).
- **Python 3** with **PyGObject**, **GTK 4**, and **Libadwaita** for `hydra-gui` (plus `libayatana-appindicator3` / AppIndicator3 for `hydra-tray`). The daemon, CLI, and TUI do not require desktop GUI libraries or systemd.
- **Firefox** (109+) for the WebExtension.
- **`zip`** (optional, to package `dist/hydra-extension.zip`), **`nfpm`** (optional, to build `.deb`/`.rpm` packages).

## Install

### User Installation (Recommended from Source)

Install Hydra for your user with a single command (builds binaries, installs desktop launchers, icon, `hydra-gui`, `hydra-tray`, Firefox native messaging manifest, provisions a randomized `$XDG_CONFIG_HOME/hydra/api.token`, enables login autostart, and starts `hydra-daemon`):

```bash
./install.sh
```

Pass `--no-tray` to omit the optional tray login autostart entry.

Uninstall cleanly:

```bash
./install.sh --uninstall
```

### Debian / Ubuntu (`.deb`) and Fedora / RHEL (`.rpm`) Packages

Build or download the release packages (`v1.1.0`) and install system-wide:

```bash
# Build local packages + extension zip + SHA256SUMS into dist/
./build.sh
# Or build & install package directly via install.sh:
./install.sh --package

# Debian / Ubuntu:
sudo apt install ./dist/hydra_1.1.0_amd64.deb

# Fedora / RHEL:
sudo dnf install ./dist/hydra-1.1.0-1.x86_64.rpm
```

When installed system-wide via `.deb`/`.rpm`, enable/check your user daemon (and verify your generated `~/.config/hydra/api.token`) with:

```bash
systemctl --user status hydra.service
```

## Build

Build individual Go binaries:

```bash
go build -o bin/hydra-daemon main.go
go build -o bin/hydra ./cmd/hydra-cli
go build -o bin/hydra-tui ./cmd/hydra-tui
```

Build all release artifacts (`bin/*`, `dist/hydra_*.deb`, `dist/hydra-*.rpm`, `dist/hydra-extension.zip`, `dist/SHA256SUMS`):

```bash
./build.sh
```

## Run Hydra

### Primary Desktop Workflow (`hydra-gui` + `hydra-tray`)

Launch **Hydra Downloader** from your desktop application menu (`hydra-gui --ensure-daemon`), or directly from a terminal:

```bash
./packaging/gui/hydra-gui --ensure-daemon
./packaging/tray/hydra-tray
```

From the GUI you can add single or batch downloads, set speed limits and schedules, inspect chunk segments and speed curves, open settings (`Ctrl+,` or `hydra-gui --settings`) to manage file-type auto-categorization rules, and pause/resume/retry/redownload/delete jobs.

### Background Daemon, TUI, and CLI

`hydra-daemon` loads `HYDRA_API_TOKEN` from the environment or `$XDG_CONFIG_HOME/hydra/api.token` (auto-generating a `0600` per-user token if missing; there is no hardcoded default token). When installed via `./install.sh` or `systemd --user`, your per-user token lives at `${XDG_CONFIG_HOME:-$HOME/.config}/hydra/api.token` (`0600`).

Start the daemon manually for development:

```bash
export HYDRA_API_TOKEN="$(openssl rand -hex 24)"
go run .
```

Optional overrides:
- `HYDRA_HTTP_ADDR` — HTTP bind address (defaults to `127.0.0.1:9000`).
- `HYDRA_ENABLE_NOTIFICATIONS=0` — disable desktop D-Bus/notify-send notifications.

The daemon also creates a `0600` Unix IPC socket under `$XDG_RUNTIME_DIR/hydra.sock` (falling back to `/tmp/hydra.sock`).

Optional terminal UI (`hydra-tui`):

```bash
go run ./cmd/hydra-tui \
   --daemon-url http://127.0.0.1:9000 \
   --token "${HYDRA_API_TOKEN}"
```

Optional CLI (`hydra`):

```bash
go run ./cmd/hydra-cli status
go run ./cmd/hydra-cli pause JOB_ID
go run ./cmd/hydra-cli resume JOB_ID
go run ./cmd/hydra-cli pause-all
go run ./cmd/hydra-cli resume-all
go run ./cmd/hydra-cli delete JOB_ID
```

Tray helper check:

```bash
./packaging/tray/hydra-tray --check
```

## TUI Keyboard & Mouse Controls

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

## HTTP API

Authenticated routes require `X-Hydra-Token` matching `HYDRA_API_TOKEN`:

```bash
TOKEN="$(cat "${XDG_CONFIG_HOME:-$HOME/.config}/hydra/api.token")"

# Health check
curl http://127.0.0.1:9000/api/health

# List jobs
curl -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs

# Create a download
curl -X POST \
   -H 'Content-Type: application/json' \
   -H "X-Hydra-Token: ${TOKEN}" \
   -d '{
      "url": "https://example.com/file.zip",
      "save_path": "DEFAULT",
      "headers": {}
   }' \
   http://127.0.0.1:9000/api/jobs

# Pause / resume / delete
curl -X POST -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/JOB_ID/pause
curl -X POST -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/JOB_ID/resume
curl -X DELETE -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/JOB_ID
curl -X DELETE -H "X-Hydra-Token: ${TOKEN}" "http://127.0.0.1:9000/api/jobs/JOB_ID?keep_file=true"
curl -X POST -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/clear-completed
```

`DEFAULT` save paths are resolved by the daemon to the configured download or matching category rule directory.

## Firefox Extension

[![Get the Add-on](https://img.shields.io/badge/Firefox-Install%20Extension-orange?logo=firefox-browser)](https://addons.mozilla.org/firefox/addon/hydra-downloader/)

The Manifest V3 WebExtension (`extension/`, `v4.1.0`) intercepts supported browser downloads, preserves session cookies/headers cleanly, cancels the native browser download, and opens a confirmation dialog to configure filename, folder preset, speed cap, checksum, pause, or scheduling options.

### Installing the Extension

1. Run `./install.sh` (or install the `.deb`/`.rpm` package) so `org.hydra.downloader.json` and `hydra-native-host` are registered for Firefox automatic token pairing.
2. Install permanently from Mozilla Add-ons: **[Add Hydra Downloader to Firefox](https://addons.mozilla.org/firefox/addon/hydra-downloader/)**.
   *(For local development or temporary testing without AMO: run `./build.sh` to generate `dist/hydra-extension.zip`, open `about:debugging` -> **This Firefox** -> **Load Temporary Add-on...**, and select `extension/manifest.json` or `dist/hydra-extension.zip`.)*
3. Click the Hydra toolbar icon to inspect daemon health, free disk space, and token pairing status (if manual pairing is needed, paste your token from `~/.config/hydra/api.token` in the extension settings page).

The extension ignores Hydra's own `127.0.0.1:9000` / `localhost:9000` management endpoints while supporting local test HTTP servers on other ports (e.g. `9100`).

## Local Download Test

```bash
dd if=/dev/zero of=/tmp/hydra-progress-500mb.bin bs=1M count=500
python3 -m http.server 9100 --directory /tmp
```

Open `http://127.0.0.1:9100/hydra-progress-500mb.bin` in Firefox with the extension active, or submit directly via API/GUI.

## Configuration and Data

Hydra follows XDG base directories:

```text
API Token:     $XDG_CONFIG_HOME/hydra/api.token   (fallback: ~/.config/hydra/api.token)
Configuration: $XDG_CONFIG_HOME/hydra/config.json
Data/database: $XDG_DATA_HOME/hydra/hydra.db
IPC Socket:    $XDG_RUNTIME_DIR/hydra.sock        (fallback: /tmp/hydra.sock)
```

Default download directory: `~/Downloads` (or `$XDG_DOWNLOAD_DIR` when configured).

## Testing and Validation

Run the full test and validation suite:

```bash
go vet ./...
go test -race -count=1 ./...
node --test extension/test_extension.mjs
python3 -m unittest discover -s packaging/gui -p "test_*.py" -v
python3 -m unittest discover -s packaging/tray -p "test_*.py" -v
```

Validate generated `.deb` package structure, metadata, permissions, and maintainer scripts:

```bash
./build.sh && ./scripts/validate-package.sh
```

## Packaging

`nfpm.yaml` and `build.sh` produce `hydra_<version>_amd64.deb`, `hydra-<version>-1.x86_64.rpm`, `hydra-extension.zip`, and `SHA256SUMS` under `dist/`. Packages install:
- Binaries: `/usr/bin/{hydra-daemon,hydra,hydra-tui,hydra-gui,hydra-tray,hydra-native-host}`
- GUI modules: `/usr/share/hydra/gui/hydra_gui/*`
- Desktop & autostart entries: `/usr/share/applications/{hydra,hydra-gui}.desktop`, `/etc/xdg/autostart/{hydra-daemon,hydra-tray}.desktop`, `/usr/share/icons/hicolor/scalable/apps/hydra.svg`
- Service & native host manifests: `/usr/lib/systemd/user/hydra.service`, `/usr/lib/mozilla/native-messaging-hosts/org.hydra.downloader.json`

## Current Limitations

- Targets 64-bit Linux distributions (daemon I/O uses Linux `fallocate`, `statfs`, and Unix socket IPC).
- The Firefox WebExtension relies on `webRequestBlocking` and `nativeMessaging` (`org.hydra.downloader`); Chromium MV3 does not support `webRequestBlocking` in unmodified form.
- The TUI and GUI display downloads tracked in Hydra's database, not untracked files already present in `~/Downloads`.
- Short-lived signed URLs (e.g., temporary pre-signed links) can still return HTTP 401/403 after server expiration if paused for extended periods.

## License

MIT — see repository metadata and [nfpm.yaml](nfpm.yaml) before redistributing Hydra.
