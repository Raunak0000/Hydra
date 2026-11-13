<div align="center">

<img src="packaging/icons/hydra.svg" width="96" height="96" alt="Hydra Logo" />

# Hydra

**Let Hydra download it.**

Your high-speed, native Linux download engine.

An open-source, local-first download manager that accelerates multi-connection downloads, catches links from your browser, sits quietly in your top bar, and never loses your progress.

[![Go 1.25+](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev/)
[![Platform](https://img.shields.io/badge/platform-Linux%20x86__64-informational?style=flat&logo=linux&logoColor=white)](https://github.com/Raunak0000/Hydra)
[![Release](https://img.shields.io/badge/release-v1.1.0-blue?style=flat)](https://github.com/Raunak0000/Hydra/releases/tag/v1.1.0)
[![Firefox Extension](https://img.shields.io/badge/Firefox-Extension%20v4.1.0-orange?style=flat&logo=firefox-browser&logoColor=white)](https://addons.mozilla.org/firefox/addon/hydra-downloader/)
[![License: GPL-3.0](https://img.shields.io/badge/license-GPLv3-green?style=flat)](LICENSE)

</div>

---

## What Hydra does

**Downloads faster.** Hydra splits ranged files across parallel streams with adaptive chunk work-stealing. It preallocates file space on Linux filesystems (`fallocate`) and writes concurrently at exact byte offsets (`pwrite`), eliminating disk fragmentation and post-download file stitching delays.

**Lives on your desktop.** A native GTK 4 / Libadwaita application (`hydra-gui`) gives you a clean view of your queue, real-time speed graphs, parallel chunk maps, batch URL submission, and file-type destination rules.

**Sits in your top bar.** A lightweight system tray icon (`hydra-tray`) stays in your desktop panel. Pause all downloads with one click, check download counts from the badge, or open your completed downloads folder without opening a terminal.

**Catches your browser downloads.** The Manifest V3 Firefox WebExtension (`v4.1.0`) intercepts downloads, preserves your session cookies and authentication headers, and pops up an IDM-style dialog to pick destinations, speed limits, checksums, or schedules. It pairs with the background engine automatically via native messaging with zero terminal setup.

**Recovers from crashes and restarts.** If your laptop reboots, Wi-Fi drops, or the power goes out, interrupted downloads resume from the exact byte offset where they stopped. Completed chunks stay saved in SQLite WAL mode.

**Automates your library.** Organize downloads automatically. Set custom rules in settings (or use presets) so ISOs go to `/data/ISOs`, videos land in `~/Videos`, and archives route to `~/Downloads/Compressed` without asking.

**Stays scriptable.** Control Hydra your way. A headless background daemon (`hydra-daemon`) handles downloading and queueing, while an interactive Bubble Tea terminal UI (`hydra-tui`), a scriptable CLI (`hydra`), and a local REST API let you manage downloads over SSH or scripts.

**Stays yours.** Local-first, private, and open source. Hydra runs on your computer with a randomized per-user token (`0600`), strips authentication headers when servers redirect across domains, and includes zero telemetry, zero analytics, and zero cloud accounts.

<div align="center">

[Install](#getting-started) • [Firefox Extension](#firefox-extension) • [Desktop & Tray](#desktop-app--tray) • [CLI & TUI](#cli--terminal-ui-tui) • [Privacy & Security](#privacy--security) • [Star on GitHub](https://github.com/Raunak0000/Hydra)

</div>

---

## Getting started

### Debian, Ubuntu, and Linux Mint (`.deb`)

Download `hydra_1.1.0_amd64.deb` from the [Releases](https://github.com/Raunak0000/Hydra/releases/tag/v1.1.0) page, or install via terminal:

```bash
curl -4 -fSL -O https://github.com/Raunak0000/Hydra/releases/download/v1.1.0/hydra_1.1.0_amd64.deb && sudo apt install ./hydra_1.1.0_amd64.deb
```

1. **Install the package.** It installs all binaries, registers the GTK 4 desktop app (`hydra-gui`), adds the tray icon (`hydra-tray`), provisions your private user token, and enables `systemd --user` background autostart.
2. **Open Hydra.** Launch **Hydra Downloader** from your application menu, or run `hydra-gui`.
3. **Verify engine status:**
   ```bash
   systemctl --user status hydra.service
   ```

---

### Fedora and RHEL (`.rpm`)

Download `hydra-1.1.0-1.x86_64.rpm` from the [Releases](https://github.com/Raunak0000/Hydra/releases/tag/v1.1.0) page, or install via terminal:

```bash
curl -4 -fSL -O https://github.com/Raunak0000/Hydra/releases/download/v1.1.0/hydra-1.1.0-1.x86_64.rpm && sudo dnf install ./hydra-1.1.0-1.x86_64.rpm
```

---

### User-Level Source Install (No root required)

If you prefer building and installing directly into `~/.local`:

```bash
git clone https://github.com/Raunak0000/Hydra.git
cd Hydra
./install.sh
```

*(Add `--no-tray` if you do not want the system tray to launch automatically on login).*

---

## Firefox Extension

The Manifest V3 WebExtension intercepts browser downloads, cancels the default browser transfer, and hands off the job to Hydra with session cookies and authentication headers preserved.

1. **Install Hydra** on your system first (`.deb`, `.rpm`, or `./install.sh`) so the native messaging bridge (`org.hydra.downloader.json`) is registered.
2. **Install the extension:**
   - From Mozilla Add-ons: **[Add Hydra Downloader to Firefox](https://addons.mozilla.org/firefox/addon/hydra-downloader/)**
   - Offline / testing: In Firefox, open `about:debugging` → **This Firefox** → **Load Temporary Add-on...** and select `dist/hydra-extension.zip` (or `extension/manifest.json`).
3. **Auto-pairing:** Click the Hydra toolbar icon. The extension auto-detects `~/.config/hydra/api.token` through `hydra-native-host` and connects to the daemon with zero manual key copying.

---

## Desktop App & Tray

### Graphical Manager (`hydra-gui`)
Launch **Hydra Downloader** from your application launcher or terminal:
```bash
hydra-gui --ensure-daemon
```

- **New & Batch Downloads:** Queue individual files or paste multi-line URL lists with automatic category routing.
- **Speed Limits & Schedules:** Set bandwidth caps per download or globally, or schedule large transfers for off-peak hours.
- **Chunk Inspector:** Watch worker threads download parallel slices in real time with live speed curves.
- **Category Rules:** Press `Ctrl+,` (or run `hydra-gui --settings`) to create folder routing rules by file extension.

### System Tray Controller (`hydra-tray`)
Starts automatically on desktop login and sits in your top bar:
```bash
hydra-tray --autostart
```
- Quick controls: **Pause All**, **Resume All**, or **Open Downloads Folder**.
- Live badge displays active and queued download counts.

---

## CLI & Terminal UI (TUI)

For headless servers, remote SSH sessions, or scripting:

### Interactive TUI (`hydra-tui`)
```bash
hydra-tui
```

| Key | Action |
| :--- | :--- |
| `↑` / `↓` or `j` / `k` | Navigate downloads |
| `p` / `e` | Pause / Resume selected download |
| `d` then `y` | Delete download |
| `r` | Refresh queue |
| `?` | Toggle help overlay |
| `q` | Quit |

### Scriptable CLI (`hydra`)
```bash
hydra status
hydra pause <JOB_ID>
hydra resume <JOB_ID>
hydra pause-all
hydra resume-all
hydra delete <JOB_ID>
```

---

## Privacy & Security

Hydra runs entirely on your machine. Your downloads, links, cookies, and database stay in `~/.local/share/hydra` and `~/.config/hydra`. Hydra has no analytics, no telemetry, no tracking, and no external accounts; its developers receive nothing.

- **What Hydra stores.** SQLite database in WAL mode on your disk, tracking active chunk ranges, resume byte offsets, and configured routing rules.
- **Per-user authentication.** The daemon provisions a private `0600` token at `~/.config/hydra/api.token`. Only local processes running under your user account can talk to the engine.
- **Session credential isolation.** Cross-origin and HTTPS-to-HTTP redirects strip sensitive authentication and cookie headers automatically (`SafeCheckRedirect`) to prevent credential leakage.
- **Filesystem boundaries.** Path traversal and symlink poisoning attacks are blocked (`O_NOFOLLOW` and jail checks).
- **Network connections.** Hydra only contacts the URLs you explicitly ask it to download, plus optional GitHub release checks if enabled.

---

## Uninstall

### Debian / Ubuntu (`.deb`):
```bash
sudo apt remove hydra
```

### Fedora / RHEL (`.rpm`):
```bash
sudo dnf remove hydra
```

### Source installation:
```bash
./install.sh --uninstall
```

To delete remaining configuration and download database history:
```bash
rm -rf ~/.config/hydra ~/.local/share/hydra
```

---

## Build from source

### Requirements
- Go 1.25+
- Linux 64-bit
- Python 3 with `PyGObject`, `GTK 4`, and `Libadwaita` (for GUI) and `libayatana-appindicator3` (for system tray)
- *Optional:* `nfpm` (to build `.deb`/`.rpm`), `zip` (for extension packaging)

```bash
git clone https://github.com/Raunak0000/Hydra.git
cd Hydra
./build.sh
```

Run test and package validation suites:
```bash
go test -race -count=1 ./...
node --test extension/test_extension.mjs
python3 -m unittest discover -s packaging/gui -p "test_*.py" -v
python3 -m unittest discover -s packaging/tray -p "test_*.py" -v
./scripts/validate-package.sh
```

---

## License

GNU General Public License v3.0 (GPL-3.0) — see [LICENSE](LICENSE) for full terms.
