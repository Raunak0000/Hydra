import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import {
    buildDestinationPresets,
    buildDownloadPayload,
    consumeBypassStage,
    createBypassEntry,
    evaluateDiskSpace,
    evaluateDownloadItemForInterception,
    evaluateResponseForInterception,
    filenameFromURL,
    isHydraEndpoint,
    joinSavePath,
    parseContentDispositionFilename,
    pruneBypassMap,
    resolveCategoryDestination,
    summarizeIntegrationHealth,
    validateDownloadOptions,
    validateFilename,
} from "./shared.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

test("manifest.json is valid MV3 with Firefox WebExtension and desktop bridge permissions", () => {
    const raw = fs.readFileSync(path.join(__dirname, "manifest.json"), "utf8");
    const manifest = JSON.parse(raw);

    assert.equal(manifest.manifest_version, 3);
    assert.ok(Array.isArray(manifest.background.scripts));
    assert.equal(manifest.background.type, "module");
    assert.ok(manifest.permissions.includes("downloads"));
    assert.ok(manifest.permissions.includes("webRequest"));
    assert.ok(manifest.permissions.includes("webRequestBlocking"));
    assert.ok(manifest.permissions.includes("nativeMessaging"));
    assert.ok(manifest.permissions.includes("storage"));
    assert.equal(manifest.browser_specific_settings?.gecko?.id, "hydra-core-sniffer@hydra.app");
    assert.deepEqual(manifest.browser_specific_settings?.gecko?.data_collection_permissions?.required, ["none"]);

    for (const rel of Object.values(manifest.icons || {})) {
        assert.ok(fs.existsSync(path.join(__dirname, rel)), `missing icon ${rel}`);
    }

    const nativeManifest = JSON.parse(
        fs.readFileSync(path.join(__dirname, "..", "packaging", "extension", "org.hydra.downloader.json"), "utf8")
    );
    assert.ok(
        nativeManifest.allowed_extensions.includes(manifest.browser_specific_settings.gecko.id),
        "native messaging allowed_extensions must match gecko.id"
    );
});

test("Content-Disposition and URL filename extraction sanitize and decode cleanly", () => {
    assert.equal(
        parseContentDispositionFilename("attachment; filename*=UTF-8''Ubuntu%2024.04%20Desktop.iso"),
        "Ubuntu 24.04 Desktop.iso"
    );
    assert.equal(
        parseContentDispositionFilename('attachment; filename="../../etc/passwd"'),
        ".._.._etc_passwd"
    );
    assert.equal(
        filenameFromURL("https://cdn.example.org/files/release%20v1.tar.gz?token=abc"),
        "release v1.tar.gz"
    );
    assert.equal(validateFilename("archive.zip"), null);
    assert.ok(validateFilename(""));
    assert.ok(validateFilename("dir/file.zip"));
    assert.ok(validateFilename(".."));
});

test("response and download interception filters webpages, excludes hosts, and matches archives/attachments", () => {
    assert.equal(isHydraEndpoint("http://127.0.0.1:9000/download"), true);
    assert.equal(isHydraEndpoint("https://example.com/archive.zip"), false);

    // Attachment disposition intercepts regardless of MIME
    const cd = evaluateResponseForInterception({
        url: "https://overleaf.com/project/123/output/output.pdf?compile=1",
        responseHeaders: [
            { name: "Content-Type", value: "application/pdf" },
            { name: "Content-Disposition", value: 'attachment; filename="paper.pdf"' },
            { name: "Content-Length", value: "102400" },
        ],
    });
    assert.equal(cd.intercept, true);
    assert.equal(cd.filename, "paper.pdf");
    assert.equal(cd.contentLength, 102400);

    // HTML page is never hijacked
    const html = evaluateResponseForInterception({
        url: "https://example.com/file.zip",
        responseHeaders: [{ name: "Content-Type", value: "text/html; charset=utf-8" }],
    });
    assert.equal(html.intercept, false);
    assert.equal(html.reason, "webpage_mime");

    // Excluded domain is skipped
    const excluded = evaluateResponseForInterception({
        url: "https://intranet.local/build.zip",
        responseHeaders: [{ name: "Content-Disposition", value: 'attachment; filename="build.zip"' }],
        settings: { excludedHosts: ["intranet.local"] },
    });
    assert.equal(excluded.intercept, false);
    assert.equal(excluded.reason, "excluded_host");

    // Paused capture skips everything
    const paused = evaluateResponseForInterception({
        url: "https://example.com/ubuntu.iso",
        responseHeaders: [
            { name: "Content-Type", value: "application/octet-stream" },
            { name: "Content-Length", value: String(2 * 1024 * 1024 * 1024) },
        ],
        settings: { autoCapture: false },
    });
    assert.equal(paused.intercept, false);

    // downloads.onCreated fallback inherits cached headers + byte size
    const itemRes = evaluateDownloadItemForInterception({
        item: { url: "https://dl.example.com/app.AppImage", totalBytes: 0 },
        cachedMeta: { contentLength: 85_000_000, contentType: "application/octet-stream" },
    });
    assert.equal(itemRes.intercept, true);
    assert.equal(itemRes.filename, "app.AppImage");
    assert.equal(itemRes.contentLength, 85_000_000);
});

test("destination presets, validation, and payload builder support folder, pending, speed cap, checksum, pause, and schedule", () => {
    const daemonSettings = {
        default_save_path: "~/Downloads",
        categories: {
            Video: { extensions: ["mkv", "mp4"], path: "~/Videos" },
            Compressed: { extensions: ["zip", "iso"], path: "~/Downloads/Compressed" },
        },
    };

    const dest = resolveCategoryDestination("movie.mkv", daemonSettings);
    assert.equal(dest.category, "Video");
    assert.equal(dest.path, "~/Videos");

    const presets = buildDestinationPresets("movie.mkv", daemonSettings, ["/mnt/media", "~/Videos"]);
    assert.equal(presets[0].path, "~/Videos");
    assert.ok(presets.some((p) => p.path === "/mnt/media"));
    assert.equal(presets.filter((p) => p.path === "~/Videos").length, 1);

    assert.equal(joinSavePath("~/Videos", "movie.mkv"), "~/Videos/movie.mkv");
    assert.equal(joinSavePath("~/Videos/", "movie.mkv"), "~/Videos/movie.mkv");

    const futureDate = new Date(Date.now() + 3600_000);
    const pad2 = (n) => String(n).padStart(2, "0");
    const futureLocal = `${futureDate.getFullYear()}-${pad2(futureDate.getMonth() + 1)}-${pad2(
        futureDate.getDate()
    )}T${pad2(futureDate.getHours())}:${pad2(futureDate.getMinutes())}`;
    assert.equal(
        validateDownloadOptions({
            filename: "movie.mkv",
            saveMode: "folder",
            destinationDir: "~/Videos",
            speedLimitKbps: 2048,
            checksumAlgo: "sha256",
            expectedChecksum: "abcdef123456",
            startMode: "schedule",
            scheduledAt: futureLocal,
        }),
        null
    );

    assert.ok(
        validateDownloadOptions({
            filename: "movie.mkv",
            saveMode: "folder",
            destinationDir: "~/Videos",
            checksumAlgo: "",
            expectedChecksum: "abcd",
        })
    );
    assert.ok(
        validateDownloadOptions({
            filename: "movie.mkv",
            saveMode: "folder",
            destinationDir: "",
        })
    );

    const payload = buildDownloadPayload(
        {
            url: "https://example.com/movie.mkv",
            filename: "movie.mkv",
            referrer: "https://example.com/page",
            capturedHeaders: { Cookie: "a=b" },
        },
        {
            filename: "renamed.mkv",
            saveMode: "folder",
            destinationDir: "/mnt/media",
            speedLimitKbps: 512,
            checksumAlgo: "sha256",
            expectedChecksum: "deadbeef",
            startMode: "paused",
            includeHeaders: false,
        }
    );
    assert.equal(payload.save_path, "/mnt/media/renamed.mkv");
    assert.equal(payload.filename, "renamed.mkv");
    assert.equal(payload.max_speed_bytes, 512 * 1024);
    assert.equal(payload.checksum_algo, "sha256");
    assert.equal(payload.expected_checksum, "deadbeef");
    assert.equal(payload.start_paused, true);
    assert.deepEqual(payload.headers, {});

    const pendingPayload = buildDownloadPayload(
        { url: "https://example.com/movie.mkv", filename: "movie.mkv" },
        { filename: "movie.mkv", saveMode: "pending" }
    );
    assert.equal(pendingPayload.save_path, "PENDING");
});

test("disk space and integration health summarizer provide clear status and recovery guidance", () => {
    const spaceBad = evaluateDiskSpace({ available_bytes: 1024, enough_space: false }, 10_000_000);
    assert.equal(spaceBad.level, "error");
    assert.match(spaceBad.message, /Insufficient space/);

    const offlineNative = summarizeIntegrationHealth({
        daemonReachable: false,
        nativeHostAvailable: true,
        autoCapture: true,
    });
    assert.equal(offlineNative.state, "offline");
    assert.equal(offlineNative.badgeText, "ERR");
    assert.ok(offlineNative.recoverySteps.some((s) => s.includes("Start Hydra Daemon")));

    const needsToken = summarizeIntegrationHealth({
        daemonReachable: true,
        authenticated: false,
        tokenConfigured: false,
        nativeHostAvailable: false,
        autoCapture: true,
    });
    assert.equal(needsToken.state, "needs_token");
    assert.equal(needsToken.badgeText, "KEY");
    assert.ok(needsToken.recoverySteps.some((s) => s.includes("api.token")));

    const paused = summarizeIntegrationHealth({ autoCapture: false });
    assert.equal(paused.state, "paused");
    assert.equal(paused.badgeText, "OFF");

    const ok = summarizeIntegrationHealth({
        daemonReachable: true,
        authenticated: true,
        tokenConfigured: true,
        nativeHostAvailable: true,
        autoCapture: true,
    });
    assert.equal(ok.state, "connected");
    assert.equal(ok.badgeText, "ON");
});

test("browser fallback bypass permits both webRequest and downloads hooks once in either order", () => {
    const url = "http://127.0.0.2:9100/hydra-test-archive.zip";
    const map1 = { [url]: createBypassEntry(60_000, 1_000) };
    // downloads.onCreated fires before webRequest.onHeadersReceived
    assert.equal(consumeBypassStage(map1, url, "downloads", 1_010), true);
    assert.equal(consumeBypassStage(map1, url, "webRequest", 1_020), true);
    // Both consumed -> cleared, so subsequent clicks are intercepted again
    assert.equal(map1[url], undefined);
    assert.equal(consumeBypassStage(map1, url, "webRequest", 1_030), false);

    const map2 = { [url]: createBypassEntry(60_000, 2_000) };
    // webRequest.onHeadersReceived fires before downloads.onCreated
    assert.equal(consumeBypassStage(map2, url, "webRequest", 2_010), true);
    assert.equal(consumeBypassStage(map2, url, "downloads", 2_020), true);
    assert.equal(map2[url], undefined);

    // Expired entries are pruned
    const map3 = { [url]: createBypassEntry(500, 3_000) };
    pruneBypassMap(map3, 4_000);
    assert.equal(map3[url], undefined);
});
