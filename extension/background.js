// extension/background.js

const HYDRA_API_URL = "http://localhost:9000/download";
const HYDRA_API_FALLBACK = "http://127.0.0.1:9000/download";

// Track URLs initiated by extension to avoid infinite loops when falling back
const ignoredUrls = new Set();

// Listen for download triggers inside the browser
chrome.downloads.onCreated.addListener(async (downloadItem) => {
    console.log("[Hydra Extension] Intercepted raw download item:", downloadItem);

    // Skip internal browser protocols and dashboard actions to prevent loops
    if (!downloadItem.url ||
        downloadItem.url.startsWith("blob:") ||
        downloadItem.url.startsWith("data:") ||
        downloadItem.url.includes("localhost") ||
        downloadItem.url.includes("127.0.0.1")) {
        return;
    }

    // Skip downloads that were re-initiated by us as a fallback
    if (ignoredUrls.has(downloadItem.url)) {
        console.log("[Hydra Extension] Skipping ignored fallback download:", downloadItem.url);
        return;
    }

    // Skip items that are not in progress (e.g. already complete or cancelled)
    if (downloadItem.state && downloadItem.state !== "in_progress") {
        return;
    }

    // 1. Immediately cancel and erase the browser's default download to prevent double downloading
    try {
        await chrome.downloads.cancel(downloadItem.id);
        await chrome.downloads.erase({ id: downloadItem.id });
        console.log(`[Hydra Extension] Instantly cancelled and purged browser download ID: ${downloadItem.id}`);
    } catch (err) {
        console.error("[Hydra Extension] Failed to cancel browser download:", err.message);
        // If we can't cancel it, the browser is downloading it, so abort to avoid double downloading
        return;
    }

    // Extract filename from the URL path if the browser hasn't resolved one yet
    let filename = downloadItem.filename ? downloadItem.filename.split('/').pop() : "";
    if (!filename) {
        filename = downloadItem.url.split('/').pop().split('?')[0] || "downloaded_file";
    }
    if (!filename.includes(".")) {
        filename += ".bin";
    }

    // Helper to send request to Hydra backend
    const routeToHydra = async () => {
        const payload = {
            url: downloadItem.url,
            save_path: "/home/raunak/Downloads/" + filename
        };

        console.log(`[Hydra Extension] Routing ${filename} to Hydra Core...`);

        try {
            // Try primary endpoint (localhost)
            let res;
            try {
                res = await fetch(HYDRA_API_URL, {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify(payload)
                });
            } catch (fetchErr) {
                console.warn("[Hydra Extension] Primary connection failed, attempting loopback fallback...", fetchErr.message);
                // Fallback endpoint (127.0.0.1)
                res = await fetch(HYDRA_API_FALLBACK, {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify(payload)
                });
            }

            if (res.ok) {
                console.log("[Hydra Extension] Successfully routed payload to Hydra Core.");
            } else {
                throw new Error(`Hydra backend returned status: ${res.status}`);
            }
        } catch (err) {
            console.error(`[Hydra Extension] Routing failed: ${err.message}. Re-initiating native browser download.`);
            
            // Fallback: Re-initiate download in browser natively
            try {
                ignoredUrls.add(downloadItem.url);
                await chrome.downloads.download({ url: downloadItem.url });
                console.log(`[Hydra Extension] Safely re-initiated browser download for: ${downloadItem.url}`);
                
                // Clear the URL from ignored list after 5 seconds to allow future normal downloads
                setTimeout(() => ignoredUrls.delete(downloadItem.url), 5000);
            } catch (downloadErr) {
                console.error("[Hydra Extension] Browser fallback download failed to start:", downloadErr.message);
            }
        }
    };

    routeToHydra();
});
