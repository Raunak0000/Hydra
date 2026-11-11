/**
 * Hydra Download Manager — Extension Settings Page Script
 */

import { DEFAULT_SETTINGS, normalizeDaemonURL } from "./shared.js";

const $ = (id) => document.getElementById(id);

const inpURL = $("inp-daemon-url");
const inpToken = $("inp-token");
const chkAutoCapture = $("chk-auto-capture");
const chkConfirmDialog = $("chk-confirm-dialog");
const chkIncludeHeaders = $("chk-include-headers");
const chkNotifications = $("chk-notifications");
const inpMinKB = $("inp-min-kb");
const selDefaultStart = $("sel-default-start");
const inpCustomExts = $("inp-custom-exts");
const inpExcludedHosts = $("inp-excluded-hosts");

const elDiagHeadline = $("diag-headline");
const elDiagDetail = $("diag-detail");
const btnTest = $("btn-test");
const btnAutoPair = $("btn-auto-pair");
const btnStartDaemon = $("btn-start-daemon");
const btnSave = $("btn-save");
const elStatus = $("status");

function setStatus(msg, cls = "") {
    elStatus.textContent = msg;
    elStatus.className = cls;
}

function sendMsg(msg) {
    return chrome.runtime.sendMessage(msg);
}

function splitList(raw) {
    return String(raw || "")
        .split(/[,\n]+/)
        .map((s) => s.trim())
        .filter(Boolean);
}

function populateForm(settings) {
    const s = { ...DEFAULT_SETTINGS, ...(settings || {}) };
    inpURL.value = s.daemonURL;
    inpToken.value = s.apiToken || "";
    chkAutoCapture.checked = s.autoCapture !== false;
    chkConfirmDialog.checked = s.confirmBeforeDownload !== false;
    chkIncludeHeaders.checked = s.includeHeadersByDefault !== false;
    chkNotifications.checked = s.notificationsEnabled !== false;
    inpMinKB.value = String(Number.isFinite(Number(s.minInterceptKB)) ? s.minInterceptKB : 512);
    selDefaultStart.value = s.defaultStartMode === "paused" ? "paused" : "now";
    inpCustomExts.value = Array.isArray(s.customExtensions) ? s.customExtensions.join(", ") : "";
    inpExcludedHosts.value = Array.isArray(s.excludedHosts) ? s.excludedHosts.join(", ") : "127.0.0.1, localhost";
}

function collectFormSettings() {
    const rawURL = inpURL.value.trim();
    if (!rawURL) {
        throw new Error("Daemon URL cannot be empty.");
    }
    try {
        new URL(rawURL);
    } catch {
        throw new Error("Daemon URL is malformed.");
    }

    const minKB = Math.max(0, parseInt(inpMinKB.value || "512", 10) || 0);
    return {
        daemonURL: normalizeDaemonURL(rawURL),
        apiToken: inpToken.value.trim(),
        autoCapture: chkAutoCapture.checked,
        confirmBeforeDownload: chkConfirmDialog.checked,
        includeHeadersByDefault: chkIncludeHeaders.checked,
        notificationsEnabled: chkNotifications.checked,
        minInterceptKB: minKB,
        defaultStartMode: selDefaultStart.value === "paused" ? "paused" : "now",
        customExtensions: splitList(inpCustomExts.value).map((e) => e.toLowerCase().replace(/^\.+/, "")),
        excludedHosts: splitList(inpExcludedHosts.value).map((h) => h.toLowerCase()),
    };
}

function renderHealth(health) {
    if (!health?.summary) return;
    const sum = health.summary;
    elDiagHeadline.textContent = sum.headline;
    elDiagHeadline.className = `diag-headline ${
        sum.state === "connected" ? "ok" : sum.state === "offline" ? "error" : "warn"
    }`;

    const bridgeNote = health.nativeHostAvailable
        ? "Native desktop bridge: installed ✓"
        : "Native desktop bridge: not installed (run ./install.sh to enable auto-pair & start daemon)";
    elDiagDetail.textContent = `${sum.detail} — ${bridgeNote}`;

    btnAutoPair.disabled = !health.nativeHostAvailable;
    btnStartDaemon.disabled = !health.nativeHostAvailable;

    if (health.settings?.apiToken && !inpToken.value.trim()) {
        inpToken.value = health.settings.apiToken;
    }
}

async function saveCurrentForm() {
    const settings = collectFormSettings();
    const resp = await sendMsg({ type: "SAVE_SETTINGS", settings });
    if (resp?.health) renderHealth(resp.health);
    return resp;
}

btnSave.addEventListener("click", async () => {
    try {
        const resp = await saveCurrentForm();
        if (resp?.ok) {
            setStatus("✓ Settings saved.", "success");
        } else {
            setStatus(resp?.error || "Failed to save settings.", "error");
        }
    } catch (err) {
        setStatus(err.message, "error");
    }
});

btnTest.addEventListener("click", async () => {
    btnTest.disabled = true;
    setStatus("Testing connection to Hydra daemon…");
    try {
        await saveCurrentForm();
        const resp = await sendMsg({ type: "TEST_CONNECTION", tryAutoPair: true });
        if (resp?.health) {
            renderHealth(resp.health);
            const st = resp.health.summary.state;
            setStatus(
                st === "connected" ? "✓ Daemon reachable and authenticated." : resp.health.summary.headline,
                st === "connected" ? "success" : "warn"
            );
        }
    } catch (err) {
        setStatus(err.message, "error");
    } finally {
        btnTest.disabled = false;
    }
});

btnAutoPair.addEventListener("click", async () => {
    btnAutoPair.disabled = true;
    setStatus("Importing API token via desktop bridge…");
    const resp = await sendMsg({ type: "PAIR_VIA_NATIVE_HOST" });
    if (resp?.ok && resp.health) {
        if (resp.health.settings?.apiToken) {
            inpToken.value = resp.health.settings.apiToken;
        }
        renderHealth(resp.health);
        setStatus("✓ Imported ~/.config/hydra/api.token automatically.", "success");
    } else {
        setStatus(resp?.error || "Auto-pair failed.", "error");
    }
    btnAutoPair.disabled = false;
});

btnStartDaemon.addEventListener("click", async () => {
    btnStartDaemon.disabled = true;
    setStatus("Starting Hydra daemon…");
    const resp = await sendMsg({ type: "START_DAEMON_AND_RETRY" });
    if (resp?.health) renderHealth(resp.health);
    if (resp?.ok) {
        if (resp.health?.settings?.apiToken) {
            inpToken.value = resp.health.settings.apiToken;
        }
        setStatus("✓ Hydra daemon is running.", "success");
    } else {
        setStatus(resp?.native?.error || "Could not start Hydra daemon.", "error");
    }
    btnStartDaemon.disabled = false;
});

async function init() {
    const [settingsResp, statusResp] = await Promise.all([
        sendMsg({ type: "GET_SETTINGS" }),
        sendMsg({ type: "TEST_CONNECTION", tryAutoPair: true }),
    ]);
    if (settingsResp?.ok) {
        populateForm(settingsResp.settings);
    }
    if (statusResp?.ok && statusResp.health) {
        if (statusResp.health.settings) {
            populateForm(statusResp.health.settings);
        }
        renderHealth(statusResp.health);
    }
}

void init();
