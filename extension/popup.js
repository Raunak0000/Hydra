/**
 * Hydra Download Manager — Dual-Mode Popup Controller
 *
 * Mode 1 (?pending=<id>): IDM-style Confirmation, Destination/Filename Picker, Options & Recovery Dialog.
 * Mode 2 (toolbar icon): Browser Integration Status, Queue Overview & Recovery Feedback Panel.
 */

import {
    buildDestinationPresets,
    evaluateDiskSpace,
    formatBytes,
    formatRelativeTime,
    joinSavePath,
    resolveCategoryDestination,
    truncateURL,
    validateDownloadOptions,
    validateFilename,
} from "./shared.js";

const params = new URLSearchParams(window.location.search);
const pendingId = params.get("pending");

// ─── DOM Helpers ─────────────────────────────────────────────────────────────

const $ = (id) => document.getElementById(id);

function setPill(el, kind, text) {
    el.className = `pill ${kind}`;
    el.innerHTML = `<span class="dot"></span>`;
    el.appendChild(document.createTextNode(text));
}

function sendMsg(msg) {
    return chrome.runtime.sendMessage(msg);
}

// ─── Mode 1: Confirmation & Options Dialog ───────────────────────────────────

function initConfirmView() {
    $("confirm-view").classList.remove("hidden");

    const elHealthPill = $("dlg-health-pill");
    const elRecoveryBanner = $("dlg-recovery-banner");
    const elRecoveryTitle = $("dlg-recovery-title");
    const elRecoveryDetail = $("dlg-recovery-detail");
    const elTokenInline = $("dlg-token-inline");
    const inpInlineToken = $("dlg-inp-token");
    const btnSaveToken = $("dlg-btn-save-token");
    const btnStartDaemon = $("dlg-btn-start-daemon");
    const btnAutoPair = $("dlg-btn-auto-pair");
    const btnReprobe = $("dlg-btn-reprobe");
    const btnOpenSettings = $("dlg-btn-open-settings");

    const elSize = $("info-size");
    const elURL = $("info-url");
    const tagHost = $("tag-host");
    const tagHeaders = $("tag-headers");

    const inpFilename = $("inp-filename");
    const elFilenameErr = $("filename-validation");
    const selPreset = $("sel-preset");
    const inpDestDir = $("inp-dest-dir");
    const btnBrowse = $("btn-browse");
    const chkPendingPath = $("chk-pending-path");
    const elDirDrawer = $("dir-drawer");
    const elDirPath = $("dir-drawer-path");
    const elDirList = $("dir-drawer-list");
    const btnDirUp = $("btn-dir-up");
    const btnDirSelect = $("btn-dir-select");
    const btnDirClose = $("btn-dir-close");
    const elPathPreview = $("path-preview");
    const elDiskHint = $("disk-space-hint");

    const selStartMode = $("sel-start-mode");
    const elSchedField = $("schedule-field");
    const inpSchedAt = $("inp-schedule-at");
    const inpSpeedKbps = $("inp-speed-kbps");
    const selChecksumAlgo = $("sel-checksum-algo");
    const inpChecksumHex = $("inp-checksum-hex");
    const chkIncludeHeaders = $("chk-include-headers");

    const elStatus = $("dialog-status");
    const btnDiscard = $("btn-discard");
    const btnBrowserFallback = $("btn-browser-fallback");
    const btnConfirm = $("btn-confirm");
    const spinner = $("confirm-spinner");

    let item = null;
    let health = null;
    let daemonSettings = null;
    let userTouchedFolder = false;
    let drawerParent = "";
    let drawerCurrent = "";
    let busy = false;
    let diskTimer = null;

    function setDialogStatus(msg, cls = "") {
        elStatus.textContent = msg;
        elStatus.className = cls;
    }

    function setBusy(state) {
        busy = state;
        btnConfirm.disabled = state;
        btnBrowserFallback.disabled = state;
        btnDiscard.disabled = state;
        spinner.style.display = state ? "inline-block" : "none";
    }

    function applyHealthBanner(h, explicitError = null) {
        health = h;
        const summary = h?.summary;
        if (!summary) return;

        const pillKind =
            summary.state === "connected"
                ? "ok"
                : summary.state === "offline"
                  ? "error"
                  : "warn";
        const shortLabel =
            summary.state === "connected"
                ? "Daemon Ready"
                : summary.state === "offline"
                  ? "Daemon Offline"
                  : summary.state === "needs_token"
                    ? "Token Required"
                    : summary.headline;
        setPill(elHealthPill, pillKind, shortLabel);

        const hasError = explicitError || summary.state === "offline" || summary.state === "needs_token";
        if (!hasError) {
            elRecoveryBanner.classList.add("hidden");
            return;
        }

        elRecoveryBanner.classList.remove("hidden");
        elRecoveryBanner.classList.toggle("error", summary.state === "offline" || Boolean(explicitError?.offline));
        elRecoveryBanner.classList.toggle("warn", !(summary.state === "offline" || Boolean(explicitError?.offline)));

        if (explicitError && summary.state === "connected") {
            elRecoveryTitle.textContent = `Recovery Needed (${explicitError.errorCode || "Error"})`;
            elRecoveryDetail.textContent = explicitError.error || "Download dispatch failed.";
        } else {
            elRecoveryTitle.textContent = summary.headline;
            elRecoveryDetail.textContent =
                explicitError?.error ||
                (summary.recoverySteps?.length ? summary.recoverySteps.join(" ") : summary.detail);
        }

        btnStartDaemon.classList.toggle("hidden", !(h.nativeHostAvailable && !h.daemonReachable));
        btnAutoPair.classList.toggle(
            "hidden",
            !(h.nativeHostAvailable && h.daemonReachable && !h.authenticated)
        );
        elTokenInline.classList.toggle("hidden", !(h.daemonReachable && !h.authenticated));
    }

    function populatePresets() {
        const filename = inpFilename.value.trim() || item?.filename || "";
        const recent = health?.settings?.recentDirectories || [];
        const presets = buildDestinationPresets(filename, daemonSettings, recent, "~/Downloads");

        const prevSelected = selPreset.value;
        selPreset.innerHTML = "";
        for (const p of presets) {
            const opt = document.createElement("option");
            opt.value = p.path;
            opt.textContent = p.label;
            selPreset.appendChild(opt);
        }

        const customOpt = document.createElement("option");
        customOpt.value = "__custom__";
        customOpt.textContent = "Custom Folder…";
        selPreset.appendChild(customOpt);

        if (!userTouchedFolder) {
            const auto = resolveCategoryDestination(filename, daemonSettings, "~/Downloads");
            inpDestDir.value = auto.path;
            selPreset.value = auto.path;
        } else {
            const matched = presets.some((p) => p.path === inpDestDir.value.trim());
            selPreset.value = matched ? inpDestDir.value.trim() : "__custom__";
            if (!selPreset.value && prevSelected) selPreset.value = "__custom__";
        }
    }

    function refreshPathPreviewAndSpace() {
        const filename = inpFilename.value.trim();
        const nameErr = validateFilename(filename);
        if (nameErr) {
            elFilenameErr.textContent = nameErr;
            elFilenameErr.classList.remove("hidden");
        } else {
            elFilenameErr.classList.add("hidden");
        }

        const pendingMode = chkPendingPath.checked;
        inpDestDir.disabled = pendingMode;
        selPreset.disabled = pendingMode;
        btnBrowse.disabled = pendingMode;

        if (pendingMode) {
            elPathPreview.textContent = "Target: Decide destination later inside Hydra (PENDING_PATH)";
            elDiskHint.textContent = "Disk space check skipped until folder is chosen.";
            elDiskHint.className = "hint-line";
            return;
        }

        const folder = inpDestDir.value.trim() || "~/Downloads";
        elPathPreview.textContent = `Target: ${joinSavePath(folder, filename || "downloaded_file.bin")}`;

        clearTimeout(diskTimer);
        diskTimer = setTimeout(async () => {
            if (!health?.daemonReachable || !health?.authenticated) {
                elDiskHint.textContent = "Free disk space: available once daemon is connected.";
                elDiskHint.className = "hint-line";
                return;
            }
            const resp = await sendMsg({
                type: "CHECK_DISK_SPACE",
                path: folder,
                requiredBytes: item?.contentLength || 0,
            });
            const evalRes = evaluateDiskSpace(resp?.ok ? resp.info : null, item?.contentLength || 0);
            elDiskHint.textContent = evalRes.message;
            elDiskHint.className = `hint-line ${evalRes.level === "ok" ? "ok" : evalRes.level === "error" ? "error" : ""}`;
        }, 180);
    }

    async function loadDirectoryDrawer(targetPath = "") {
        elDirDrawer.classList.remove("hidden");
        elDirList.innerHTML = `<div class="hint-line">Loading folders…</div>`;
        const resp = await sendMsg({ type: "LIST_DIRECTORY", path: targetPath });
        if (!resp?.ok) {
            elDirList.innerHTML = `<div class="hint-line error">${resp?.error || "Cannot list directories (is daemon authenticated?)"}</div>`;
            return;
        }

        drawerCurrent = resp.current || targetPath;
        drawerParent = resp.parent || "";
        elDirPath.textContent = drawerCurrent;
        btnDirUp.disabled = !drawerParent;

        const dirs = Array.isArray(resp.directories) ? resp.directories : [];
        elDirList.innerHTML = "";
        if (dirs.length === 0) {
            elDirList.innerHTML = `<div class="hint-line">No subdirectories</div>`;
            return;
        }
        for (const d of dirs) {
            const btn = document.createElement("button");
            btn.type = "button";
            btn.className = "dir-item";
            btn.textContent = `📁 ${d.name}`;
            btn.addEventListener("click", () => {
                void loadDirectoryDrawer(d.path);
            });
            elDirList.appendChild(btn);
        }
    }

    async function probeContext() {
        const res = await sendMsg({ type: "PROBE_DIALOG_CONTEXT" });
        if (!res?.ok) return;
        daemonSettings = res.daemonSettings || null;
        applyHealthBanner(res.health, item?.initialDispatchError || null);

        // Apply user's default options from extension settings once on initial load
        if (res.health?.settings) {
            const s = res.health.settings;
            if (!selStartMode.dataset.touched && s.defaultStartMode) {
                selStartMode.value = s.defaultStartMode;
                elSchedField.classList.toggle("hidden", selStartMode.value !== "schedule");
            }
            if (!chkIncludeHeaders.dataset.touched) {
                chkIncludeHeaders.checked = s.includeHeadersByDefault !== false;
            }
        }

        populatePresets();
        refreshPathPreviewAndSpace();
    }

    async function loadPending() {
        const resp = await sendMsg({ type: "GET_PENDING", id: pendingId });
        if (!resp?.ok || !resp.item) {
            setPill(elHealthPill, "warn", "Expired");
            setDialogStatus(resp?.error || "Pending download not found (may have already been started).", "error");
            btnConfirm.disabled = true;
            btnBrowserFallback.disabled = true;
            return;
        }

        item = resp.item;
        elSize.textContent = formatBytes(item.contentLength);
        elURL.textContent = truncateURL(item.url, 62);
        elURL.title = item.url;
        inpFilename.value = item.filename || "downloaded_file.bin";

        try {
            tagHost.textContent = new URL(item.url).hostname;
        } catch {
            tagHost.textContent = "HTTP/HTTPS";
        }
        const headerCount = Object.keys(item.capturedHeaders || {}).length;
        const hasAuth = Boolean(
            item.capturedHeaders?.Cookie ||
                item.capturedHeaders?.cookie ||
                item.capturedHeaders?.Authorization ||
                item.capturedHeaders?.authorization
        );
        if (hasAuth) {
            setPill(tagHeaders, "ok", "Session Cookies & Auth Captured");
        } else if (headerCount > 0) {
            setPill(tagHeaders, "muted", `${headerCount} Header(s) Captured`);
        } else {
            setPill(tagHeaders, "muted", "Direct Link");
        }

        if (item.initialDispatchError) {
            $("options-details").open = true;
        }

        await probeContext();
    }

    // ─── Event Wiring ────────────────────────────────────────────────────────

    inpFilename.addEventListener("input", () => {
        populatePresets();
        refreshPathPreviewAndSpace();
    });

    selPreset.addEventListener("change", () => {
        if (selPreset.value === "__custom__") {
            userTouchedFolder = true;
            inpDestDir.focus();
            return;
        }
        userTouchedFolder = true;
        inpDestDir.value = selPreset.value;
        refreshPathPreviewAndSpace();
    });

    inpDestDir.addEventListener("input", () => {
        userTouchedFolder = true;
        selPreset.value = "__custom__";
        refreshPathPreviewAndSpace();
    });

    chkPendingPath.addEventListener("change", refreshPathPreviewAndSpace);

    btnBrowse.addEventListener("click", async () => {
        btnBrowse.disabled = true;
        btnBrowse.textContent = "Opening…";
        const resp = await sendMsg({ type: "BROWSE_FOLDER", currentPath: inpDestDir.value.trim() });
        btnBrowse.disabled = false;
        btnBrowse.textContent = "Browse…";

        if (resp?.ok && resp.path) {
            userTouchedFolder = true;
            inpDestDir.value = resp.path;
            selPreset.value = "__custom__";
            elDirDrawer.classList.add("hidden");
            refreshPathPreviewAndSpace();
        } else {
            // Fall back cleanly to inline directory drawer if desktop zenity/kdialog unavailable
            await loadDirectoryDrawer(inpDestDir.value.trim());
        }
    });

    btnDirUp.addEventListener("click", () => {
        if (drawerParent) void loadDirectoryDrawer(drawerParent);
    });
    btnDirSelect.addEventListener("click", () => {
        if (drawerCurrent) {
            userTouchedFolder = true;
            inpDestDir.value = drawerCurrent;
            selPreset.value = "__custom__";
            refreshPathPreviewAndSpace();
        }
        elDirDrawer.classList.add("hidden");
    });
    btnDirClose.addEventListener("click", () => elDirDrawer.classList.add("hidden"));

    selStartMode.addEventListener("change", () => {
        selStartMode.dataset.touched = "1";
        const isSched = selStartMode.value === "schedule";
        elSchedField.classList.toggle("hidden", !isSched);
        if (isSched && !inpSchedAt.value) {
            const d = new Date(Date.now() + 30 * 60 * 1000);
            const pad = (n) => String(n).padStart(2, "0");
            inpSchedAt.value = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(
                d.getHours()
            )}:${pad(d.getMinutes())}`;
        }
    });
    chkIncludeHeaders.addEventListener("change", () => {
        chkIncludeHeaders.dataset.touched = "1";
    });

    btnReprobe.addEventListener("click", () => void probeContext());
    btnOpenSettings.addEventListener("click", () => chrome.runtime.openOptionsPage());

    btnAutoPair.addEventListener("click", async () => {
        btnAutoPair.disabled = true;
        const resp = await sendMsg({ type: "PAIR_VIA_NATIVE_HOST" });
        btnAutoPair.disabled = false;
        if (resp?.ok) {
            await probeContext();
            setDialogStatus("✓ Paired with local Hydra token.", "success");
        } else {
            setDialogStatus(resp?.error || "Auto-pair failed.", "error");
        }
    });

    btnStartDaemon.addEventListener("click", async () => {
        btnStartDaemon.disabled = true;
        btnStartDaemon.textContent = "Starting…";
        const resp = await sendMsg({ type: "START_DAEMON_AND_RETRY" });
        btnStartDaemon.disabled = false;
        btnStartDaemon.textContent = "Start Hydra Daemon";
        if (resp?.ok) {
            await probeContext();
            setDialogStatus("✓ Hydra daemon started.", "success");
        } else {
            setDialogStatus(resp?.native?.error || "Could not start Hydra daemon.", "error");
        }
    });

    btnSaveToken.addEventListener("click", async () => {
        const tok = inpInlineToken.value.trim();
        if (!tok) return;
        btnSaveToken.disabled = true;
        await sendMsg({ type: "SAVE_INLINE_TOKEN", token: tok });
        btnSaveToken.disabled = false;
        await probeContext();
    });

    btnConfirm.addEventListener("click", async () => {
        if (busy || !item) return;

        const options = {
            filename: inpFilename.value.trim(),
            saveMode: chkPendingPath.checked ? "pending" : "folder",
            destinationDir: inpDestDir.value.trim(),
            speedLimitKbps: Number(inpSpeedKbps.value || 0),
            checksumAlgo: selChecksumAlgo.value,
            expectedChecksum: inpChecksumHex.value.trim(),
            startMode: selStartMode.value,
            scheduledAt: inpSchedAt.value,
            includeHeaders: chkIncludeHeaders.checked,
        };

        const validationErr = validateDownloadOptions(options);
        if (validationErr) {
            setDialogStatus(validationErr, "error");
            return;
        }

        setBusy(true);
        setDialogStatus("Sending to Hydra daemon…");

        const resp = await sendMsg({
            type: "CONFIRM_DOWNLOAD",
            id: pendingId,
            options,
        });

        if (resp?.ok) {
            const label =
                resp.jobStatus === "PAUSED"
                    ? "✓ Added to Hydra (Paused)"
                    : resp.jobStatus === "SCHEDULED"
                      ? "✓ Scheduled in Hydra"
                      : "✓ Queued in Hydra!";
            setDialogStatus(label, "success");
            setTimeout(() => window.close(), 950);
            return;
        }

        // Preserve dialog & inputs so user can fix destination/token/start daemon or fall back to browser!
        setBusy(false);
        setDialogStatus(resp?.error || "Failed to send download to Hydra.", "error");
        const recheck = await sendMsg({ type: "PROBE_DIALOG_CONTEXT" });
        if (recheck?.ok) {
            applyHealthBanner(recheck.health, resp);
        }
    });

    btnBrowserFallback.addEventListener("click", async () => {
        if (busy) return;
        setBusy(true);
        setDialogStatus("Handing back to browser…");
        await sendMsg({ type: "CANCEL_DOWNLOAD", id: pendingId });
        window.close();
    });

    btnDiscard.addEventListener("click", async () => {
        if (busy) return;
        setBusy(true);
        await sendMsg({ type: "DISCARD_DOWNLOAD", id: pendingId });
        window.close();
    });

    document.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && !e.shiftKey && document.activeElement?.tagName !== "BUTTON") {
            e.preventDefault();
            btnConfirm.click();
        }
    });

    void loadPending();
}

// ─── Mode 2: Toolbar Integration Status & Recovery Panel ─────────────────────

function initStatusView() {
    $("status-view").classList.remove("hidden");

    const elPill = $("tb-health-pill");
    const elBanner = $("tb-recovery-banner");
    const elBannerTitle = $("tb-recovery-title");
    const elBannerDetail = $("tb-recovery-detail");
    const btnStartDaemon = $("tb-btn-start-daemon");
    const btnAutoPair = $("tb-btn-auto-pair");
    const btnRetry = $("tb-btn-retry");
    const btnSettings = $("tb-btn-settings");

    const elDiagDaemon = $("diag-daemon");
    const elDiagAuth = $("diag-auth");
    const elDiagBridge = $("diag-bridge");
    const elDiagQueue = $("diag-queue");

    const chkAutoCapture = $("tb-chk-autocapture");
    const chkConfirm = $("tb-chk-confirm");
    const elEventsList = $("events-list");

    const btnClear = $("tb-btn-clear-events");
    const btnOpenGUI = $("tb-btn-open-gui");
    const btnRefresh = $("tb-btn-refresh");
    const btnOpenOptions = $("tb-btn-open-options");

    function renderEvents(events = []) {
        elEventsList.innerHTML = "";
        if (!Array.isArray(events) || events.length === 0) {
            elEventsList.innerHTML = `<div class="hint-line">No browser downloads captured yet.</div>`;
            return;
        }

        for (const ev of events) {
            const row = document.createElement("div");
            row.className = "event-row";

            const info = document.createElement("div");
            info.style.minWidth = "0";
            const name = document.createElement("div");
            name.className = "event-name";
            name.textContent = ev.filename || ev.url;

            const outcomeLabels = {
                queued: "Queued in Hydra",
                paused: "Added Paused",
                scheduled: "Scheduled",
                browser: "Browser Fallback",
                failed: `Failed: ${ev.error || ev.errorCode || "Error"}`,
            };

            const meta = document.createElement("div");
            meta.className = "event-meta";
            const sizePart = ev.contentLength > 0 ? ` • ${formatBytes(ev.contentLength)}` : "";
            meta.textContent = `${outcomeLabels[ev.outcome] || ev.outcome}${sizePart} • ${formatRelativeTime(ev.timestamp)}`;

            info.appendChild(name);
            info.appendChild(meta);
            row.appendChild(info);

            if (ev.outcome === "failed") {
                const acts = document.createElement("div");
                acts.className = "event-actions";

                const retryBtn = document.createElement("button");
                retryBtn.type = "button";
                retryBtn.className = "btn btn-sm btn-primary";
                retryBtn.textContent = "Retry";
                retryBtn.addEventListener("click", async () => {
                    await sendMsg({ type: "RETRY_EVENT", eventId: ev.id });
                    window.close();
                });

                const browserBtn = document.createElement("button");
                browserBtn.type = "button";
                browserBtn.className = "btn btn-sm";
                browserBtn.textContent = "Browser";
                browserBtn.addEventListener("click", async () => {
                    await sendMsg({ type: "FALLBACK_EVENT_TO_BROWSER", eventId: ev.id });
                    void refreshStatus();
                });

                acts.appendChild(retryBtn);
                acts.appendChild(browserBtn);
                row.appendChild(acts);
            }

            elEventsList.appendChild(row);
        }
    }

    function applyStatusResponse(resp) {
        const h = resp?.health;
        if (!h) return;
        const s = h.summary;

        const pillKind =
            s.state === "connected" ? "ok" : s.state === "offline" ? "error" : s.state === "paused" ? "muted" : "warn";
        setPill(elPill, pillKind, s.badgeText === "ON" ? "Connected" : s.badgeText);

        elDiagDaemon.textContent = h.daemonReachable ? "Running (9000)" : "Offline";
        elDiagDaemon.style.color = h.daemonReachable ? "var(--ok)" : "var(--danger)";

        elDiagAuth.textContent = h.authenticated ? "Paired ✓" : h.tokenConfigured ? "Rejected ✗" : "Not Paired";
        elDiagAuth.style.color = h.authenticated ? "var(--ok)" : "var(--warn)";

        elDiagBridge.textContent = h.nativeHostAvailable ? "Installed ✓" : "HTTP-Only";
        elDiagBridge.style.color = h.nativeHostAvailable ? "var(--ok)" : "var(--muted)";

        if (Array.isArray(resp.jobs)) {
            const active = resp.jobs.filter((j) => j.status === "DOWNLOADING").length;
            const queued = resp.jobs.filter((j) => j.status === "QUEUED" || j.status === "PENDING_PATH").length;
            const failed = resp.jobs.filter((j) => j.status === "FAILED").length;
            elDiagQueue.textContent = `${active} active • ${queued} queued${failed ? ` • ${failed} failed` : ""}`;
        } else {
            elDiagQueue.textContent = h.daemonReachable ? "Auth required" : "Unavailable";
        }

        chkAutoCapture.checked = h.settings?.autoCapture !== false;
        chkConfirm.checked = h.settings?.confirmBeforeDownload !== false;

        const showBanner = s.state !== "connected";
        elBanner.classList.toggle("hidden", !showBanner);
        if (showBanner) {
            elBanner.classList.toggle("error", s.state === "offline");
            elBanner.classList.toggle("warn", s.state !== "offline");
            elBannerTitle.textContent = s.headline;
            elBannerDetail.textContent = s.recoverySteps?.[0] || s.detail;
            btnStartDaemon.classList.toggle("hidden", !(h.nativeHostAvailable && !h.daemonReachable));
            btnAutoPair.classList.toggle("hidden", !(h.nativeHostAvailable && h.daemonReachable && !h.authenticated));
        }

        renderEvents(h.recentEvents || []);
    }

    async function refreshStatus() {
        const resp = await sendMsg({ type: "GET_INTEGRATION_STATUS" });
        if (resp?.ok) applyStatusResponse(resp);
    }

    chkAutoCapture.addEventListener("change", async () => {
        await sendMsg({ type: "SET_CAPTURE_PREFS", autoCapture: chkAutoCapture.checked });
        void refreshStatus();
    });

    chkConfirm.addEventListener("change", async () => {
        await sendMsg({ type: "SET_CAPTURE_PREFS", confirmBeforeDownload: chkConfirm.checked });
        void refreshStatus();
    });

    btnStartDaemon.addEventListener("click", async () => {
        btnStartDaemon.disabled = true;
        await sendMsg({ type: "START_DAEMON_AND_RETRY" });
        btnStartDaemon.disabled = false;
        void refreshStatus();
    });

    btnAutoPair.addEventListener("click", async () => {
        btnAutoPair.disabled = true;
        await sendMsg({ type: "PAIR_VIA_NATIVE_HOST" });
        btnAutoPair.disabled = false;
        void refreshStatus();
    });

    btnRetry.addEventListener("click", () => void refreshStatus());
    btnRefresh.addEventListener("click", () => void refreshStatus());
    btnSettings.addEventListener("click", () => chrome.runtime.openOptionsPage());
    btnOpenOptions.addEventListener("click", () => chrome.runtime.openOptionsPage());

    btnClear.addEventListener("click", async () => {
        await sendMsg({ type: "CLEAR_RECENT_EVENTS" });
        void refreshStatus();
    });

    btnOpenGUI.addEventListener("click", async () => {
        const resp = await sendMsg({ type: "OPEN_HYDRA_GUI" });
        if (resp?.ok) {
            window.close();
        } else {
            elDiagBridge.textContent = resp?.error || "Bridge missing";
        }
    });

    void refreshStatus();
}

// ─── Bootstrap ───────────────────────────────────────────────────────────────

if (pendingId) {
    initConfirmView();
} else {
    initStatusView();
}
