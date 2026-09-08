const HYDRA_API_URL = "http://127.0.0.1:9000/download";

console.log("[Hydra] background interceptor started");

function isHydraLocalEndpoint(url) {
    try {
        const parsed = new URL(url);
        return (parsed.hostname === "127.0.0.1" || parsed.hostname === "localhost") && parsed.port === "9000";
    } catch (e) {
        return false;
    }
}

function filenameFromURL(url) {
    try {
        const pathname = new URL(url).pathname;
        return decodeURIComponent(pathname.split('/').pop() || "downloaded_file.bin");
    } catch (e) {
        return "downloaded_file.bin";
    }
}

function hasKnownDownloadExtension(url) {
    const filename = filenameFromURL(url).toLowerCase();
    const extension = filename.includes('.') ? filename.split('.').pop() : "";
    return SnatchExtensions.has(extension);
}

// ──────────────────────────────────────────────────────────────
// File extensions Hydra should intercept (games, music, movies, software, archives)
// ──────────────────────────────────────────────────────────────
const SnatchExtensions = new Set([
    // Archives & Disk Images
    "zip", "tar", "gz", "bz2", "xz", "7z", "rar", "iso", "img", "bin",
    // Video
    "mp4", "mkv", "avi", "mov", "flv", "webm", "wmv", "m4v", "mpg", "mpeg",
    // Audio
    "mp3", "flac", "wav", "aac", "ogg", "m4a", "opus", "wma",
    // Software & Games
    "exe", "msi", "dmg", "deb", "rpm", "appimage", "apk", "xapk", "snap",
    // Documents
    "pdf", "epub", "djvu"
]);

// MIME types that indicate a binary file download (not a webpage)
const BinaryMimePatterns = [
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
    "application/x-shockwave-flash"
];

// MIME types that are NEVER downloads — always skip these
const WebpageMimePatterns = [
    "text/html", "text/css", "text/javascript", "application/javascript",
    "application/json", "application/xml", "text/xml", "text/plain",
    "image/svg+xml", "application/xhtml+xml"
];

// Minimum file size to intercept (skip tiny files like favicons)
const MIN_INTERCEPT_BYTES = 512 * 1024; // 512 KB

// Track recently dispatched URLs to prevent duplicate jobs
const recentDispatches = new Map();
const DEDUP_WINDOW_MS = 5000;
const requestHeadersByID = new Map();

function captureRequestHeaders(details) {
    const headers = {};
    for (const header of details.requestHeaders || []) {
        const name = header.name.toLowerCase();
        if (["cookie", "referer", "user-agent", "authorization", "accept", "origin"].includes(name)) {
            headers[header.name] = header.value || "";
        }
    }
    requestHeadersByID.set(details.requestId, headers);
}

function isDuplicate(url) {
    const now = Date.now();
    // Clean old entries
    for (const [key, ts] of recentDispatches) {
        if (now - ts > DEDUP_WINDOW_MS) recentDispatches.delete(key);
    }
    if (recentDispatches.has(url)) return true;
    recentDispatches.set(url, now);
    return false;
}

// ──────────────────────────────────────────────────────────────
// Dispatch intercepted URL to Hydra Core Daemon
// ──────────────────────────────────────────────────────────────
async function dispatchToHydra(url, suggestedFilename, initiator, capturedHeaders = {}) {
    if (!url || isHydraLocalEndpoint(url)) {
        return;
    }

    if (isDuplicate(url)) {
        console.log("[Hydra] Skipping duplicate dispatch:", url);
        return;
    }

    try {
        let cookieString = capturedHeaders.Cookie || capturedHeaders.cookie || "";
        if (!cookieString) {
            try {
                const cookies = await chrome.cookies.getAll({ url: url });
                cookieString = cookies.map(c => `${c.name}=${c.value}`).join('; ');
            } catch (e) {
                // Cookie extraction may fail for cross-domain restrictions
            }
        }

        const payload = {
            url: url,
            save_path: "DEFAULT",
            filename: decodeURIComponent(suggestedFilename || "downloaded_file.bin"),
            headers: { ...capturedHeaders, "Cookie": cookieString, "Referer": capturedHeaders.Referer || initiator || url }
        };

        const res = await fetch(HYDRA_API_URL, {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
                "X-Hydra-Token": "hydra_secure_token_bf1f753e"
            },
            body: JSON.stringify(payload)
        });

        const data = await res.json();
        if (data.job_id) {
            chrome.tabs.create({ url: "http://127.0.0.1:9000" });
        }
    } catch (err) {
        console.error("[Hydra Sniffer] Dispatch error:", err);
    }
}

// ──────────────────────────────────────────────────────────────
// 1. Network Response Interceptor (catches DDL links by headers)
// ──────────────────────────────────────────────────────────────
chrome.webRequest.onHeadersReceived.addListener(
    (details) => {
        console.log("[Hydra] headers received:", details.type, details.url);
        if (isHydraLocalEndpoint(details.url)) {
            return;
        }

        let shouldIntercept = false;
        let filename = "";

        const getHeader = (name) =>
            details.responseHeaders?.find(h => h.name.toLowerCase() === name)?.value || "";

        const contentDisposition = getHeader("content-disposition");
        const contentType = getHeader("content-type").toLowerCase();
        const contentLength = parseInt(getHeader("content-length") || "0", 10);

        // ── Check 1: Content-Disposition header (strongest signal) ──
        if (contentDisposition) {
            const cdLower = contentDisposition.toLowerCase();
            if (cdLower.includes("attachment") || cdLower.includes("filename=")) {
                shouldIntercept = true;

                // Extract filename from header
                const match = contentDisposition.match(/filename\*?=(?:UTF-8''|")?([^";\n]+)/i);
                if (match && match[1]) {
                    filename = match[1].replace(/['"]/g, '').trim();
                }
            }
        }

        // ── Check 2: Binary MIME type with matching file extension ──
        if (!shouldIntercept && contentType) {
            const isWebpage = WebpageMimePatterns.some(m => contentType.includes(m));
            if (isWebpage) return; // Never intercept webpages

            const isBinaryMime = BinaryMimePatterns.some(m => contentType.includes(m));
            if (isBinaryMime) {
                try {
                    const urlPath = new URL(details.url).pathname;
                    const ext = urlPath.split('.').pop().toLowerCase();
                    if (SnatchExtensions.has(ext)) {
                        shouldIntercept = true;
                        filename = urlPath.split('/').pop() || "";
                    }
                } catch (e) { }
            }
        }

        // ── Check 3: application/octet-stream WITHOUT file extension ──
        // (Common on MediaFire, GoFile, Pixeldrain — URLs like /download/abc123)
        if (!shouldIntercept && contentType.includes("application/octet-stream")) {
            // Only intercept if file is large enough to be a real download
            if (contentLength >= MIN_INTERCEPT_BYTES || contentLength === 0) {
                shouldIntercept = true;
                // Try to get filename from Content-Disposition (already checked above)
                // or fall back to last URL segment
            }
        }

        // ── Check 4: URL extension match even without MIME validation ──
        // (Some CDNs serve files with wrong or missing Content-Type)
        if (!shouldIntercept) {
            try {
                const urlPath = new URL(details.url).pathname;
                const ext = urlPath.split('.').pop().toLowerCase();
                if (SnatchExtensions.has(ext) && contentLength >= MIN_INTERCEPT_BYTES) {
                    shouldIntercept = true;
                    filename = urlPath.split('/').pop() || "";
                }
            } catch (e) { }
        }

        if (shouldIntercept) {
            if (!filename) {
                filename = details.url.split('/').pop().split('?')[0] || "downloaded_file.bin";
            }
            // Clean URL-encoded characters from filename
            try { filename = decodeURIComponent(filename); } catch (e) { }

            console.log("[Hydra] Intercepting download:", filename, details.url);
            // Cancel the browser response immediately, then let Hydra download it.
            const requestHeaders = requestHeadersByID.get(details.requestId) || {};
            requestHeadersByID.delete(details.requestId);
            void dispatchToHydra(details.url, filename, details.initiator || details.url, requestHeaders);
            return { cancel: true };
        }

        return {};
    },
    { urls: ["<all_urls>"], types: ["main_frame", "sub_frame", "other"] },
    ["blocking", "responseHeaders"]
);

chrome.webRequest.onBeforeSendHeaders.addListener(
    (details) => {
        if (!isHydraLocalEndpoint(details.url)) {
            captureRequestHeaders(details);
        }
        return {};
    },
    { urls: ["<all_urls>"], types: ["main_frame", "sub_frame", "other", "xmlhttprequest"] },
    ["requestHeaders"]
);

// ──────────────────────────────────────────────────────────────
// 2. Native Browser Download Interceptor (fallback safety net)
//    Catches downloads triggered by JS blobs, programmatic clicks, etc.
// ──────────────────────────────────────────────────────────────
if (chrome.downloads && chrome.downloads.onCreated) {
    chrome.downloads.onCreated.addListener(async (downloadItem) => {
        console.log("[Hydra] Browser download created:", downloadItem.url);
        if (!downloadItem.url ||
            downloadItem.url.startsWith("blob:") ||
            isHydraLocalEndpoint(downloadItem.url)) {
            return;
        }

        // Cancel the browser's built-in download and hand over to Hydra
        try {
            await chrome.downloads.cancel(downloadItem.id);
            await chrome.downloads.erase({ id: downloadItem.id });
        } catch (e) { }

        await dispatchToHydra(
            downloadItem.url,
            downloadItem.filename || downloadItem.url.split('/').pop(),
            downloadItem.referrer
        );
    });
}