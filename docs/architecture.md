# Hydra Architecture

## System Overview

Hydra is a Linux-focused, multi-threaded desktop download manager, comparable
in user experience to IDM on Windows. It runs a background download engine
inside the user's desktop session, while the browser integration and user
interfaces make downloads feel like a native part of the operating system.

The background engine is an implementation detail of the desktop application,
not a product that users are expected to deploy or operate as a network
service. Its local APIs and database exist to coordinate Hydra's own clients.

The system has four primary layers:

1. **User experience layer**
   - `hydra-tui` provides an interactive Bubble Tea terminal interface.
   - `hydra` provides command-line control through the Unix socket.
   - The Firefox extension forwards browser downloads to Hydra.
   - Future desktop integration can provide a tray/menu-bar entry, download
     notifications, and a graphical download list.

2. **Application control layer**
   - The background engine exposes a protected local HTTP API on port `9000`.
   - REST endpoints create, list, pause, resume, delete, and batch jobs.
   - Server-sent events provide live job telemetry.
   - Queue and scheduler logic promote eligible jobs to workers.

3. **Download engine layer**
   - Handshake logic discovers remote file capabilities and metadata.
   - Ranged workers download chunks concurrently.
   - Adaptive work stealing redistributes unfinished ranges.
   - Rate limiting, retries, progress, ETA, and checksum verification are
     handled during execution.

4. **Persistence and filesystem layer**
   - SQLite with WAL mode stores jobs, chunks, settings, schedules, and
     verification state.
   - Resume state is persisted so interrupted downloads can continue safely.
   - Destination validation, conflict handling, disk-space checks, allocation,
     and cleanup protect the local filesystem.

## System Diagram

```text
 Firefox extension ──┐
                     │
 hydra-tui ──────────┼── HTTP API / SSE ──┐
                     │                   │
 hydra CLI ──────────┘                   ▼
                                  ┌──────────────┐
                                  │ Hydra daemon │
                                  │ API + queue  │
                                  └──────┬───────┘
                                         │
                         ┌───────────────┼───────────────┐
                         ▼               ▼               ▼
                   Downloader        Scheduler       SSE broker
                         │               │               │
                         └───────────────┼───────────────┘
                                         ▼
                                  SQLite / filesystem
```

The TUI, CLI, and extension do not access SQLite directly. They communicate
with Hydra's background engine, which remains the owner of job state and
filesystem operations. Users normally encounter this engine through the
desktop application and browser extension rather than by starting it manually.

## Repository Structure

```text
Hydra/
├── cmd/
│   ├── hydra-cli/              Unix-socket CLI client
│   └── hydra-tui/              Bubble Tea TUI entry point
├── extension/                  Firefox WebExtension
├── pkg/
│   ├── downloader/             Handshake, workers, chunks, limits, checksums
│   ├── models/                 Shared job and chunk models
│   ├── storage/                SQLite, queue, API, IPC, paths, notifications
│   └── tui/                    TUI model, forms, browser, daemon client
├── packaging/                  Desktop, icon, systemd, and install files
├── scripts/                    Local daemon and TUI launchers
├── main.go                     Hydra daemon entry point
├── build.sh                    Release build and package script
└── nfpm.yaml                   Debian/RPM package definition
```

## Download Lifecycle

1. A client submits a URL, destination, optional headers, limits, checksum,
   batch, or schedule information.
2. The daemon validates the request and creates a persistent job.
3. The queue either starts the job or keeps it queued/scheduled.
4. The handshake determines file size, range support, and response metadata.
5. The worker allocates the destination and downloads ranges in parallel.
6. Progress is persisted and broadcast through the event stream.
7. Pause, cancellation, failure, and process interruption preserve enough
   state for safe recovery.
8. The completed file is verified, including an optional checksum.
9. The job is marked completed, or cleaned up and marked failed with an error.

## API and IPC

The local HTTP API is used internally by the browser extension and TUI. It
supports:

- Single and batch download creation.
- Job listing and job-detail retrieval.
- Pause, resume, and delete operations.
- Queue and settings inspection.
- Directory browsing and disk-space checks.
- Server-sent event telemetry.

The CLI uses the Unix socket for local engine control. API requests require
the configured `HYDRA_API_TOKEN`; browser-facing routes additionally enforce
the engine's localhost-origin policy where applicable. This API is not
intended to be an externally hosted or publicly exposed service.

## Persistence Model

The `jobs` table stores:

- Identity, URL, filename, and destination.
- Progress, speed, ETA, and status.
- Serialized chunk and request-header state.
- Batch and scheduling metadata.
- Expected checksum and verification result.
- Error and completion timestamps.

State writes are atomic, and resume logic validates persisted chunk boundaries
and the destination before continuing.

## Operational Components

- **Background engine:** internal application component owning the API, queue,
  workers, persistence, and filesystem operations.
- **TUI:** headless-friendly interactive client suitable for SSH sessions.
- **CLI:** lightweight local control client.
- **Firefox extension:** captures browser downloads while preserving relevant
  request headers for authenticated resources.
- **Packaging:** desktop launcher, optional systemd user startup, Debian/RPM
  output, browser extension packaging, and release automation.

## Platform Assumptions

- Linux is the supported daemon platform.
- Go 1.25 or newer is required.
- Firefox is required for browser interception.
- The TUI does not require GTK, Qt, or a graphical desktop.
- The default download directory is `~/Downloads`.
