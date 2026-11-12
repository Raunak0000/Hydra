# Hydra Project Roadmap

Hydra is intended to be a native Linux download manager with an IDM-like
desktop experience. The daemon is an internal background engine, not a
user-facing service that users must operate manually.

## Phase 1: Core Download Engine (Weeks 1-3)

### Week 1: Foundation and network core

- [x] Establish the Go project architecture.
- [x] Implement HTTP handshake and remote metadata discovery.
- [x] Implement the initial multi-threaded download flow.
- [x] Define shared job and chunk models.
- [x] **Milestone:** Download a file reliably through the core engine.

### Week 2: Parallel I/O and worker coordination

- [x] Implement ranged downloads.
- [x] Implement dynamic chunk allocation.
- [x] Implement adaptive work stealing.
- [x] Add Linux file allocation and positional writes.
- [x] Add atomic progress tracking, speed, and ETA reporting.
- [x] **Milestone:** Complete large downloads using parallel workers.

### Week 3: Recovery and lifecycle controls

- [x] Add pause, resume, cancellation, and context-aware shutdown.
- [x] Add retries and worker recovery.
- [x] Add secure destination and filename handling.
- [x] Add large-file validation and progress correctness.
- [x] **Milestone:** Resume interrupted downloads without restarting them.

## Phase 2: Background Engine and Persistence (Weeks 4-6)

### Week 4: Desktop background engine

- [x] Build the headless Hydra engine.
- [x] Add graceful signal handling and shutdown.
- [x] Add XDG-aware data and runtime paths.
- [x] Add SQLite persistence with WAL mode.
- [x] **Milestone:** Hydra can run continuously in a user's desktop session.

### Week 5: Local control and recovery

- [x] Persist job and chunk state for resume.
- [x] Add Unix-socket IPC.
- [x] Add the `hydra` CLI client.
- [x] Add dynamic pause, resume, and delete controls.
- [x] Add socket permission hardening.
- [x] **Milestone:** The background engine survives restarts and remains
  controllable without direct database access.

### Week 6: Installation foundation

- [x] Add systemd user-service configuration.
- [x] Add desktop launcher metadata.
- [x] Add native Linux notifications.
- [x] Add release build automation.
- [x] **Milestone:** Hydra can be installed and started as a desktop
  application component.

## Phase 3: Browser Integration and Internal API (Weeks 7-9)

### Week 7: Local HTTP control API

- [x] Add the HTTP REST gateway.
- [x] Add token-authenticated API requests.
- [x] Add localhost-origin and CORS protections.
- [x] Add job creation, listing, pause, resume, and delete endpoints.
- [x] **Milestone:** Hydra clients can submit and control downloads locally.

### Week 8: Real-time status and queue control

- [x] Add server-sent event progress telemetry.
- [x] Add collision-safe job identifiers.
- [x] Add queue concurrency controls.
- [x] Add automatic queued-job promotion.
- [x] Add retry handling for queued jobs.
- [x] **Milestone:** Users can see live progress while Hydra manages a queue.

### Week 9: Firefox capture

- [x] Build the Firefox download-interception extension.
- [x] Preserve browser cookies, referer, user-agent, and authorization headers.
- [x] Add custom save-path support for intercepted downloads.
- [x] Migrate and harden extension manifests and request handling.
- [x] **Milestone:** A browser download can be captured and managed by Hydra.

## Phase 4: Queue, Batch, and Scheduling (Weeks 10-12)

### Week 10: Batch downloads

- [x] Add batch URL import.
- [x] Add batch dispatch API and batch identifiers.
- [x] Add grouped job status and error reporting.
- [x] **Milestone:** Users can submit and monitor a group of downloads.

### Week 11: Scheduling and download rules

- [x] Add scheduled downloads.
- [x] Add off-peak scheduling.
- [x] Add persistent configuration.
- [x] Add automatic media categorization.
- [x] Add user-facing default rules by file type and destination.
- [x] **Milestone:** Hydra can organize and defer downloads automatically.

### Week 12: Queue validation

- [x] Validate queue promotion and retry behavior.
- [x] Test queue recovery after daemon restart.
- [x] Test schedule behavior across timezone and clock changes.
- [x] **Milestone:** Queued and scheduled downloads behave predictably.

## Phase 5: Performance and Integrity (Weeks 13-15)

### Week 13: Transfer performance

- [x] Add per-job rate limiting.
- [x] Add adaptive worker counts.
- [x] Add connection pooling and larger transfer buffers.
- [x] Add buffer pooling and resilient stream retries.
- [x] Add HTTP/2 support.
- [x] **Milestone:** Hydra provides fast, configurable transfers.

### Week 14: Verification and completion

- [x] Add checksum persistence.
- [x] Add MD5, SHA-256, and CRC32 verification.
- [x] Add completion verification.
- [x] Preserve accurate progress across pause and resume.
- [x] **Milestone:** Completed files can be verified before being reported
  as finished.

### Week 15: Performance validation

- [x] Benchmark single-stream and ranged downloads.
- [x] Measure rate limiting accuracy.
- [x] Measure memory use with large files and many jobs.
- [x] **Milestone:** Performance characteristics are documented for release.

## Phase 6: Terminal Interface (Weeks 16-18)

### Week 16: TUI controls

- [x] Create the Bubble Tea dashboard.
- [x] Add job selection and scrolling.
- [x] Add pause, resume, delete, and confirmation controls.
- [x] Add refresh and connection-error states.
- [x] Add help, keyboard, and mouse support.
- [x] **Milestone:** Users can manage active downloads from a terminal.

### Week 17: Directory browser

- [x] Add directory navigation.
- [x] Add parent-directory traversal.
- [x] Add directory selection.
- [x] Add symlink-safe path resolution.
- [x] Integrate the browser with the download form.
- [x] **Milestone:** Users can choose safe destinations interactively.

### Week 18: New-download workflow

- [x] Add URL and destination entry.
- [x] Add filename and request-header input.
- [x] Add checksum, speed-limit, and scheduling fields.
- [x] Submit downloads through the background engine API.
- [x] **Milestone:** Hydra has a complete terminal download workflow.

## Phase 7: Packaging and Distribution (Weeks 19-20)

### Week 19: Linux packages

- [x] Build daemon, CLI, and TUI binaries.
- [x] Add Debian/RPM packaging configuration.
- [x] Add systemd and desktop packaging files.
- [x] Add icons and maintainer scripts.
- [x] **Milestone:** Hydra can be installed as a Linux application.

### Week 20: Release automation

- [x] Add GitHub Actions release workflow.
- [x] Document extension packaging and installation.
- [x] Validate package installation, upgrade, and removal.
- [x] **Milestone:** Hydra packages are reproducible and ready for testing.

## Phase 8: Storage and Download Hardening (Weeks 21-23)

### Week 21: Destination and resume safety

- [x] Validate resumed-download destinations.
- [x] Validate persisted chunk ranges.
- [x] Add regression coverage for invalid chunk state.
- [x] Handle destination conflicts safely.
- [x] Clean up failed download initialization.
- [x] **Milestone:** Resume cannot write to an invalid or unintended
  destination.

### Week 22: State and filesystem safety

- [x] Verify download completion before finalizing state.
- [x] Validate available disk space without creating directories.
- [x] Save job state atomically.
- [x] Harden resume integrity and recovery.
- [x] Audit every storage/API error path for explicit, actionable reporting.
- [x] **Milestone:** Failed or interrupted work cannot silently corrupt state.

### Week 23: Hardening validation

- [x] Expand crash, interruption, disk-full, and concurrent-state tests.
- [x] Run race detection across downloader, queue, and storage packages.
- [x] Test symlinks, traversal attempts, conflicts, and unusual filenames.
- [x] **Milestone:** Storage behavior is safe under failure and concurrency.

## Phase 9: IDM-Like Linux Desktop Experience (Weeks 24-27)

**Status: In Progress**

### Week 24: Desktop application shell

- [x] Provide a one-command/package installation experience.
- [x] Start the background engine automatically after user login.
- [x] Add a desktop launcher and optional system-tray/menu-bar controller.
- [x] Add native notifications for queued, completed, and failed jobs.
- [x] **Milestone:** Hydra behaves like a normal installed Linux desktop
  application.

### Week 25: Graphical download manager

- [x] Add a graphical download list.
- [x] Add download details and progress views.
- [x] Add pause, resume, cancel, retry, and delete actions.
- [x] Add destination and filename controls.
- [x] **Milestone:** Users can manage downloads without a terminal.

### Week 26: IDM-style browser workflow

- [x] Automatically capture supported browser downloads.
- [x] Add a confirmation dialog before starting a browser download.
- [x] Allow users to select destination, filename, and download options.
- [x] Add browser integration status and recovery feedback.
- [x] **Milestone:** Clicking a browser download opens a Hydra-managed
  download flow.

### Week 27: Settings and desktop polish

- [x] Add settings for folders, limits, connections, scheduling, and rules.
- [x] Add global/default download rules by file type and destination.
- [x] Make the TUI and CLI optional advanced interfaces.
- [x] Ensure the complete lifecycle is manageable from the desktop.
- [ ] **Milestone:** Hydra provides a complete IDM-like Linux experience.

## Phase 10: Final Validation and Stable Release (Weeks 28-30)

### Week 28: End-to-end testing

- [ ] Run the complete Go test suite and resolve remaining failures.
- [ ] Validate daemon, CLI, TUI, desktop UI, and Firefox extension together.
- [ ] Test interrupted downloads and daemon restarts.
- [ ] Test disk-full behavior and cleanup.
- [ ] **Milestone:** All primary user workflows are verified.

### Week 29: Security and operational review

- [ ] Review API token configuration and remove development defaults.
- [ ] Validate browser authentication-header handling.
- [ ] Test path traversal, symlinks, conflicts, and unusual filenames.
- [ ] Validate package startup, upgrade, removal, and background recovery.
- [ ] **Milestone:** Hydra is safe for real Linux desktop users.

### Week 30: Release

- [ ] Complete user-facing documentation and release notes.
- [ ] Publish installation packages and browser extension instructions.
- [ ] Create a stable versioned release.
- [ ] **Milestone:** Hydra is ready for external users.
