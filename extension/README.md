# Hydra Browser Extension

A Manifest V3 Firefox & Chromium WebExtension that gives Hydra an IDM-style browser download workflow.

## Features

- **Automatic download capture** — intercepts qualifying download responses (`Content-Disposition`, binary MIME types, known file extensions) via `webRequest` + `downloads.onCreated` fallback while keeping regular webpages untouched.
- **Confirmation, destination & filename selection** — review file size, source host, and captured session headers; rename the file; pick from daemon category presets (`Video`, `Music`, `Compressed`, `Programs`, `Documents`, recent folders), launch the native desktop folder picker or built-in folder navigator, check free disk space, or choose **Pending Path** to decide later inside Hydra.
- **Download options** — start immediately, add paused, or schedule for a future time; set a per-download KB/s speed limit; attach SHA-256 / MD5 / CRC32 checksum verification; toggle forwarding browser cookies & auth headers.
- **Browser integration status & recovery feedback** — toolbar badge (`ON`, `OFF`, `KEY`, `ERR`), live connection/auth/native-bridge/queue status popup, recent capture & failure history, one-click native token auto-pairing, one-click daemon startup recovery, and safe fallback back to the browser downloader without re-interception loops.

## Supported File Types

Archives & Disk Images (`zip tar gz bz2 xz 7z rar iso img bin`), Video (`mp4 mkv avi mov flv webm wmv m4v mpg mpeg`), Audio (`mp3 flac wav aac ogg m4a opus wma`), Software (`exe msi dmg deb rpm appimage apk xapk snap`), Documents (`pdf epub djvu`), plus custom extensions in Settings.

## Installation

### Native Desktop Bridge (Recommended for Zero-Terminal Auto-Pairing)

Running `./install.sh` (or installing the `.deb`/`.rpm` package) installs `hydra-native-host` and registers `~/.mozilla/native-messaging-hosts/org.hydra.downloader.json` (or `/usr/lib/mozilla/native-messaging-hosts/org.hydra.downloader.json`). This allows the extension to:

- Automatically pair with `~/.config/hydra/api.token` without copying tokens manually.
- Start `hydra-daemon` from the browser recovery banner if the background service is stopped.
- Launch `hydra-gui` from the extension popup.

### Firefox (Primary Target)

1. Package or load `extension/`:
   ```bash
   (cd extension && zip -r ../hydra-extension.zip . -x "package.json" "test_extension.mjs")
   ```
2. Open `about:debugging#/runtime/this-firefox`.
3. Click **Load Temporary Add-on…** and select `extension/manifest.json` (or `hydra-extension.zip`).
4. Click the Hydra toolbar icon to confirm **Connected** status (or use **Auto-Pair Token** / Settings).

### Chrome / Chromium

- Chrome MV3 forbids `webRequestBlocking` in third-party manifests. To load unpacked in Chromium, remove `"webRequestBlocking"` from `extension/manifest.json` permissions; `background.js` automatically falls back to non-blocking header metadata capture + `downloads.onCreated`.

## Testing

```bash
node --test extension/test_extension.mjs
```

## Files

| File | Purpose |
|------|---------|
| `manifest.json` | Manifest V3 extension descriptor |
| `shared.js` | Shared interception rules, validation, presets, payload builder, and health helpers |
| `background.js` | Service worker / event page — capture, native bridge, recovery history, dispatch |
| `popup.html` / `popup.js` | Dual-mode UI: download confirmation/options dialog and toolbar status dashboard |
| `settings.html` / `settings.js` | Options page for pairing, capture rules, size threshold, and exclusions |
| `test_extension.mjs` | Node built-in unit test suite |
| `icons/` | Extension icons (16, 48, 128 px) |
