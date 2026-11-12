/**
 * Hydra Download Manager — Shared Extension Logic (ES Module)
 *
 * Pure helpers shared across background.js, popup.js, settings.js,
 * and executable directly under Node (`node --test`).
 */

export const DEFAULT_DAEMON_URL = "http://127.0.0.1:9000";
export const NATIVE_HOST_NAME = "org.hydra.downloader";
export const MAX_RECENT_EVENTS = 15;
export const MAX_RECENT_DIRS = 6;
export const MAX_FILENAME_BYTES = 255;

export const DEFAULT_SNATCH_EXTENSIONS = [
    // Archives & Disk Images
    "zip", "tar", "gz", "bz2", "xz", "7z", "rar", "iso", "img", "bin",
    // Video
    "mp4", "mkv", "avi", "mov", "flv", "webm", "wmv", "m4v", "mpg", "mpeg",
    // Audio
    "mp3", "flac", "wav", "aac", "ogg", "m4a", "opus", "wma",
    // Software & Packages
    "exe", "msi", "dmg", "deb", "rpm", "appimage", "apk", "xapk", "snap",
    // Documents
    "pdf", "epub", "djvu",
];

export const BINARY_MIME_PATTERNS = [
    "application/octet-stream",
    "application/zip", "application/x-zip-compressed",
    "application/x-rar-compressed", "application/x-rar",
    "application/x-7z-compressed",
    "application/x-tar", "application/gzip", "application/x-bzip2",
    "application/x-iso9660-image",
    "application/x-msdownload", "application/x-msdos-program",
    "application/vnd.android.package-archive",
    "application/pdf",
    "application/x-debian-package", "application/x-rpm",
    "video/", "audio/",
];

export const WEBPAGE_MIME_PATTERNS = [
    "text/html", "text/css", "text/javascript", "application/javascript",
    "application/json", "+json", "application/xml", "text/xml", "+xml",
    "text/plain", "image/svg+xml", "application/xhtml+xml",
    "application/x-www-form-urlencoded", "application/wasm",
];

export const DEFAULT_SETTINGS = Object.freeze({
    daemonURL: DEFAULT_DAEMON_URL,
    apiToken: "",
    autoCapture: true,
    confirmBeforeDownload: true,
    includeHeadersByDefault: true,
    minInterceptKB: 512,
    notificationsEnabled: true,
    defaultStartMode: "now", // "now" | "paused"
    customExtensions: [],
    excludedHosts: ["127.0.0.1", "localhost"],
    recentDirectories: [],
    lastDestinationDir: "",
});

export function normalizeDaemonURL(rawURL) {
    const cleaned = String(rawURL || "").trim().replace(/\/+$/, "");
    if (!cleaned) return DEFAULT_DAEMON_URL;
    return cleaned;
}

export function isHydraEndpoint(url, daemonURL = DEFAULT_DAEMON_URL) {
    try {
        const parsed = new URL(url);
        const daemon = new URL(normalizeDaemonURL(daemonURL));
        const sameLocalHost =
            (parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost" || parsed.hostname === daemon.hostname) &&
            (parsed.port || (parsed.protocol === "https:" ? "443" : "80")) ===
                (daemon.port || (daemon.protocol === "https:" ? "443" : "80"));
        return sameLocalHost;
    } catch {
        return false;
    }
}

export function isInterceptableURL(url) {
    try {
        const parsed = new URL(url);
        return parsed.protocol === "http:" || parsed.protocol === "https:";
    } catch {
        return false;
    }
}

export function isHostExcluded(url, excludedHosts = []) {
    if (!Array.isArray(excludedHosts) || excludedHosts.length === 0) return false;
    try {
        const host = new URL(url).hostname.toLowerCase();
        return excludedHosts.some((rawEntry) => {
            const entry = String(rawEntry || "").trim().toLowerCase();
            if (!entry) return false;
            return host === entry || host.endsWith(`.${entry}`);
        });
    } catch {
        return false;
    }
}

export function getEffectiveExtensions(settings = {}) {
    const exts = new Set(DEFAULT_SNATCH_EXTENSIONS);
    const custom = Array.isArray(settings.customExtensions) ? settings.customExtensions : [];
    for (const raw of custom) {
        const cleaned = String(raw || "").trim().toLowerCase().replace(/^\.+/, "");
        if (cleaned) exts.add(cleaned);
    }
    return exts;
}

export function extractExtension(filenameOrPath) {
    const base = String(filenameOrPath || "").split("/").pop()?.split("?")[0] || "";
    const idx = base.lastIndexOf(".");
    if (idx <= 0 || idx === base.length - 1) return "";
    return base.slice(idx + 1).toLowerCase();
}

export function sanitizeFilename(rawName) {
    let cleaned = String(rawName || "")
        .replace(/[/\\\x00-\x1f\x7f]/g, "_")
        .trim();
    if (!cleaned || cleaned === "." || cleaned === "..") {
        return "downloaded_file.bin";
    }
    return cleaned;
}

export function validateFilename(rawName) {
    const name = String(rawName || "").trim();
    if (!name) return "Filename cannot be empty.";
    if (name === "." || name === "..") return `Filename cannot be "${name}".`;
    if (name.includes("/") || name.includes("\\")) return "Filename cannot contain path separators (/ or \\).";
    if (/[\x00-\x1f\x7f]/.test(name)) return "Filename contains invalid control characters.";
    const byteLen = new TextEncoder().encode(name).length;
    if (byteLen > MAX_FILENAME_BYTES) return `Filename exceeds ${MAX_FILENAME_BYTES} bytes.`;
    return null;
}

export function parseContentDispositionFilename(header) {
    if (!header) return "";
    const text = String(header);

    // Prefer RFC 5987 extended filename*: e.g., filename*=UTF-8''my%20file.zip
    const extMatch = text.match(/filename\*\s*=\s*(?:[A-Za-z0-9_-]+''|")?([^";\r\n]+)"?/i);
    if (extMatch?.[1]) {
        const raw = extMatch[1].replace(/^['"]|['"]$/g, "").trim();
        try {
            return sanitizeFilename(decodeURIComponent(raw));
        } catch {
            return sanitizeFilename(raw);
        }
    }

    const quotedMatch = text.match(/filename\s*=\s*"([^"\r\n]+)"/i);
    if (quotedMatch?.[1]) {
        return sanitizeFilename(quotedMatch[1]);
    }

    const plainMatch = text.match(/filename\s*=\s*([^;\r\n]+)/i);
    if (plainMatch?.[1]) {
        const raw = plainMatch[1].replace(/^['"]|['"]$/g, "").trim();
        try {
            return sanitizeFilename(decodeURIComponent(raw));
        } catch {
            return sanitizeFilename(raw);
        }
    }

    return "";
}

export function filenameFromURL(url) {
    try {
        const pathname = new URL(url).pathname;
        const segment = pathname.split("/").filter(Boolean).pop() || "";
        if (!segment) return "downloaded_file.bin";
        try {
            return sanitizeFilename(decodeURIComponent(segment));
        } catch {
            return sanitizeFilename(segment);
        }
    } catch {
        return "downloaded_file.bin";
    }
}

export function getResponseHeader(responseHeaders, name) {
    if (!Array.isArray(responseHeaders)) return "";
    const lower = name.toLowerCase();
    return responseHeaders.find((h) => h?.name?.toLowerCase() === lower)?.value || "";
}

export function evaluateResponseForInterception({
    url,
    statusCode = 0,
    method = "",
    resourceType = "",
    responseHeaders = [],
    settings = {},
}) {
    const merged = { ...DEFAULT_SETTINGS, ...settings };
    if (merged.autoCapture === false) {
        return { intercept: false, reason: "autoCapture_disabled" };
    }
    if (!isInterceptableURL(url)) {
        return { intercept: false, reason: "unsupported_scheme" };
    }
    if (isHydraEndpoint(url, merged.daemonURL)) {
        return { intercept: false, reason: "hydra_local_endpoint" };
    }
    if (isHostExcluded(url, merged.excludedHosts)) {
        return { intercept: false, reason: "excluded_host" };
    }

    // Never hijack redirects (3xx login/SSO hops), 204/304 empty responses, or 4xx/5xx error pages.
    if (Number.isFinite(statusCode) && statusCode > 0) {
        if (statusCode < 200 || statusCode >= 300 || statusCode === 204 || statusCode === 205) {
            return { intercept: false, reason: "non_download_status" };
        }
    }

    const minInterceptBytes = Math.max(0, Number(merged.minInterceptKB ?? 512)) * 1024;
    const extensions = getEffectiveExtensions(merged);

    const contentDisposition = getResponseHeader(responseHeaders, "content-disposition");
    const contentType = getResponseHeader(responseHeaders, "content-type").toLowerCase();
    const rawLength = parseInt(getResponseHeader(responseHeaders, "content-length") || "0", 10);
    const contentLength = Number.isFinite(rawLength) && rawLength > 0 ? rawLength : 0;

    // 1. Explicit attachment Content-Disposition always wins (ignore "inline" dispositions).
    if (contentDisposition && /\battachment\b/i.test(contentDisposition)) {
        const cdName = parseContentDispositionFilename(contentDisposition);
        return {
            intercept: true,
            reason: "content_disposition",
            filename: cdName || filenameFromURL(url),
            contentLength,
            contentType,
        };
    }

    // Never hijack webpages/API responses unless explicit attachment was requested above.
    if (contentType && WEBPAGE_MIME_PATTERNS.some((pat) => contentType.includes(pat))) {
        return { intercept: false, reason: "webpage_mime" };
    }

    // Form submissions (e.g., AWS/OAuth POST sign-in) and hidden iframes without explicit
    // attachment headers should navigate normally; genuine downloads still trigger downloads.onCreated.
    const normMethod = String(method || "").trim().toUpperCase();
    if (normMethod && normMethod !== "GET") {
        return { intercept: false, reason: "non_get_heuristic" };
    }
    if (resourceType === "sub_frame") {
        return { intercept: false, reason: "subframe_heuristic" };
    }

    const urlName = filenameFromURL(url);
    const ext = extractExtension(urlName);

    // 2. Binary MIME type + supported extension.
    if (contentType && BINARY_MIME_PATTERNS.some((pat) => contentType.includes(pat))) {
        if (ext && extensions.has(ext)) {
            return {
                intercept: true,
                reason: "binary_mime_and_extension",
                filename: urlName,
                contentLength,
                contentType,
            };
        }
    }

    // 3. Extensionless application/octet-stream: only intercept upfront when Content-Length
    // meets minInterceptBytes; unknown-size streams fall through to browser downloads.onCreated
    // so auth/redirect endpoints without extensions are never misidentified as downloads.
    if (contentType.includes("application/octet-stream")) {
        if (contentLength >= minInterceptBytes) {
            return {
                intercept: true,
                reason: "octet_stream",
                filename: urlName,
                contentLength,
                contentType,
            };
        }
    }

    // 4. URL extension fallback when server omits or mislabels Content-Type.
    if (ext && extensions.has(ext) && contentLength >= minInterceptBytes) {
        return {
            intercept: true,
            reason: "url_extension",
            filename: urlName,
            contentLength,
            contentType,
        };
    }

    return { intercept: false, reason: "no_match", contentLength, contentType };
}

export function evaluateDownloadItemForInterception({ item = {}, cachedMeta = null, settings = {} }) {
    const merged = { ...DEFAULT_SETTINGS, ...settings };
    const url = item.finalUrl || item.url || "";

    if (merged.autoCapture === false) {
        return { intercept: false, reason: "autoCapture_disabled" };
    }
    if (!isInterceptableURL(url)) {
        return { intercept: false, reason: "unsupported_scheme" };
    }
    if (isHydraEndpoint(url, merged.daemonURL)) {
        return { intercept: false, reason: "hydra_local_endpoint" };
    }
    if (isHostExcluded(url, merged.excludedHosts)) {
        return { intercept: false, reason: "excluded_host" };
    }

    const baseName = item.filename ? sanitizeFilename(String(item.filename).split(/[/\\]/).pop()) : "";
    const resolvedName = baseName || cachedMeta?.filename || filenameFromURL(url);
    const ext = extractExtension(resolvedName) || extractExtension(filenameFromURL(url));
    const extensions = getEffectiveExtensions(merged);
    const contentLength = item.totalBytes > 0 ? item.totalBytes : (cachedMeta?.contentLength || 0);

    if (cachedMeta?.interceptReason === "content_disposition" || cachedMeta?.interceptReason === "octet_stream") {
        return {
            intercept: true,
            reason: cachedMeta.interceptReason,
            filename: resolvedName,
            contentLength,
            contentType: cachedMeta.contentType || item.mime || "",
        };
    }

    if (ext && extensions.has(ext)) {
        return {
            intercept: true,
            reason: "download_item_extension",
            filename: resolvedName,
            contentLength,
            contentType: item.mime || cachedMeta?.contentType || "",
        };
    }

    if (item.mime && BINARY_MIME_PATTERNS.some((pat) => item.mime.toLowerCase().includes(pat))) {
        return {
            intercept: true,
            reason: "download_item_mime",
            filename: resolvedName,
            contentLength,
            contentType: item.mime,
        };
    }

    return { intercept: false, reason: "no_match" };
}

function filenameMatchesExt(lowerBase, ext) {
    if (!lowerBase || !ext) return false;
    const suffix = `.${ext}`;
    return lowerBase.length > suffix.length && lowerBase.endsWith(suffix);
}

export function resolveCategoryDestination(filename, daemonSettings = null, fallbackDir = "~/Downloads") {
    const defaultDir = String(daemonSettings?.default_save_path || "").trim() || fallbackDir;
    if (daemonSettings?.disable_auto_categorize) {
        return { category: "Default", path: defaultDir };
    }

    const baseName = String(filename || "").split("/").pop()?.split("?")[0]?.trim().toLowerCase() || "";
    const categories = daemonSettings?.categories;

    if (baseName && baseName !== "." && baseName !== ".." && categories && typeof categories === "object") {
        const matches = [];
        for (const catName of Object.keys(categories).sort()) {
            const rule = categories[catName];
            if (!rule || rule.disabled || !Array.isArray(rule.extensions) || !String(rule.path || "").trim()) {
                continue;
            }
            let bestExt = "";
            for (const rawExt of rule.extensions) {
                const norm = String(rawExt || "").trim().replace(/^\.+/, "").toLowerCase();
                if (filenameMatchesExt(baseName, norm) && norm.length > bestExt.length) {
                    bestExt = norm;
                }
            }
            if (bestExt) {
                matches.push({
                    category: catName,
                    path: String(rule.path).trim(),
                    priority: Number(rule.priority || 0),
                    extLen: bestExt.length,
                });
            }
        }
        if (matches.length > 0) {
            matches.sort((a, b) => {
                if (a.priority !== b.priority) return b.priority - a.priority;
                if (a.extLen !== b.extLen) return b.extLen - a.extLen;
                return a.category < b.category ? -1 : a.category > b.category ? 1 : 0;
            });
            return { category: matches[0].category, path: matches[0].path };
        }
    }
    return { category: "Default", path: defaultDir };
}

export function buildDestinationPresets(filename, daemonSettings = null, recentDirs = [], fallbackDir = "~/Downloads") {
    const presets = [];
    const seen = new Set();

    const addPreset = (label, path, kind) => {
        const clean = String(path || "").trim();
        if (!clean || seen.has(clean)) return;
        seen.add(clean);
        presets.push({ label, path: clean, kind });
    };

    const autoMatch = resolveCategoryDestination(filename, daemonSettings, fallbackDir);
    addPreset(`Auto (${autoMatch.category}: ${autoMatch.path})`, autoMatch.path, "auto");

    const defaultDir = String(daemonSettings?.default_save_path || "").trim() || fallbackDir;
    addPreset(`Default (${defaultDir})`, defaultDir, "default");

    const categories = daemonSettings?.categories;
    if (categories && typeof categories === "object") {
        for (const [name, rule] of Object.entries(categories)) {
            if (rule && !rule.disabled && rule.path) {
                addPreset(`${name} (${rule.path})`, rule.path, "category");
            }
        }
    }

    if (Array.isArray(recentDirs)) {
        for (const dir of recentDirs) {
            addPreset(`Recent (${dir})`, dir, "recent");
        }
    }

    return presets;
}

export function joinSavePath(dirPath, filename) {
    const dir = String(dirPath || "").trim();
    const name = String(filename || "").trim();
    if (!dir || dir === "DEFAULT") return "DEFAULT";
    if (dir === "PENDING") return "PENDING";
    if (!name) return dir;
    return dir.endsWith("/") ? `${dir}${name}` : `${dir}/${name}`;
}

export function validateDownloadOptions({
    filename = "",
    saveMode = "folder", // "folder" | "pending" | "default"
    destinationDir = "",
    speedLimitKbps = 0,
    checksumAlgo = "",
    expectedChecksum = "",
    startMode = "now", // "now" | "paused" | "schedule"
    scheduledAt = "",
} = {}) {
    const nameErr = validateFilename(filename);
    if (nameErr) return nameErr;

    if (saveMode === "folder" && !String(destinationDir || "").trim()) {
        return "Destination folder cannot be empty (or select 'Choose later in Hydra').";
    }

    const numericSpeed = Number(speedLimitKbps || 0);
    if (!Number.isFinite(numericSpeed) || numericSpeed < 0) {
        return "Speed limit must be 0 (unlimited) or a positive KB/s value.";
    }

    const cleanAlgo = String(checksumAlgo || "").trim().toLowerCase();
    const cleanHash = String(expectedChecksum || "").trim();
    if (cleanHash) {
        if (!cleanAlgo) {
            return "Select a checksum algorithm (SHA-256, MD5, or CRC32).";
        }
        if (!/^[0-9a-fA-F]+$/.test(cleanHash)) {
            return "Expected checksum must be a valid hexadecimal string.";
        }
    }

    if (startMode === "schedule") {
        const rawSched = String(scheduledAt || "").trim();
        if (!rawSched) {
            return "Select a date and time for a scheduled download.";
        }
        const parsed = new Date(rawSched);
        if (Number.isNaN(parsed.getTime())) {
            return "Scheduled start time is invalid.";
        }
        if (parsed.getTime() <= Date.now()) {
            return "Scheduled start time must be in the future.";
        }
    }

    return null;
}

export function buildDownloadPayload(pendingItem, options = {}) {
    const filename = String(options.filename || pendingItem.filename || filenameFromURL(pendingItem.url)).trim();
    const saveMode = options.saveMode || "folder";
    const destinationDir = String(options.destinationDir || "").trim();

    let savePath = "DEFAULT";
    if (saveMode === "pending") {
        savePath = "PENDING";
    } else if (saveMode === "folder" && destinationDir) {
        savePath = joinSavePath(destinationDir, filename);
    }

    const includeHeaders = options.includeHeaders !== false;
    const headers = includeHeaders ? { ...(pendingItem.capturedHeaders || {}) } : {};
    if (includeHeaders && pendingItem.referrer && !headers.Referer && !headers.referer) {
        headers.Referer = pendingItem.referrer;
    }

    const payload = {
        url: pendingItem.url,
        save_path: savePath,
        filename,
        headers,
    };

    const kbps = Number(options.speedLimitKbps || 0);
    if (Number.isFinite(kbps) && kbps > 0) {
        payload.max_speed_bytes = Math.round(kbps * 1024);
    }

    const algo = String(options.checksumAlgo || "").trim().toLowerCase();
    const hash = String(options.expectedChecksum || "").trim();
    if (algo && hash) {
        payload.checksum_algo = algo;
        payload.expected_checksum = hash;
    }

    const startMode = options.startMode || "now";
    if (startMode === "schedule" && options.scheduledAt) {
        const dt = new Date(options.scheduledAt);
        if (!Number.isNaN(dt.getTime())) {
            payload.scheduled_at = dt.toISOString();
        }
    } else if (startMode === "paused") {
        payload.start_paused = true;
    }

    return payload;
}

export function evaluateDiskSpace(diskInfo, expectedBytes = 0) {
    if (!diskInfo || typeof diskInfo.available_bytes !== "number") {
        return {
            level: "unknown",
            message: "Free disk space unavailable (connect daemon to verify)",
        };
    }

    const freeText = formatBytes(diskInfo.available_bytes);
    if (expectedBytes > 0 && diskInfo.enough_space === false) {
        return {
            level: "error",
            message: `Insufficient space: ${freeText} free, need ${formatBytes(expectedBytes)}`,
        };
    }

    return {
        level: "ok",
        message: `Free disk space: ${freeText}`,
    };
}

export function summarizeIntegrationHealth({
    daemonReachable = false,
    authenticated = false,
    tokenConfigured = false,
    nativeHostAvailable = false,
    autoCapture = true,
    lastError = "",
} = {}) {
    if (!autoCapture) {
        return {
            state: "paused",
            badgeText: "OFF",
            badgeColor: "#6c7086",
            headline: "Browser capture is paused",
            detail: "Downloads use the browser's built-in manager until you re-enable Hydra capture.",
            recoverySteps: ["Toggle Auto-Capture ON when you want Hydra to intercept downloads."],
        };
    }

    if (!daemonReachable) {
        return {
            state: "offline",
            badgeText: "ERR",
            badgeColor: "#e05c5c",
            headline: "Hydra daemon is offline",
            detail: lastError || "Could not connect to the background daemon.",
            recoverySteps: nativeHostAvailable
                ? [
                      "Click 'Start Hydra Daemon' to launch the background service automatically.",
                      "Or fall back to browser download without losing your link.",
                  ]
                : [
                      "Start Hydra from your desktop launcher or run: systemctl --user start hydra.service",
                      "Verify the Daemon URL in Settings, then click Retry.",
                  ],
        };
    }

    if (!tokenConfigured || !authenticated) {
        return {
            state: "needs_token",
            badgeText: "KEY",
            badgeColor: "#f9e2af",
            headline: tokenConfigured ? "API token rejected" : "API token not paired",
            detail: tokenConfigured
                ? "Daemon rejected the saved X-Hydra-Token."
                : "Extension needs your local Hydra API token to queue downloads.",
            recoverySteps: nativeHostAvailable
                ? ["Click 'Auto-Pair Token' to import ~/.config/hydra/api.token automatically."]
                : [
                      "Run: cat ~/.config/hydra/api.token",
                      "Paste the token below (or in Extension Settings) and click Save & Retry.",
                  ],
        };
    }

    if (lastError) {
        return {
            state: "warning",
            badgeText: "!",
            badgeColor: "#fab387",
            headline: "Connected (last dispatch had a warning)",
            detail: lastError,
            recoverySteps: ["Review Recent Activity below to retry or adjust the failed download."],
        };
    }

    return {
        state: "connected",
        badgeText: "ON",
        badgeColor: "#a6e3a1",
        headline: "Connected & capturing downloads",
        detail: nativeHostAvailable
            ? "Daemon active • Authenticated • Desktop bridge connected"
            : "Daemon active • Authenticated (HTTP mode)",
        recoverySteps: [],
    };
}

export function formatBytes(bytes) {
    if (!bytes || bytes <= 0) return "Unknown";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    let val = Number(bytes);
    while (val >= 1024 && i < units.length - 1) {
        val /= 1024;
        i++;
    }
    return `${val.toFixed(i === 0 ? 0 : 2)} ${units[i]}`;
}

export function truncateURL(url, max = 64) {
    if (!url || url.length <= max) return url || "";
    try {
        const { origin, pathname } = new URL(url);
        const combined = `${origin}${pathname}`;
        return combined.length <= max ? combined : `${combined.slice(0, max - 1)}…`;
    } catch {
        return `${url.slice(0, max - 1)}…`;
    }
}

export function formatRelativeTime(ts) {
    if (!ts) return "";
    const diffSec = Math.max(0, Math.floor((Date.now() - Number(ts)) / 1000));
    if (diffSec < 10) return "just now";
    if (diffSec < 60) return `${diffSec}s ago`;
    const diffMin = Math.floor(diffSec / 60);
    if (diffMin < 60) return `${diffMin}m ago`;
    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return `${diffHr}h ago`;
    return `${Math.floor(diffHr / 24)}d ago`;
}

export function createBypassEntry(ttlMs = 30_000, now = Date.now()) {
    return {
        expiresAt: now + ttlMs,
        webRequestDone: false,
        downloadsDone: false,
    };
}

export function pruneBypassMap(bypassMap, now = Date.now()) {
    if (!bypassMap || typeof bypassMap !== "object") return;
    for (const [url, entry] of Object.entries(bypassMap)) {
        const exp = typeof entry === "number" ? entry : entry?.expiresAt;
        if (!exp || exp <= now) delete bypassMap[url];
    }
}

export function consumeBypassStage(bypassMap, url, stage, now = Date.now()) {
    if (!bypassMap || !url) return false;
    const raw = bypassMap[url];
    if (!raw) return false;

    const entry = typeof raw === "number" ? { expiresAt: raw, webRequestDone: false, downloadsDone: false } : raw;
    if (!entry.expiresAt || entry.expiresAt <= now) {
        delete bypassMap[url];
        return false;
    }

    if (stage === "webRequest") {
        if (entry.webRequestDone) return false;
        entry.webRequestDone = true;
    } else if (stage === "downloads") {
        if (entry.downloadsDone) return false;
        entry.downloadsDone = true;
    } else {
        delete bypassMap[url];
        return true;
    }

    if (entry.webRequestDone && entry.downloadsDone) {
        delete bypassMap[url];
    } else {
        entry.expiresAt = Math.min(entry.expiresAt, now + 4_000);
        bypassMap[url] = entry;
    }
    return true;
}

