// extension/background.js

const HYDRA_API_URL = "http://localhost:9000/download";

// Target file extensions to snatch automatically regardless of disposition
const SnatchExtensions = new Set([
    "zip", "tar", "gz", "7z", "rar", "iso", "bin", "exe", "dmg", "mp4", "mkv"
]);

// Listen for network response headers before the browser processes the download dialog
chrome.webRequest.onHeadersReceived.addListener(
    async (details) => {
        // Skip internal tracking and loopback requests to avoid cycles
        if (details.url.includes("localhost") || details.url.includes("127.0.0.1")) {
            return;
        }

        let isAttachment = false;
        let filename = "";

        // Sniff response headers for attachment attributes
        const dispositionHeader = details.responseHeaders.find(
            h => h.name.toLowerCase() === "content-disposition"
        );
        const contentTypeHeader = details.responseHeaders.find(
            h => h.name.toLowerCase() === "content-type"
        );

        if (dispositionHeader && dispositionHeader.value) {
            const value = dispositionHeader.value.toLowerCase();
            if (value.includes("attachment")) {
                isAttachment = true;
                // Attempt to extract filename parameter cleanly
                const match = dispositionHeader.value.match(/filename\*?=["']?([^"';\n]+)/i);
                if (match && match[1]) {
                    filename = match[1].replace(/utf-8''/i, '');
                }
            }
        }

        // Fallback: If no explicit attachment directive, check URL lexical structure extensions
        if (!isAttachment) {
            const urlPath = new URL(details.url).pathname;
            const ext = urlPath.split('.').pop().toLowerCase();
            if (SnatchExtensions.has(ext)) {
                isAttachment = true;
                filename = urlPath.split('/').pop() || "download_asset.bin";
            }
        }

        // If it's a valid targeted download stream, snatch it!
        if (isAttachment) {
            console.log(`[Hydra Sniffer] Snatched target download stream: ${details.url}`);

            if (!filename) {
                filename = details.url.split('/').pop().split('?')[0] || "downloaded_file.bin";
            }

            // Route to engine using your Phase 6 session cookie logic
            chrome.cookies.getAll({ url: details.url }, async (cookies) => {
                const cookieString = cookies.map(c => `${c.name}=${c.value}`).join('; ');

                const payload = {
                    url: details.url,
                    save_path: "/home/raunak/Downloads/" + decodeURIComponent(filename),
                    headers: {
                        "Cookie": cookieString,
                        "User-Agent": navigator.userAgent,
                        "Referer": details.initiator || ""
                    }
                };

                try {
                    await fetch(HYDRA_API_URL, {
                        method: "POST",
                        headers: { 
                            "Content-Type": "application/json",
                            "X-Hydra-Token": "hydra_secure_token_bf1f753e"
                        },
                        body: JSON.stringify(payload)
                    });
                    console.log("[Hydra Sniffer] Core server notified successfully.");
                } catch (err) {
                    console.error("[Hydra Sniffer] Core connection dropped:", err.message);
                }
            });

            // 🚨 THE HOLY GRAIL: Return a redirect directive to a dead endpoint cancel string.
            // This forces the browser to completely drop the request on its side immediately,
            // preventing the browser from starting any parallel single-threaded download traffic!
            return { redirectUrl: "javascript:void(0)" };
        }
    },
    { urls: ["<all_urls>"], types: ["main_frame", "sub_frame"] },
    ["responseHeaders", "blocking"]
);