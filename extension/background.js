/**
 * Hydra Download Manager — Background Interceptor & Integration Worker
 */

import {
    DEFAULT_SETTINGS,
    MAX_RECENT_DIRS,
    MAX_RECENT_EVENTS,
    NATIVE_HOST_NAME,
    buildDownloadPayload,
    consumeBypassStage,
    createBypassEntry,
    evaluateDownloadItemForInterception,
    evaluateResponseForInterception,
    filenameFromURL,
    isHydraEndpoint,
    normalizeDaemonURL,
    pruneBypassMap,
    resolveCategoryDestination,
    sanitizeFilename,
    summarizeIntegrationHealth,
} from "./shared.js";

const DEDUP_WINDOW_MS = 3_000;
const BYPASS_TTL_MS = 60_000;
const META_CACHE_TTL_MS = 20_000;
const PENDING_TTL_MS = 2 * 60 * 60 * 1000; // 2 hours
const SESSION_KEY = "hydraSessionState";

// In-memory hot state (rehydrated from session storage on worker wakeup)
const requestHeadersByID = new Map();
const recentDispatches = new Map();
const responseMetaCache = new Map();

let sessionLoaded = false;
let sessionCache = {
    pending: {}, // id -> pendingItem
    bypass: {}, // url -> expiresAtMs
    counter: 0,
};

// ─── Storage helpers ─────────────────────────────────────────────────────────

function getSessionArea() {
    return chrome.storage?.session || chrome.storage.local;
}

async function ensureSessionLoaded() {
    if (sessionLoaded) return sessionCache;
    try {
        const area = getSessionArea();
        const raw = await area.get(SESSION_KEY);
        const saved = raw?.[SESSION_KEY];
        if (saved && typeof saved === "object") {
            sessionCache = {
                pending: saved.pending || {},
                bypass: saved.bypass || {},
                counter: Number(saved.counter || 0),
            };
        }
    } catch {
        /* ignore */
    }
    pruneSessionMaps();
    sessionLoaded = true;
    return sessionCache;
}

function pruneSessionMaps() {
    const now = Date.now();
    pruneBypassMap(sessionCache.bypass, now);
    for (const [id, item] of Object.entries(sessionCache.pending)) {
        if (!item?.createdAt || now - item.createdAt > PENDING_TTL_MS) {
            delete sessionCache.pending[id];
        }
    }
    for (const [key, meta] of responseMetaCache.entries()) {
        if (!meta?.timestamp || now - meta.timestamp > META_CACHE_TTL_MS) {
            responseMetaCache.delete(key);
        }
    }
    for (const [url, ts] of recentDispatches.entries()) {
        if (now - ts > DEDUP_WINDOW_MS) {
            recentDispatches.delete(url);
        }
    }
}

async function saveSessionState() {
    pruneSessionMaps();
    try {
        await getSessionArea().set({ [SESSION_KEY]: sessionCache });
    } catch {
        /* ignore quota / shutdown errors */
    }
}

async function loadSettings() {
    const data = await chrome.storage.local.get(DEFAULT_SETTINGS);
    return {
        ...DEFAULT_SETTINGS,
        ...data,
        daemonURL: normalizeDaemonURL(data.daemonURL),
    };
}

async function saveSettings(partial) {
    const current = await loadSettings();
    const next = {
        ...current,
        ...partial,
        daemonURL: normalizeDaemonURL(partial.daemonURL ?? current.daemonURL),
    };
    await chrome.storage.local.set(next);
    return next;
}

async function loadRuntimeMeta() {
    const data = await chrome.storage.local.get({
        hydraRecentEvents: [],
        hydraLastError: "",
        hydraNativeAutoPaired: false,
    });
    return {
        recentEvents: Array.isArray(data.hydraRecentEvents) ? data.hydraRecentEvents : [],
        lastError: String(data.hydraLastError || ""),
        nativeAutoPaired: Boolean(data.hydraNativeAutoPaired),
    };
}

async function recordEvent(entry) {
    const { recentEvents } = await loadRuntimeMeta();
    const record = {
        id: `ev_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`,
        timestamp: Date.now(),
        ...entry,
    };
    const next = [record, ...recentEvents.filter((ev) => ev.id !== record.id)].slice(0, MAX_RECENT_EVENTS);
    const nextLastError =
        entry.outcome === "failed"
            ? entry.error || "Last download dispatch failed."
            : entry.outcome === "queued" || entry.outcome === "paused" || entry.outcome === "scheduled"
              ? ""
              : undefined;

    const patch = { hydraRecentEvents: next };
    if (nextLastError !== undefined) {
        patch.hydraLastError = nextLastError;
    }
    await chrome.storage.local.set(patch);
    return record;
}

async function recordRecentDirectory(dirPath) {
    const clean = String(dirPath || "").trim();
    if (!clean || clean === "DEFAULT" || clean === "PENDING") return;
    const settings = await loadSettings();
    const merged = [clean, ...(settings.recentDirectories || []).filter((d) => d !== clean)].slice(0, MAX_RECENT_DIRS);
    await saveSettings({
        recentDirectories: merged,
        lastDestinationDir: clean,
    });
}

function isDuplicate(url) {
    const now = Date.now();
    for (const [key, ts] of recentDispatches.entries()) {
        if (now - ts > DEDUP_WINDOW_MS) recentDispatches.delete(key);
    }
    if (recentDispatches.has(url)) return true;
    recentDispatches.set(url, now);
    return false;
}

async function addBypassUrl(url) {
    await ensureSessionLoaded();
    sessionCache.bypass[url] = createBypassEntry(BYPASS_TTL_MS);
    await saveSessionState();
}

async function consumeBypassUrl(url, stage) {
    await ensureSessionLoaded();
    const matched = consumeBypassStage(sessionCache.bypass, url, stage);
    if (matched) {
        await saveSessionState();
    }
    return matched;
}

// ─── Native Messaging & Daemon HTTP Helpers ──────────────────────────────────

async function sendNative(payload, timeoutMs = 4500) {
    if (!chrome.runtime?.sendNativeMessage) {
        return { ok: false, available: false, error: "nativeMessaging API unavailable" };
    }
    try {
        const timer = new Promise((_, reject) =>
            setTimeout(() => reject(new Error("Native bridge timed out")), timeoutMs)
        );
        const resp = await Promise.race([
            chrome.runtime.sendNativeMessage(NATIVE_HOST_NAME, payload),
            timer,
        ]);
        if (!resp || typeof resp !== "object") {
            return { ok: false, available: true, error: "Empty response from Hydra native bridge" };
        }
        return { available: true, ...resp };
    } catch (err) {
        return {
            ok: false,
            available: false,
            error: err?.message || String(err),
        };
    }
}

async function fetchDaemonJSON(daemonURL, path, { method = "GET", token = "", body = null, timeoutMs = 2500 } = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const headers = {};
    if (token) headers["X-Hydra-Token"] = token;
    if (body !== null) headers["Content-Type"] = "application/json";

    try {
        const res = await fetch(`${normalizeDaemonURL(daemonURL)}${path}`, {
            method,
            headers,
            body: body !== null ? JSON.stringify(body) : undefined,
            signal: controller.signal,
        });
        const text = await res.text();
        let data = {};
        if (text) {
            try {
                data = JSON.parse(text);
            } catch {
                data = { raw: text };
            }
        }
        return {
            reachable: true,
            ok: res.ok,
            status: res.status,
            data,
        };
    } catch (err) {
        return {
            reachable: false,
            ok: false,
            status: 0,
            error: err?.name === "AbortError" ? "Connection timed out" : err?.message || String(err),
            data: {},
        };
    } finally {
        clearTimeout(timer);
    }
}

/**
 * Probes daemon health and optionally auto-pairs the API token via native bridge.
 */
async function probeIntegrationHealth({ tryAutoPair = true } = {}) {
    let settings = await loadSettings();
    const runtime = await loadRuntimeMeta();

    let healthRes = await fetchDaemonJSON(settings.daemonURL, "/api/health", {
        token: settings.apiToken,
        timeoutMs: 2000,
    });

    let nativeInfo = await sendNative({ action: "get_integration_info" });

    // Auto-import token from native bridge when extension has no token or stale rejected token.
    if (
        tryAutoPair &&
        nativeInfo.available &&
        nativeInfo.token &&
        (!settings.apiToken || (healthRes.reachable && !healthRes.data?.authenticated))
    ) {
        settings = await saveSettings({ apiToken: nativeInfo.token });
        await chrome.storage.local.set({ hydraNativeAutoPaired: true });
        healthRes = await fetchDaemonJSON(settings.daemonURL, "/api/health", {
            token: settings.apiToken,
            timeoutMs: 2000,
        });
    }

    const daemonReachable = Boolean(healthRes.reachable && healthRes.ok);
    const authenticated = Boolean(daemonReachable && healthRes.data?.authenticated);

    const summary = summarizeIntegrationHealth({
        daemonReachable,
        authenticated,
        tokenConfigured: Boolean(settings.apiToken),
        nativeHostAvailable: Boolean(nativeInfo.available),
        autoCapture: settings.autoCapture !== false,
        lastError: daemonReachable ? runtime.lastError : healthRes.error || "",
    });

    await applyToolbarBadge(summary);

    return {
        settings,
        daemonReachable,
        authenticated,
        tokenConfigured: Boolean(settings.apiToken),
        nativeHostAvailable: Boolean(nativeInfo.available),
        guiAvailable: Boolean(nativeInfo.gui_available),
        summary,
        recentEvents: runtime.recentEvents,
        lastError: runtime.lastError,
    };
}

async function applyToolbarBadge(summary) {
    if (!chrome.action) return;
    try {
        await chrome.action.setBadgeText({ text: summary.badgeText || "" });
        await chrome.action.setBadgeBackgroundColor({ color: summary.badgeColor || "#5b8dee" });
        await chrome.action.setTitle({
            title: `Hydra Download Manager — ${summary.headline}`,
        });
    } catch {
        /* ignore if browser lacks badge method */
    }
}

async function showDesktopNotification(title, message) {
    const settings = await loadSettings();
    if (!settings.notificationsEnabled || !chrome.notifications) return;
    try {
        await chrome.notifications.create({
            type: "basic",
            iconUrl: "icons/icon48.png",
            title,
            message,
        });
    } catch {
        /* ignore notification permission issues */
    }
}

// ─── Pending Dialog & Dispatch Flow ──────────────────────────────────────────

async function openConfirmationWindow(pendingId) {
    const popupURL = chrome.runtime.getURL(`popup.html?pending=${encodeURIComponent(pendingId)}`);
    try {
        await chrome.windows.create({
            url: popupURL,
            type: "popup",
            width: 560,
            height: 640,
            focused: true,
        });
    } catch {
        if (chrome.tabs?.create) {
            await chrome.tabs.create({ url: popupURL, active: true });
        }
    }
}

async function dispatchItemToHydra(item, options = {}) {
    let settings = await loadSettings();

    // Try auto-pairing once if token is empty before hitting daemon
    if (!settings.apiToken) {
        const nativeInfo = await sendNative({ action: "get_integration_info" });
        if (nativeInfo.available && nativeInfo.token) {
            settings = await saveSettings({ apiToken: nativeInfo.token });
        }
    }

    if (!settings.apiToken) {
        return {
            ok: false,
            status: 401,
            errorCode: "MISSING_TOKEN",
            error: "API token is not configured yet. Paste ~/.config/hydra/api.token or click Auto-Pair.",
            offline: false,
        };
    }

    // Fill cookies if user kept header forwarding enabled and none came from webRequest
    const hydratedItem = {
        ...item,
        capturedHeaders: { ...(item.capturedHeaders || {}) },
    };
    if (options.includeHeaders !== false && !hydratedItem.capturedHeaders.Cookie && !hydratedItem.capturedHeaders.cookie) {
        try {
            const cookies = await chrome.cookies.getAll({ url: item.url });
            if (Array.isArray(cookies) && cookies.length > 0) {
                hydratedItem.capturedHeaders.Cookie = cookies.map((c) => `${c.name}=${c.value}`).join("; ");
            }
        } catch {
            /* ignore restricted cookies */
        }
    }

    const payload = buildDownloadPayload(hydratedItem, options);
    const res = await fetchDaemonJSON(settings.daemonURL, "/download", {
        method: "POST",
        token: settings.apiToken,
        body: payload,
        timeoutMs: 4500,
    });

    if (!res.reachable) {
        await recordEvent({
            url: item.url,
            filename: payload.filename,
            contentLength: item.contentLength || 0,
            referrer: item.referrer || "",
            capturedHeaders: hydratedItem.capturedHeaders,
            outcome: "failed",
            errorCode: "DAEMON_OFFLINE",
            error: `Daemon unreachable at ${settings.daemonURL}`,
        });
        void probeIntegrationHealth({ tryAutoPair: false });
        return {
            ok: false,
            status: 0,
            offline: true,
            errorCode: "DAEMON_OFFLINE",
            error: `Could not reach Hydra daemon at ${settings.daemonURL} (${res.error || "connection refused"}).`,
        };
    }

    if (!res.ok || !res.data?.job_id) {
        const errCode = res.data?.code || (res.status === 401 ? "INVALID_TOKEN" : "DAEMON_ERROR");
        const errMsg = res.data?.message || res.data?.error || `Daemon returned HTTP ${res.status}`;
        await recordEvent({
            url: item.url,
            filename: payload.filename,
            contentLength: item.contentLength || 0,
            referrer: item.referrer || "",
            capturedHeaders: hydratedItem.capturedHeaders,
            outcome: "failed",
            errorCode: errCode,
            error: errMsg,
        });
        void probeIntegrationHealth({ tryAutoPair: false });
        return {
            ok: false,
            status: res.status,
            offline: false,
            errorCode: errCode,
            error: errMsg,
        };
    }

    // Success: update recent dirs + event feed + notification + badge
    if (options.saveMode === "folder" && options.destinationDir) {
        await recordRecentDirectory(options.destinationDir);
    }

    const jobStatus = String(res.data.status || "QUEUED").toUpperCase();
    const outcome =
        jobStatus === "PAUSED" ? "paused" : jobStatus === "SCHEDULED" ? "scheduled" : "queued";

    await recordEvent({
        url: item.url,
        filename: payload.filename,
        savePath: payload.save_path,
        contentLength: item.contentLength || 0,
        outcome,
        jobId: res.data.job_id,
    });

    const labelAction =
        outcome === "paused" ? "Added (Paused)" : outcome === "scheduled" ? "Scheduled" : "Queued";
    await showDesktopNotification(`Hydra — ${labelAction}`, `${payload.filename}`);
    void probeIntegrationHealth({ tryAutoPair: false });

    return {
        ok: true,
        status: res.status,
        jobId: res.data.job_id,
        jobStatus,
    };
}

async function handleInterceptedDownload({
    url,
    filename,
    referrer = "",
    contentLength = 0,
    contentType = "",
    capturedHeaders = {},
}) {
    await ensureSessionLoaded();

    const settings = await loadSettings();
    const resolvedName = sanitizeFilename(filename || filenameFromURL(url));
    const pendingId = `dl_${Date.now()}_${sessionCache.counter++}`;

    const pendingItem = {
        id: pendingId,
        url,
        filename: resolvedName,
        referrer,
        contentLength: Number(contentLength || 0),
        contentType,
        capturedHeaders,
        createdAt: Date.now(),
    };

    // Fast path when user disabled confirmation dialog in Settings
    if (settings.confirmBeforeDownload === false) {
        const daemonSettingsRes = settings.apiToken
            ? await fetchDaemonJSON(settings.daemonURL, "/api/settings", { token: settings.apiToken, timeoutMs: 1200 })
            : null;
        const daemonSettings = daemonSettingsRes?.ok ? daemonSettingsRes.data : null;
        const autoDest = resolveCategoryDestination(resolvedName, daemonSettings, "~/Downloads");
        const directRes = await dispatchItemToHydra(pendingItem, {
            filename: resolvedName,
            saveMode: daemonSettings ? "folder" : "default",
            destinationDir: autoDest.path,
            includeHeaders: settings.includeHeadersByDefault !== false,
            startMode: settings.defaultStartMode || "now",
        });
        if (directRes.ok) return;
        // Recovery fallback: open confirmation/recovery dialog instead of dropping download!
        pendingItem.initialDispatchError = directRes;
    }

    sessionCache.pending[pendingId] = pendingItem;
    await saveSessionState();
    await openConfirmationWindow(pendingId);
}

async function releaseDownloadToBrowser(item) {
    await addBypassUrl(item.url);
    await recordEvent({
        url: item.url,
        filename: item.filename || filenameFromURL(item.url),
        contentLength: item.contentLength || 0,
        outcome: "browser",
    });

    const safeName = sanitizeFilename(item.filename || filenameFromURL(item.url));
    try {
        await chrome.downloads.download({
            url: item.url,
            filename: safeName,
        });
    } catch {
        // Fallback without filename hint if browser forbids suggested name
        await chrome.downloads.download({ url: item.url });
    }
}

// ─── WebRequest & Downloads Listeners ────────────────────────────────────────

chrome.webRequest.onBeforeSendHeaders.addListener(
    (details) => {
        if (isHydraEndpoint(details.url)) return {};
        const headers = {};
        for (const h of details.requestHeaders || []) {
            const name = h.name.toLowerCase();
            if (["cookie", "referer", "user-agent", "authorization", "accept", "origin"].includes(name)) {
                headers[h.name] = h.value || "";
            }
        }
        requestHeadersByID.set(details.requestId, headers);
        return {};
    },
    { urls: ["<all_urls>"], types: ["main_frame", "sub_frame", "other", "xmlhttprequest"] },
    ["requestHeaders"]
);

function cacheResponseMetadata(details, decision, capturedHeaders) {
    responseMetaCache.set(details.url, {
        filename: decision.filename || "",
        contentLength: decision.contentLength || 0,
        contentType: decision.contentType || "",
        interceptReason: decision.reason || "",
        headers: capturedHeaders,
        timestamp: Date.now(),
    });
}

async function processHeadersReceived(details, allowBlockingCancel) {
    if (isHydraEndpoint(details.url)) return {};

    const capturedHeaders = requestHeadersByID.get(details.requestId) || {};
    requestHeadersByID.delete(details.requestId);

    const settings = await loadSettings();
    const decision = evaluateResponseForInterception({
        url: details.url,
        responseHeaders: details.responseHeaders || [],
        settings,
    });

    cacheResponseMetadata(details, decision, capturedHeaders);

    if (!allowBlockingCancel || !decision.intercept) {
        return {};
    }

    if (await consumeBypassUrl(details.url, "webRequest")) {
        return {};
    }

    if (isDuplicate(details.url)) {
        return { cancel: true };
    }

    // Persist pending item cleanly and launch confirmation UI without blocking network abort
    void handleInterceptedDownload({
        url: details.url,
        filename: decision.filename,
        referrer: details.initiator || details.originUrl || "",
        contentLength: decision.contentLength,
        contentType: decision.contentType,
        capturedHeaders,
    }).catch((err) => {
        console.error("[Hydra] Failed to handle intercepted download:", err);
        void recordEvent({
            url: details.url,
            filename: decision.filename || filenameFromURL(details.url),
            contentLength: decision.contentLength || 0,
            referrer: details.initiator || details.originUrl || "",
            capturedHeaders,
            outcome: "failed",
            errorCode: "INTERCEPT_UI_ERROR",
            error: err?.message || String(err),
        });
    });

    return { cancel: true };
}

try {
    // Firefox MV3 supports webRequestBlocking cleanly; throws on Chrome MV3.
    chrome.webRequest.onHeadersReceived.addListener(
        (details) => processHeadersReceived(details, true),
        { urls: ["<all_urls>"], types: ["main_frame", "sub_frame", "other"] },
        ["blocking", "responseHeaders"]
    );
} catch {
    // Non-blocking fallback for Chrome MV3 (metadata cached for downloads.onCreated)
    chrome.webRequest.onHeadersReceived.addListener(
        (details) => {
            void processHeadersReceived(details, false);
        },
        { urls: ["<all_urls>"], types: ["main_frame", "sub_frame", "other"] },
        ["responseHeaders"]
    );
}

if (chrome.downloads?.onCreated) {
    chrome.downloads.onCreated.addListener(async (downloadItem) => {
        const targetURL = downloadItem.finalUrl || downloadItem.url;
        if (!targetURL || isHydraEndpoint(targetURL)) return;

        const bypassed =
            (await consumeBypassUrl(targetURL, "downloads")) ||
            (downloadItem.url &&
                downloadItem.url !== targetURL &&
                (await consumeBypassUrl(downloadItem.url, "downloads")));
        if (bypassed) {
            return;
        }

        const settings = await loadSettings();
        const cachedMeta = responseMetaCache.get(targetURL) || responseMetaCache.get(downloadItem.url) || null;
        const decision = evaluateDownloadItemForInterception({
            item: downloadItem,
            cachedMeta,
            settings,
        });

        if (!decision.intercept) return;
        if (isDuplicate(targetURL)) {
            try {
                await chrome.downloads.cancel(downloadItem.id);
                await chrome.downloads.erase({ id: downloadItem.id });
            } catch {
                /* ignore */
            }
            return;
        }

        try {
            await chrome.downloads.cancel(downloadItem.id);
            await chrome.downloads.erase({ id: downloadItem.id });
        } catch {
            /* may already have finished */
        }

        await handleInterceptedDownload({
            url: targetURL,
            filename: decision.filename,
            referrer: downloadItem.referrer || "",
            contentLength: decision.contentLength,
            contentType: decision.contentType,
            capturedHeaders: cachedMeta?.headers || {},
        });
    });
}

// ─── Message Router (from popup.js & settings.js) ────────────────────────────

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    const handleAsync = async () => {
        await ensureSessionLoaded();

        switch (msg.type) {
            case "GET_PENDING": {
                const item = sessionCache.pending[msg.id];
                return item ? { ok: true, item } : { ok: false, error: "Pending download expired or already handled." };
            }

            case "PROBE_DIALOG_CONTEXT": {
                const health = await probeIntegrationHealth({ tryAutoPair: true });
                let daemonSettings = null;
                if (health.daemonReachable && health.authenticated) {
                    const sRes = await fetchDaemonJSON(health.settings.daemonURL, "/api/settings", {
                        token: health.settings.apiToken,
                        timeoutMs: 1800,
                    });
                    if (sRes.ok) daemonSettings = sRes.data;
                }
                return { ok: true, health, daemonSettings };
            }

            case "CHECK_DISK_SPACE": {
                const settings = await loadSettings();
                const res = await fetchDaemonJSON(settings.daemonURL, "/api/disk-space", {
                    method: "POST",
                    token: settings.apiToken,
                    body: { path: msg.path || "", required_bytes: Number(msg.requiredBytes || 0) },
                    timeoutMs: 1500,
                });
                return res.ok ? { ok: true, info: res.data } : { ok: false, error: res.data?.error || res.error };
            }

            case "BROWSE_FOLDER": {
                const settings = await loadSettings();
                const res = await fetchDaemonJSON(settings.daemonURL, "/api/browse-directory", {
                    method: "POST",
                    token: settings.apiToken,
                    body: { current_path: msg.currentPath || "" },
                    timeoutMs: 45_000,
                });
                if (!res.reachable) {
                    return { ok: false, error: res.error || "Daemon unreachable" };
                }
                return { ok: Boolean(res.ok && res.data?.success), path: res.data?.path || "", error: res.data?.error || "" };
            }

            case "LIST_DIRECTORY": {
                const settings = await loadSettings();
                const res = await fetchDaemonJSON(settings.daemonURL, "/api/list-directory", {
                    method: "POST",
                    token: settings.apiToken,
                    body: { path: msg.path || "" },
                    timeoutMs: 2500,
                });
                if (!res.reachable || !res.ok) {
                    return { ok: false, error: res.data?.error || res.error || "Cannot list directory" };
                }
                return { ok: Boolean(res.data?.success), ...res.data };
            }

            case "CONFIRM_DOWNLOAD": {
                const item = sessionCache.pending[msg.id];
                if (!item) {
                    return { ok: false, errorCode: "NOT_FOUND", error: "Pending download was already handled." };
                }
                const result = await dispatchItemToHydra(item, msg.options || {});
                if (result.ok) {
                    delete sessionCache.pending[msg.id];
                    await saveSessionState();
                }
                return result;
            }

            case "CANCEL_DOWNLOAD": {
                const item = sessionCache.pending[msg.id];
                if (item) {
                    delete sessionCache.pending[msg.id];
                    await saveSessionState();
                    await releaseDownloadToBrowser(item);
                }
                return { ok: true };
            }

            case "DISCARD_DOWNLOAD": {
                if (msg.id && sessionCache.pending[msg.id]) {
                    delete sessionCache.pending[msg.id];
                    await saveSessionState();
                }
                return { ok: true };
            }

            case "SAVE_INLINE_TOKEN": {
                const cleaned = String(msg.token || "").trim();
                await saveSettings({ apiToken: cleaned });
                const health = await probeIntegrationHealth({ tryAutoPair: false });
                return { ok: true, health };
            }

            case "PAIR_VIA_NATIVE_HOST": {
                const nativeInfo = await sendNative({ action: "get_integration_info" });
                if (!nativeInfo.available) {
                    return { ok: false, error: "Native desktop bridge is not installed yet." };
                }
                if (!nativeInfo.token) {
                    return { ok: false, error: "Could not read ~/.config/hydra/api.token." };
                }
                await saveSettings({ apiToken: nativeInfo.token });
                await chrome.storage.local.set({ hydraNativeAutoPaired: true });
                const health = await probeIntegrationHealth({ tryAutoPair: false });
                return { ok: true, health };
            }

            case "START_DAEMON_AND_RETRY": {
                const res = await sendNative({ action: "ensure_daemon" }, 6000);
                if (res.token) {
                    await saveSettings({ apiToken: res.token });
                }
                const health = await probeIntegrationHealth({ tryAutoPair: false });
                return { ok: Boolean(res.ok || health.daemonReachable), native: res, health };
            }

            case "OPEN_HYDRA_GUI": {
                const nativeRes = await sendNative({
                    action: "open_gui",
                    job_id: msg.jobId || "",
                    url: msg.url || "",
                });
                return { ok: Boolean(nativeRes.ok), error: nativeRes.error || "" };
            }

            case "OPEN_DOWNLOADS_FOLDER": {
                const nativeRes = await sendNative({ action: "open_downloads" });
                return { ok: Boolean(nativeRes.ok), error: nativeRes.error || "" };
            }

            case "GET_INTEGRATION_STATUS": {
                const health = await probeIntegrationHealth({ tryAutoPair: true });
                let jobs = null;
                if (health.daemonReachable && health.authenticated) {
                    const jobsRes = await fetchDaemonJSON(health.settings.daemonURL, "/api/jobs", {
                        token: health.settings.apiToken,
                        timeoutMs: 1800,
                    });
                    if (jobsRes.ok && Array.isArray(jobsRes.data)) {
                        jobs = jobsRes.data;
                    }
                }
                return {
                    ok: true,
                    health,
                    jobs,
                    pendingCount: Object.keys(sessionCache.pending).length,
                };
            }

            case "SET_CAPTURE_PREFS": {
                const patch = {};
                if (typeof msg.autoCapture === "boolean") patch.autoCapture = msg.autoCapture;
                if (typeof msg.confirmBeforeDownload === "boolean") {
                    patch.confirmBeforeDownload = msg.confirmBeforeDownload;
                }
                await saveSettings(patch);
                const health = await probeIntegrationHealth({ tryAutoPair: false });
                return { ok: true, health };
            }

            case "RETRY_EVENT": {
                const { recentEvents } = await loadRuntimeMeta();
                const target = recentEvents.find((ev) => ev.id === msg.eventId);
                if (!target) return { ok: false, error: "Event not found." };
                await handleInterceptedDownload({
                    url: target.url,
                    filename: target.filename,
                    referrer: target.referrer || "",
                    contentLength: target.contentLength || 0,
                    capturedHeaders: target.capturedHeaders || {},
                });
                return { ok: true };
            }

            case "FALLBACK_EVENT_TO_BROWSER": {
                const { recentEvents } = await loadRuntimeMeta();
                const target = recentEvents.find((ev) => ev.id === msg.eventId);
                if (!target) return { ok: false, error: "Event not found." };
                await releaseDownloadToBrowser(target);
                return { ok: true };
            }

            case "CLEAR_RECENT_EVENTS": {
                await chrome.storage.local.set({ hydraRecentEvents: [], hydraLastError: "" });
                const health = await probeIntegrationHealth({ tryAutoPair: false });
                return { ok: true, health };
            }

            case "GET_SETTINGS": {
                const settings = await loadSettings();
                return { ok: true, settings };
            }

            case "SAVE_SETTINGS": {
                const saved = await saveSettings(msg.settings || {});
                const health = await probeIntegrationHealth({ tryAutoPair: false });
                return { ok: true, settings: saved, health };
            }

            case "TEST_CONNECTION": {
                const health = await probeIntegrationHealth({ tryAutoPair: Boolean(msg.tryAutoPair) });
                return { ok: true, health };
            }

            default:
                return { ok: false, error: `Unknown message type: ${msg.type}` };
        }
    };

    handleAsync()
        .then((resp) => sendResponse(resp))
        .catch((err) => sendResponse({ ok: false, error: err?.message || String(err) }));
    return true;
});

// Probe on browser startup / extension install so toolbar badge reflects live status immediately.
if (chrome.runtime?.onStartup) {
    chrome.runtime.onStartup.addListener(() => {
        void probeIntegrationHealth({ tryAutoPair: true });
    });
}
if (chrome.runtime?.onInstalled) {
    chrome.runtime.onInstalled.addListener(() => {
        void probeIntegrationHealth({ tryAutoPair: true });
    });
}
void probeIntegrationHealth({ tryAutoPair: true });
