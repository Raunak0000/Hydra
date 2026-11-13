# Changelog

All notable changes to Hydra are documented in this file.

## [v1.1.1] - 2026-11-13

### Fixed
- **Boot Port Conflict Resolution:** Added `ConditionUser=!@system` to `hydra.service` to prevent display managers (`gdm`, `lightdm`, `sddm`) from launching Hydra during system boot and occupying port `9000`.
- **Daemon HTTP Listener Retry & Fail-Fast:** Added a synchronous 10-attempt listener retry loop (over 3 seconds) with backoff in `hydra-daemon`. If port binding cannot succeed, the daemon now exits fatal with a non-zero code to trigger automatic systemd service restart instead of silently lingering in an unreachable state.
- **Client Auto-Recovery:** Updated `hydra-gui` ("Start Background Engine"), `hydra-tray`, and `hydra-native-host` to issue `systemctl --user restart hydra.service` instead of `start`, forcefully reviving any stale or unreachable daemon state.
- **Extension Auth Detection:** Prevented false-positive download interception on OAuth login, authentication redirects, and navigation hops.

## [v1.1.0] - 2026-11-12

### Highlights
- **Native GTK 4 / Libadwaita Desktop App (`hydra-gui`)**: Primary desktop interface (`Hydra Downloader`) with live queue search/filtering, active/completed split view, inspector panel with parallel chunk segment map and speed chart, global settings & file-type auto-categorization rules (`Ctrl+,`), new & batch download dialogs, speed/schedule overrides, pause/resume/start-now/retry/redownload controls, and remove-keep-file or full delete actions.
- **System Tray Controller (`hydra-tray`)**: Lightweight background tray menu for quick engine status, pause/resume all, downloads folder shortcut, and launching `hydra-gui`.
- **Automatic Firefox Native Messaging Pairing (`hydra-native-host`)**: Zero-terminal API token pairing, background daemon auto-start recovery, and direct GUI/picker integration from Firefox.
- **Background Engine Autostart & Recovery**: `systemd --user` service (`hydra.service`), XDG autostart entries, pre-download disk-space checks with orphan cleanup, crash/restart recovery (`DOWNLOADING` -> `PAUSED` with byte-range resumption), and `ENOSPC` / quota fail-fast error classification.

### Security Hardening
- Removed static/development fallback API tokens; `hydra-daemon` requires `HYDRA_API_TOKEN` or a per-user `0600` token at `$XDG_CONFIG_HOME/hydra/api.token` (auto-generated on first start if missing).
- Enforced `SafeCheckRedirect` cross-origin and HTTPS-to-HTTP downgrade credential stripping across metadata probes and parallel chunk workers, plus sanitization of caller-submitted headers and credential redaction on unauthenticated `/api/queue/json` views.
- Hardened path traversal, `O_NOFOLLOW` symlink protection, destination conflict resolution, and Unix IPC socket permissions (`0600`).

### Testing & Packaging
- Full Go test suite with `-race` covering daemon/CLI/TUI/desktop/extension end-to-end flows, graceful `SIGTERM` & `SIGKILL` daemon restarts, disk-full cleanup, redirect auth stripping, and install/upgrade/removal lifecycles.
- Automated Python test suites for `packaging/gui` (17 tests) and `packaging/tray` (6 tests), plus Firefox extension tests (`extension/test_extension.mjs`, 7 tests).
- Verified `.deb`, `.rpm`, `hydra-extension.zip`, and `SHA256SUMS` release artifact generation via `build.sh` and `scripts/validate-package.sh`.

## [v1.0.0]

- Initial native Linux multi-threaded download engine (`hydra-daemon`), Bubble Tea TUI (`hydra-tui`), CLI (`hydra`), SQLite WAL persistence, rate limiting, checksum verification, and Firefox WebExtension (`extension/`).

