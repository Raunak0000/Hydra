<div align="center">

<img src="packaging/icons/hydra.svg" width="96" height="96" alt="Hydra Logo" />

# Hydra Download Manager

**High-performance, multi-threaded native Linux download manager.**

[![Release](https://img.shields.io/badge/release-v1.1.0-blue.svg)](https://github.com/Raunak0000/Hydra/releases)
[![Platform](https://img.shields.io/badge/platform-Linux%20x86__64-informational.svg)](https://github.com/Raunak0000/Hydra)
[![License: GPL-3.0](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE)
[![Firefox Add-on](https://img.shields.io/badge/Firefox-Install%20Extension-orange?logo=firefox-browser)](https://addons.mozilla.org/firefox/addon/hydra-downloader/)

[Key Features](#key-features) •
[Installation](#installation) •
[Firefox Extension](#firefox-extension) •
[Desktop App & Tray](#desktop-app--tray) •
[CLI & TUI](#cli--terminal-ui-tui) •
[Building from Source](#building-from-source) •
[Changelog](CHANGELOG.md)

</div>

---

Hydra is a native Linux, multi-threaded download manager written in Go and Python. It combines a low-overhead background daemon with a modern GTK 4 / Libadwaita desktop interface (`hydra-gui`), a top-bar system tray controller (`hydra-tray`), an interactive Bubble Tea terminal UI (`hydra-tui`), a scriptable CLI (`hydra`), and a Firefox WebExtension with native messaging integration.

The background daemon (`hydra-daemon`) owns downloading, queueing, SQLite WAL persistence, filesystem space allocation, and authentication. Client interfaces communicate locally over token-authenticated HTTP/SSE or Unix domain socket IPC.

---

## Key Features

- **Multi-Threaded Engine:** Parallel ranged downloads with adaptive chunk work-stealing and Linux-optimized space preallocation (`fallocate`) and positional writes (`pwrite`).
- **Modern GTK 4 / Libadwaita GUI:** Live queue filtering, active/completed views, parallel chunk segment maps, real-time speed curves, batch downloads, speed caps, scheduling windows, and file-type routing rules.
- **System Tray Controller:** Lightweight AppIndicator/StatusIcon tray (`hydra-tray`) with quick pause/resume all, engine state control, downloads folder shortcut, and live badge counters.
- **Crash & Restart Recovery:** Interrupted downloads automatically resume from saved byte offsets on daemon restart.
- **Fail-Fast Reliability:** Retry handling with exponential backoff, checksum verification (SHA-256, MD5, CRC32), non-retryable disk-full (`ENOSPC`) guards, and smoothed ETA calculations.
- **Firefox WebExtension (`v4.1.0`):** Intercepts browser downloads, preserves cookies and session headers cleanly, provides an IDM-style capture dialog, and pairs automatically with zero terminal setup via `hydra-native-host`.
- **Security Hardening:** Randomized per-user API tokens (`0600`), cross-origin and HTTPS-to-HTTP redirect credential stripping, unauthenticated credential redaction, and `O_NOFOLLOW` symlink protection.

---

## Installation

### Option 1: Debian / Ubuntu (`.deb`) & Fedora / RHEL (`.rpm`) *(Recommended)*

Download the latest release package from the [Releases page](https://github.com/Raunak0000/Hydra/releases/tag/v1.1.0):

#### Debian / Ubuntu / Linux Mint:
```bash
sudo apt install ./hydra_1.1.0_amd64.deb
```

#### Fedora / RHEL:
```bash
sudo dnf install ./hydra-1.1.0-1.x86_64.rpm
```

The package automatically registers the desktop application, system tray autostart, icon caches, `systemd --user` background service, and Firefox native messaging host.

Verify your user service:
```bash
systemctl --user status hydra.service
```

---

### Option 2: User-Level Source Installation

Clone the repository and run the automated installer (installs to `~/.local/bin`, sets up desktop launchers, autostart, and starts the daemon):

```bash
git clone https://github.com/Raunak0000/Hydra.git
cd Hydra
./install.sh
```

*(Pass `--no-tray` if you do not want the system tray to launch on login).*

To cleanly uninstall:
```bash
./install.sh --uninstall
```

---

## Firefox Extension

[![Install Extension](https://img.shields.io/badge/Firefox-Install%20Extension-orange?logo=firefox-browser)](https://addons.mozilla.org/firefox/addon/hydra-downloader/)

The Manifest V3 WebExtension (`v4.1.0`) intercepts browser downloads, cancels the native browser download, and opens a customizable confirmation dialog to set save destinations, speed caps, checksums, scheduling, and folder routing.

1. **Install Hydra** on your system first (`.deb`, `.rpm`, or `./install.sh`) so the native messaging bridge (`org.hydra.downloader.json`) is registered.
2. **Install the add-on:**
   - Permanent install: **[Add Hydra Downloader to Firefox](https://addons.mozilla.org/firefox/addon/hydra-downloader/)**.
   - Offline / testing: In Firefox, open `about:debugging` → **This Firefox** → **Load Temporary Add-on...** and select `dist/hydra-extension.zip` (or `extension/manifest.json`).
3. **Auto-Pairing:** Click the Hydra toolbar icon. The extension pairs automatically with your local engine using your generated per-user token without requiring terminal commands.

---

## Desktop App & Tray

### Graphical Manager (`hydra-gui`)
Launch **Hydra Downloader** from your application launcher or terminal:
```bash
hydra-gui --ensure-daemon
```

- **New / Batch Download:** Add single links or batch URL lists with category destination presets.
- **Speed Limits & Scheduling:** Configure global or per-download speed caps and active time windows.
- **Live Chunk Inspector:** Inspect concurrent worker chunk ranges and transfer speed history graphs.
- **File-Type Routing Rules:** Open settings (`Ctrl+,` or `hydra-gui --settings`) to route file types (e.g. ISOs, Videos, Archives) to dedicated folders.

### System Tray Controller (`hydra-tray`)
The tray icon runs in your desktop top bar / notification area on login:
```bash
hydra-tray --autostart
```
- Click to view active and queued downloads.
- Quick actions: **Pause All**, **Resume All**, **Open Downloads Folder**, or launch GUI / TUI.

---

## CLI & Terminal UI (TUI)

For headless systems, SSH sessions, or scripting, Hydra includes full terminal tools that communicate with `hydra-daemon`.

### Terminal UI (`hydra-tui`)
Interactive Bubble Tea console monitor:
```bash
hydra-tui --daemon-url http://127.0.0.1:9000
```

<details>
<summary><b>TUI Keyboard & Mouse Controls</b></summary>

| Key / Action | Description |
| :--- | :--- |
| `Up` / `Down` or `j` / `k` | Select download row |
| `PageUp` / `PageDown` | Scroll by page |
| Mouse click / wheel | Select and scroll rows |
| `p` | Pause selected job |
| `e` | Resume selected job |
| `d` | Start delete confirmation |
| `y` or `Enter` | Confirm deletion |
| `n` or `Escape` | Cancel deletion |
| `r` | Refresh jobs list |
| `?` | Toggle help overlay |
| `q` or `Ctrl+C` | Quit TUI |

</details>

### Command Line Interface (`hydra`)
Control downloads via Unix domain socket IPC:
```bash
hydra status
hydra pause <JOB_ID>
hydra resume <JOB_ID>
hydra pause-all
hydra resume-all
hydra delete <JOB_ID>
```

---

## HTTP API

The daemon provides a local REST API authenticated via the `X-Hydra-Token` header.

```bash
TOKEN="$(cat "${XDG_CONFIG_HOME:-$HOME/.config}/hydra/api.token")"
```

<details>
<summary><b>View API Endpoint Examples</b></summary>

```bash
# Engine health check (no token required)
curl http://127.0.0.1:9000/api/health

# List all downloads
curl -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs

# Submit a new download
curl -X POST \
   -H 'Content-Type: application/json' \
   -H "X-Hydra-Token: ${TOKEN}" \
   -d '{
      "url": "https://example.com/file.zip",
      "save_path": "DEFAULT",
      "headers": {}
   }' \
   http://127.0.0.1:9000/api/jobs

# Pause / Resume a job
curl -X POST -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/<JOB_ID>/pause
curl -X POST -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/<JOB_ID>/resume

# Delete a job
curl -X DELETE -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/<JOB_ID>

# Clear completed jobs from history
curl -X POST -H "X-Hydra-Token: ${TOKEN}" http://127.0.0.1:9000/api/jobs/clear-completed
```

</details>

---

## Building from Source

### Requirements
- **Go 1.25+** (for daemon, CLI, and TUI)
- **Linux 64-bit** (`fallocate`, `statfs`, Unix sockets)
- **Python 3** with `PyGObject`, `GTK 4`, and `Libadwaita` (for GUI) and `libayatana-appindicator3` (for system tray)
- **Firefox 142.0+** (for WebExtension)
- *Optional:* `nfpm` (for building `.deb`/`.rpm`), `zip` (for extension packaging)

### Build Steps

Build Go binaries individually:
```bash
go build -o bin/hydra-daemon main.go
go build -o bin/hydra ./cmd/hydra-cli
go build -o bin/hydra-tui ./cmd/hydra-tui
```

Build all release artifacts (`bin/*`, `dist/hydra_*.deb`, `dist/hydra-*.rpm`, `dist/hydra-extension.zip`, `dist/SHA256SUMS`):
```bash
./build.sh
```

Run test and package validation suites:
```bash
go vet ./...
go test -race -count=1 ./...
node --test extension/test_extension.mjs
python3 -m unittest discover -s packaging/gui -p "test_*.py" -v
python3 -m unittest discover -s packaging/tray -p "test_*.py" -v
./scripts/validate-package.sh
```

---

## Configuration & Architecture

### XDG Paths

Hydra adheres strictly to the XDG Base Directory specification:

| Resource | Path |
| :--- | :--- |
| **API Token** | `$XDG_CONFIG_HOME/hydra/api.token` (`0600`) |
| **Settings** | `$XDG_CONFIG_HOME/hydra/config.json` |
| **Database** | `$XDG_DATA_HOME/hydra/hydra.db` (SQLite WAL) |
| **IPC Socket** | `$XDG_RUNTIME_DIR/hydra.sock` (`0600`) |
| **Downloads** | `~/Downloads` (or `$XDG_DOWNLOAD_DIR`) |

<details>
<summary><b>Architecture Diagram & Components</b></summary>

```text
Firefox Extension ----(native-host + HTTP API)----\
Hydra GUI (GTK 4) ----(HTTP API + SSE)-------------+--> hydra-daemon ---> downloader/storage
Hydra Tray -----------(HTTP API)-------------------+         |
Hydra TUI ------------(HTTP API + SSE)-------------+         v
Hydra CLI ------------(Unix socket IPC)------------/    SQLite DB (WAL)
```

| Component | Role |
| :--- | :--- |
| `hydra-daemon` | Headless engine, queue manager, HTTP/SSE server, and Unix IPC listener. |
| `hydra-gui` | Primary GTK 4 + Libadwaita desktop application (**Hydra Downloader**). |
| `hydra-tray` | System tray and quick-actions controller. |
| `hydra-tui` | Bubble Tea interactive terminal interface. |
| `hydra` | CLI client for terminal scripts and socket control. |
| `hydra-native-host` | Firefox native messaging bridge (`org.hydra.downloader`). |

</details>

---

## Current Limitations

- **Linux 64-bit only:** Daemon I/O uses Linux `fallocate`, `statfs`, and Unix socket IPC.
- **Firefox-only extension:** Relies on `webRequestBlocking` and `nativeMessaging` (`org.hydra.downloader`); unmodified Chromium MV3 does not support `webRequestBlocking`.
- **Tracked jobs only:** The GUI and TUI show downloads managed by Hydra, not pre-existing files in `~/Downloads`.
- **Expiring signed URLs:** Short-lived pre-signed URLs can still return HTTP `401`/`403` if paused past server expiration.

---

## License

GNU General Public License v3.0 (GPL-3.0) — see [LICENSE](LICENSE) for full terms.
