# Hydra — Linux-Native Multi-Threaded Download Manager

Hydra is a high-performance, multi-threaded download manager designed specifically for Linux. It combines a native Go download engine with a responsive, real-time web dashboard and a Firefox browser extension to intercept and manage your downloads seamlessly.

---

## ⚡ Key Features

- **Multi-Threaded Download Engine**: Dynamically segments large downloads into parallel chunks to maximize network throughput.
- **Linux-Native I/O Optimization**: Utilizes low-level system calls (`fallocate` and `pwrite`) for zero-overhead disk pre-allocation, reducing disk fragmentation and CPU cycles on high-speed lines.
- **Firefox Integration (Browser Intercept)**: Features a persistent Manifest V2 background extension that sniffs downloads, replicates session cookies (essential for sites like Gofile), and forwards them to the daemon.
- **Interactive Save Path Selection**: Intercepted downloads do not silently force themselves into `~/Downloads/`. Instead, they enter a `PENDING_PATH` state, triggering a dashboard modal to let you review the filename, source URL, and customize the target destination.
- **Real-Time Dashboard**: Built with Go, `htmx`, and `templ` for surgical DOM updates, showcasing live progress bars, worker thread offsets, downloading speed metrics, and active pause/resume controls.
- **Token-Authenticated API**: Secure communication between the browser extension, dashboard frontend, and daemon backend via custom security headers (`X-Hydra-Token`).

---

## 📂 Project Architecture

```
├── cmd/
│   └── hydra-cli/           # CLI interaction frontend
├── extension/
│   ├── background.js       # Sniffs download networks, forwards cookies/headers
│   └── manifest.json       # Manifest V2 persistent configuration
├── pkg/
│   ├── downloader/         # Handshakes, segment calculators, and worker loops
│   ├── models/             # Shared job structures and thread-state schemas
│   ├── storage/            # Disk allocators, REST router, and HTTP server daemon
│   └── views/              # HTMX dashboard templates (written in templ)
├── main.go                 # Application entry point & daemon manager
└── README.md
```

---

## 🚀 Installation & Setup

### Prerequisites
- Go 1.25.0 or later
- [templ](https://github.com/a-h/templ) template compiler
- Firefox Browser (for browser integration)

### 1. Compile the Dashboard templates
If you modify the templates in `pkg/views/dashboard.templ`, regenerate the Go source files:
```bash
go run github.com/a-h/templ/cmd/templ generate
```

### 2. Build the Daemon
Build the compiled native binary:
```bash
go build -o hydra main.go
```

### 3. Launch the Daemon
Run the background server (default port is `9000`):
```bash
nohup ./hydra > /dev/null 2>&1 &
```
Access the dashboard by navigating to `http://localhost:9000` in your browser.

---

## 🔌 Browser Extension Setup

To package the browser extension for manual installation in Firefox:

1. Compress the extension components:
   ```bash
   zip -j /home/raunak/Projects/extension.zip extension/background.js extension/manifest.json
   ```
2. Open Firefox and go to `about:debugging`.
3. Click on **This Firefox**.
4. Click **Load Temporary Add-on...** and select `/home/raunak/Projects/extension.zip`.
