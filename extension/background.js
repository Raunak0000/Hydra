const HYDRA_API_URL = "http://127.0.0.1:9000/download";

const SnatchExtensions = new Set([
    "zip", "tar", "gz", "7z", "rar", "iso", "bin", "exe", "dmg", "mp4", "mkv", "avi", "mov", "flv", "webm", "ts"
]);

// Helper to dispatch intercepted URLs directly to Hydra Core Daemon
async function dispatchToHydra(url, suggestedFilename, initiator) {
    if (!url || url.includes("127.0.0.1") || url.includes("localhost")) {
        return;
    }

    try {
        let cookieString = "";
        try {
            const cookies = await chrome.cookies.getAll({ url: url });
            cookieString = cookies.map(c => `${c.name}=${c.value}`).join('; ');
        } catch (e) {
            // Cookie extraction fallback for cross-domain restrictions
        }

        const payload = {
            url: url,
            save_path: "PENDING",
            filename: decodeURIComponent(suggestedFilename || "downloaded_file.bin"),
            headers: {
                "Cookie": cookieString,
                "User-Agent": navigator.userAgent,
                "Referer": initiator || url
            }
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
            chrome.tabs.create({ url: "http://127.0.0.1:9000/?pending_job=" + data.job_id });
        }
    } catch (err) {
        console.error("[Hydra Sniffer] Dispatch error:", err);
    }
}

// 1. Broadened Network Request Listener across all request types
chrome.webRequest.onHeadersReceived.addListener(
    async (details) => {
        if (details.url.includes("127.0.0.1") || details.url.includes("localhost")) {
            return;
        }

        let isAttachment = false;
        let filename = "";

        const dispositionHeader = details.responseHeaders?.find(
            h => h.name.toLowerCase() === "content-disposition"
        );
        const contentTypeHeader = details.responseHeaders?.find(
            h => h.name.toLowerCase() === "content-type"
        );

        if (dispositionHeader && dispositionHeader.value) {
            const value = dispositionHeader.value.toLowerCase();
            if (value.includes("attachment") || value.includes("filename=")) {
                isAttachment = true;
                const match = dispositionHeader.value.match(/filename\*?=["']?([^"';\n]+)/i);
                if (match && match[1]) {
                    filename = match[1].replace(/utf-8''/i, '').replace(/['"]/g, '');
                }
            }
        }

        if (!isAttachment) {
            try {
                const urlPath = new URL(details.url).pathname;
                const ext = urlPath.split('.').pop().toLowerCase();
                if (SnatchExtensions.has(ext)) {
                    isAttachment = true;
                    filename = urlPath.split('/').pop() || "download_asset.bin";
                }
            } catch (e) { }
        }

        if (isAttachment) {
            if (!filename) {
                filename = details.url.split('/').pop().split('?')[0] || "downloaded_file.bin";
            }
            await dispatchToHydra(details.url, filename, details.initiator || details.url);
        }
    },
    { urls: ["<all_urls>"] }, // 🚀 Intercepts ALL request types (not just main_frame)
    ["responseHeaders"]
);

// 2. Native Firefox Download Interceptor (Catches any browser-initiated file downloads)
if (chrome.downloads && chrome.downloads.onCreated) {
    chrome.downloads.onCreated.addListener(async (downloadItem) => {
        if (downloadItem.url.includes("127.0.0.1") || downloadItem.url.includes("localhost")) {
            return;
        }

        // Cancel the browser's built-in download mechanism and hand over to Hydra
        try {
            await chrome.downloads.cancel(downloadItem.id);
            await chrome.downloads.erase({ id: downloadItem.id });
        } catch (e) { }

        await dispatchToHydra(downloadItem.url, downloadItem.filename, downloadItem.referrer);
    });
}