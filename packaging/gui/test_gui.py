import importlib.machinery
import importlib.util
import json
import os
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

GUI_DIR = Path(__file__).resolve().parent
if str(GUI_DIR) not in sys.path:
    sys.path.insert(0, str(GUI_DIR))

from hydra_gui.client import (  # noqa: E402
    HydraGUIClient,
    SSEWatcher,
    daemon_address_from_url,
    probe_gui_backend,
    resolve_token,
)
from hydra_gui.models import (  # noqa: E402
    SpeedHistoryTracker,
    available_job_actions,
    compute_chunk_stats,
    compute_dashboard_stats,
    display_name_for_job,
    effective_total_size_str,
    filter_jobs,
    format_bytes,
    format_speed,
    infer_category,
    mask_headers,
    parse_byte_string,
    parse_speed_bps,
    parse_timestamp,
    resolve_category_destination,
    sanitize_url_for_display,
    sort_jobs,
    suggest_filename_from_url,
    validate_download_url,
    validate_filename,
)


def load_gui_entrypoint():
    gui_path = GUI_DIR / "hydra-gui"
    loader = importlib.machinery.SourceFileLoader("hydra_gui_bin", str(gui_path))
    spec = importlib.util.spec_from_loader("hydra_gui_bin", loader)
    assert spec is not None
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


class HydraGUIModelsTest(unittest.TestCase):
    def test_parse_byte_string_and_speed_bps(self):
        self.assertEqual(parse_byte_string("512 B"), 512)
        self.assertEqual(parse_byte_string("2.00 KB"), 2048)
        self.assertEqual(parse_byte_string("1.50 MB"), int(1.5 * 1024 * 1024))
        self.assertEqual(parse_byte_string("2.00 GB"), 2 * 1024 ** 3)
        self.assertEqual(parse_byte_string("Calculating..."), 0)
        self.assertEqual(parse_byte_string("Unknown Size"), 0)
        self.assertEqual(parse_byte_string("--"), 0)

        self.assertAlmostEqual(parse_speed_bps("4.00 MB/s"), 4.0 * 1024 ** 2, delta=1.0)
        self.assertAlmostEqual(parse_speed_bps("256.00 KB/s"), 256.0 * 1024, delta=1.0)
        self.assertEqual(parse_speed_bps("0.00 KB/s"), 0.0)
        self.assertEqual(parse_speed_bps("--"), 0.0)

        self.assertEqual(format_bytes(500), "500 B")
        self.assertEqual(format_bytes(2048), "2.00 KB")
        self.assertEqual(format_speed(0.0), "0.00 KB/s")
        self.assertEqual(format_speed(2 * 1024 ** 2), "2.00 MB/s")

        # effective_total_size_str fallback for historical jobs without persisted total_size
        self.assertEqual(
            effective_total_size_str({"total_size": "Calculating...", "chunks": [{"end": 2047}]}),
            "2.00 KB",
        )
        self.assertEqual(
            effective_total_size_str({"total_size": "Calculating...", "status": "COMPLETED", "downloaded": "2.68 GB"}),
            "2.68 GB",
        )
        self.assertEqual(
            effective_total_size_str({"total_size": "Calculating...", "status": "DOWNLOADING", "downloaded": "0 B"}),
            "Calculating...",
        )

    def test_display_name_and_category_inference(self):
        self.assertEqual(
            display_name_for_job(
                {"id": "j1", "file_name": "Calculating...", "save_path": "/home/user/Videos/movie.mkv", "url": "https://x/a"}
            ),
            "movie.mkv",
        )
        self.assertEqual(
            display_name_for_job(
                {"id": "j2", "file_name": "report.pdf", "save_path": "PENDING", "url": "https://x/report.pdf"}
            ),
            "report.pdf",
        )
        self.assertEqual(
            display_name_for_job(
                {"id": "j3", "file_name": "Calculating...", "save_path": "DEFAULT", "url": "https://example.com/archive.tar.gz?token=1"}
            ),
            "archive.tar.gz",
        )

        self.assertEqual(infer_category("clip.mkv"), "Video")
        self.assertEqual(infer_category("song.flac"), "Music")
        self.assertEqual(infer_category("ubuntu.iso"), "Compressed")
        self.assertEqual(infer_category("hydra_1.0.0_amd64.deb"), "Programs")
        self.assertEqual(infer_category("thesis.pdf"), "Documents")
        self.assertEqual(infer_category("README"), "Other")

        custom = {"Research": {"extensions": ["ipynb", "csv"], "path": "~/Research"}}
        self.assertEqual(infer_category("analysis.ipynb", custom_categories=custom), "Research")

    def test_compute_chunk_stats(self):
        chunks = [
            {"index": 1, "start": 100, "current_offset": 150, "end": 199, "completed": False},
            {"index": 0, "start": 0, "current_offset": 100, "end": 99, "completed": True},
            {"index": 2, "start": 200, "current_offset": 200, "end": 299, "completed": False},
        ]
        stats = compute_chunk_stats(chunks, job_status="DOWNLOADING", job_progress=50.0)
        self.assertTrue(stats["has_chunks"])
        self.assertEqual(stats["total_chunks"], 3)
        self.assertEqual(stats["completed_chunks"], 1)
        self.assertEqual(stats["active_chunks"], 1)
        self.assertEqual(stats["pending_chunks"], 1)
        self.assertEqual(stats["total_bytes"], 300)
        self.assertEqual(stats["downloaded_bytes"], 150)
        self.assertAlmostEqual(stats["overall_fraction"], 0.5, places=4)
        self.assertEqual([s["index"] for s in stats["segments"]], [0, 1, 2])

        # Empty chunks fall back cleanly to overall job progress
        empty_stats = compute_chunk_stats([], job_status="DOWNLOADING", job_progress=42.0)
        self.assertFalse(empty_stats["has_chunks"])
        self.assertAlmostEqual(empty_stats["overall_fraction"], 0.42, places=4)

        # Single unknown-size stream (end == -1)
        stream_stats = compute_chunk_stats(
            [{"index": 0, "start": 0, "current_offset": 4096, "end": -1, "completed": False}],
            job_status="DOWNLOADING",
            job_progress=0.0,
        )
        self.assertEqual(stream_stats["segments"][0]["byte_range_label"], "Streaming (unknown total size)")

    def test_filtering_sorting_and_dashboard_stats(self):
        jobs = [
            {
                "id": "j1",
                "file_name": "alpha.mkv",
                "url": "https://cdn.example.com/alpha.mkv",
                "save_path": "/Videos/alpha.mkv",
                "status": "DOWNLOADING",
                "progress": 60.0,
                "speed": "5.00 MB/s",
                "total_size": "2.00 GB",
                "created_at": "2026-11-10T10:00:00Z",
            },
            {
                "id": "j2",
                "file_name": "beta.zip",
                "url": "https://mirror.org/beta.zip",
                "save_path": "/Downloads/beta.zip",
                "status": "PAUSED",
                "progress": 25.0,
                "speed": "10.00 MB/s",  # Should NOT be counted in active total speed
                "total_size": "500.00 MB",
                "batch_id": "batch_99",
                "created_at": "2026-11-10T11:00:00Z",
            },
            {
                "id": "j3",
                "file_name": "gamma.pdf",
                "url": "https://docs.org/gamma.pdf",
                "save_path": "/Documents/gamma.pdf",
                "status": "FAILED",
                "progress": 80.0,
                "speed": "0.00 KB/s",
                "total_size": "10.00 MB",
                "created_at": "2026-11-10T12:00:00Z",
            },
        ]

        self.assertEqual([j["id"] for j in filter_jobs(jobs, status_filter="active")], ["j1"])
        self.assertEqual([j["id"] for j in filter_jobs(jobs, status_filter="paused")], ["j2"])
        self.assertEqual([j["id"] for j in filter_jobs(jobs, category_filter="Documents")], ["j3"])
        self.assertEqual([j["id"] for j in filter_jobs(jobs, search_query="batch_99")], ["j2"])

        self.assertEqual([j["id"] for j in sort_jobs(jobs, sort_by="name", descending=False)], ["j1", "j2", "j3"])
        self.assertEqual([j["id"] for j in sort_jobs(jobs, sort_by="progress", descending=True)], ["j3", "j1", "j2"])
        self.assertEqual([j["id"] for j in sort_jobs(jobs, sort_by="size", descending=True)], ["j1", "j2", "j3"])
        self.assertEqual([j["id"] for j in sort_jobs(jobs, sort_by="added", descending=True)], ["j3", "j2", "j1"])

        stats = compute_dashboard_stats(jobs)
        self.assertEqual(stats["status_counts"]["all"], 3)
        self.assertEqual(stats["status_counts"]["active"], 1)
        self.assertEqual(stats["status_counts"]["paused"], 1)
        self.assertEqual(stats["status_counts"]["failed"], 1)
        self.assertEqual(stats["category_counts"]["Video"], 1)
        self.assertEqual(stats["category_counts"]["Compressed"], 1)
        self.assertEqual(stats["category_counts"]["Documents"], 1)
        self.assertAlmostEqual(stats["total_speed_bps"], 5.0 * 1024 ** 2, delta=1.0)
        self.assertEqual(stats["total_speed_text"], "5.00 MB/s")

    def test_speed_history_tracker(self):
        tracker = SpeedHistoryTracker(max_samples=4)
        for mb in [1, 2, 3, 4, 5]:
            tracker.record_jobs([{"id": "j1", "status": "DOWNLOADING", "speed": f"{mb}.00 MB/s"}])
        samples = tracker.get_samples("j1")
        self.assertEqual(len(samples), 4)
        self.assertAlmostEqual(samples[-1], 5.0 * 1024 ** 2, delta=1.0)

        tracker.record_jobs([])
        self.assertEqual(tracker.get_samples("j1"), [])

    def test_privacy_header_masking_and_timestamps(self):
        headers = {
            "User-Agent": "Mozilla/5.0",
            "Authorization": "Bearer supersecret",
            "Cookie": "session=abc123",
            "Referer": "https://example.com",
        }
        masked = {k: (v, sens) for k, v, sens in mask_headers(headers, reveal=False)}
        self.assertEqual(masked["User-Agent"], ("Mozilla/5.0", False))
        self.assertEqual(masked["Referer"], ("https://example.com", False))
        self.assertEqual(masked["Authorization"], ("•••••••• (hidden)", True))
        self.assertEqual(masked["Cookie"], ("•••••••• (hidden)", True))

        revealed = {k: v for k, v, _ in mask_headers(headers, reveal=True)}
        self.assertEqual(revealed["Authorization"], "Bearer supersecret")
        self.assertEqual(revealed["Cookie"], "session=abc123")

        self.assertEqual(
            sanitize_url_for_display("https://alice:topsecret@example.com:8443/file.bin"),
            "https://alice:••••@example.com:8443/file.bin",
        )
        self.assertEqual(parse_timestamp(""), "--")
        self.assertNotEqual(parse_timestamp("2026-11-10T10:15:30Z"), "--")

    def test_url_filename_destination_validation_and_actions(self):
        self.assertEqual(
            suggest_filename_from_url("https://example.com/files/Ubuntu%20Desktop.iso?mirror=1"),
            "Ubuntu Desktop.iso",
        )
        self.assertEqual(suggest_filename_from_url("https://example.com/"), "downloaded_file.bin")

        self.assertIsNone(validate_download_url("https://example.com/file.zip"))
        self.assertIsNotNone(validate_download_url(""))
        self.assertIsNotNone(validate_download_url("ftp://example.com/file.zip"))
        self.assertIsNotNone(validate_download_url("not-a-url"))

        self.assertIsNone(validate_filename("archive.tar.gz"))
        self.assertIsNotNone(validate_filename(""))
        self.assertIsNotNone(validate_filename(".."))
        self.assertIsNotNone(validate_filename("sub/file.zip"))
        self.assertIsNotNone(validate_filename("sub\\file.zip"))

        fake_settings = {
            "default_save_path": "~/Downloads",
            "categories": {
                "Video": {"extensions": ["mkv", "mp4"], "path": "~/Videos"},
                "Documents": {"extensions": ["pdf"], "path": "~/Documents"},
            },
        }
        self.assertEqual(
            resolve_category_destination("movie.mkv", fake_settings, "/tmp"),
            str(Path.home() / "Videos"),
        )
        self.assertEqual(
            resolve_category_destination("unknown.xyz", fake_settings, "/tmp"),
            str(Path.home() / "Downloads"),
        )

        # Capabilities matrix across all lifecycle states
        dl_caps = available_job_actions({"id": "1", "status": "DOWNLOADING", "save_path": "/a/b.zip", "chunks": [{"index": 0}]})
        self.assertTrue(dl_caps["can_pause"])
        self.assertTrue(dl_caps["can_cancel"])
        self.assertFalse(dl_caps["can_resume"])
        self.assertFalse(dl_caps["can_edit_destination"])

        paused_partial = available_job_actions({"id": "2", "status": "PAUSED", "save_path": "/a/b.zip", "chunks": [{"index": 0}]})
        self.assertTrue(paused_partial["can_resume"])
        self.assertTrue(paused_partial["can_edit_url"])
        self.assertFalse(paused_partial["can_edit_destination"])

        pending_caps = available_job_actions({"id": "3", "status": "PENDING_PATH", "save_path": "PENDING", "chunks": []})
        self.assertTrue(pending_caps["can_edit_destination"])
        self.assertTrue(pending_caps["can_cancel"])
        self.assertFalse(pending_caps["can_open_folder"])

        failed_caps = available_job_actions({"id": "4", "status": "FAILED", "save_path": "/a/b.zip", "chunks": []})
        self.assertTrue(failed_caps["can_retry"])
        self.assertTrue(failed_caps["can_edit_destination"])
        self.assertTrue(failed_caps["can_delete"])

        done_caps = available_job_actions({"id": "5", "status": "COMPLETED", "save_path": "/a/b.zip", "chunks": []})
        self.assertTrue(done_caps["can_delete"])
        self.assertTrue(done_caps["can_open_folder"])
        self.assertFalse(done_caps["can_cancel"])
        self.assertFalse(done_caps["can_edit_destination"])


class HydraGUIClientAndEntryTest(unittest.TestCase):
    def test_headless_probe_and_daemon_url_guard(self):
        info = probe_gui_backend(env={})
        self.assertFalse(info["available"])
        self.assertIn("DISPLAY", info["reason"])

        self.assertEqual(daemon_address_from_url("http://127.0.0.1:9000"), ("127.0.0.1", 9000))
        self.assertEqual(daemon_address_from_url("http://localhost:9000"), ("localhost", 9000))
        self.assertIsNone(daemon_address_from_url("https://evil.example.com:9000"))

    def test_token_resolution_rejects_symlinks(self):
        with tempfile.TemporaryDirectory() as tmp:
            cfg = Path(tmp) / "hydra"
            cfg.mkdir(parents=True)
            real = Path(tmp) / "real.secret"
            real.write_text("hijacked\n", encoding="utf-8")
            link = cfg / "api.token"
            os.symlink(real, link)
            self.assertEqual(resolve_token("", env={"XDG_CONFIG_HOME": tmp}), "")

    def test_client_and_sse_watcher_against_mock_daemon(self):
        sse_received = []
        sse_event = threading.Event()

        class MockDaemonHandler(BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                pass

            def do_GET(self):
                if self.path == "/api/settings":
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(json.dumps({"default_save_path": "~/Downloads", "categories": {}}).encode())
                    return

                if self.headers.get("X-Hydra-Token") != "gui-secret":
                    self.send_response(401)
                    self.end_headers()
                    return

                if self.path == "/api/jobs":
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(
                        json.dumps([{"id": "job_1", "file_name": "fedora.iso", "status": "DOWNLOADING", "progress": 33.3}]).encode()
                    )
                    return

                if self.path == "/api/events":
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.end_headers()
                    payload = json.dumps([{"id": "job_sse", "file_name": "arch.iso", "status": "COMPLETED", "progress": 100.0}])
                    self.wfile.write(f": ping\n\nevent: queue\ndata: {payload}\n\n".encode())
                    self.wfile.flush()
                    time.sleep(0.1)
                    return

                self.send_response(404)
                self.end_headers()

        server = HTTPServer(("127.0.0.1", 0), MockDaemonHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()

        watcher = None
        try:
            client = HydraGUIClient(f"http://127.0.0.1:{server.server_port}", token="gui-secret")
            settings = client.get_settings()
            self.assertIsNotNone(settings)
            assert settings is not None
            self.assertEqual(settings["default_save_path"], "~/Downloads")

            online, jobs, err = client.get_jobs()
            self.assertTrue(online)
            self.assertEqual(err, "")
            self.assertEqual(len(jobs), 1)
            self.assertEqual(jobs[0]["id"], "job_1")

            def on_sse(items):
                sse_received.append(items)
                sse_event.set()

            watcher = SSEWatcher(client, on_sse)
            watcher.start()
            self.assertTrue(sse_event.wait(timeout=3.0), "expected SSE queue payload")
            self.assertEqual(sse_received[0][0]["id"], "job_sse")
        finally:
            if watcher is not None:
                watcher.stop()
            server.shutdown()
            server.server_close()

    def test_client_actions_create_control_and_destination(self):
        calls = []

        class ActionMockHandler(BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                pass

            def _read_json(self):
                length = int(self.headers.get("Content-Length") or 0)
                if length <= 0:
                    return {}
                return json.loads(self.rfile.read(length).decode("utf-8"))

            def do_POST(self):
                if self.path == "/api/disk-space":
                    body = self._read_json()
                    calls.append(("POST", self.path, body))
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(json.dumps({"available_bytes": 1024 ** 3, "enough_space": True}).encode())
                    return

                if self.headers.get("X-Hydra-Token") != "gui-secret":
                    self.send_response(401)
                    self.end_headers()
                    return

                body = self._read_json()
                calls.append(("POST", self.path, body))
                self.send_response(202 if self.path == "/api/jobs" and "job_id" not in body else 200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                if self.path == "/api/jobs":
                    self.wfile.write(json.dumps({"status": "QUEUED", "job_id": body.get("job_id") or "job_new_1"}).encode())
                elif self.path == "/api/jobs/pause-all":
                    self.wfile.write(json.dumps({"status": "ok", "paused": 2}).encode())
                elif self.path == "/api/jobs/resume-all":
                    self.wfile.write(json.dumps({"status": "ok", "resumed": 3}).encode())
                else:
                    self.wfile.write(b'{"status":"ok"}')

            def do_DELETE(self):
                if self.headers.get("X-Hydra-Token") != "gui-secret":
                    self.send_response(401)
                    self.end_headers()
                    return
                calls.append(("DELETE", self.path, {}))
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"status":"ok"}')

        server = HTTPServer(("127.0.0.1", 0), ActionMockHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            client = HydraGUIClient(f"http://127.0.0.1:{server.server_port}", token="gui-secret")
            disk = client.check_disk_space("/tmp", 0)
            self.assertIsNotNone(disk)
            self.assertEqual(disk["available_bytes"], 1024 ** 3)

            ok, new_id, err = client.create_download(
                url="https://example.com/archive.zip",
                save_path="/tmp/archive.zip",
                filename="archive.zip",
                max_speed_bytes=512 * 1024,
            )
            self.assertTrue(ok, err)
            self.assertEqual(new_id, "job_new_1")

            self.assertEqual(client.update_job_destination("job_pending", "/home/u/Videos/clip.mkv", "clip.mkv"), (True, ""))
            self.assertEqual(client.update_job_url("job_pending", "https://new.example.com/clip.mkv"), (True, ""))
            self.assertEqual(client.pause_job("job_pending"), (True, ""))
            self.assertEqual(client.resume_job("job_pending"), (True, ""))
            self.assertEqual(client.delete_job("job_pending"), (True, ""))
            self.assertEqual(client.pause_all(), (True, 2, ""))
            self.assertEqual(client.resume_all(), (True, 3, ""))

            paths = [(m, p) for m, p, _ in calls]
            self.assertIn(("POST", "/api/jobs"), paths)
            self.assertIn(("POST", "/api/jobs/job_pending/url"), paths)
            self.assertIn(("POST", "/api/jobs/job_pending/pause"), paths)
            self.assertIn(("POST", "/api/jobs/job_pending/resume"), paths)
            self.assertIn(("DELETE", "/api/jobs/job_pending"), paths)
            self.assertIn(("POST", "/api/jobs/pause-all"), paths)
            self.assertIn(("POST", "/api/jobs/resume-all"), paths)
        finally:
            server.shutdown()
            server.server_close()

        # Verify dialogs module imports cleanly alongside GTK4 + Adw
        import importlib

        dialogs_mod = importlib.import_module("hydra_gui.dialogs")
        self.assertIsNotNone(dialogs_mod)
        self.assertTrue(hasattr(dialogs_mod, "NewDownloadDialog"))
        self.assertTrue(hasattr(dialogs_mod, "EditDestinationDialog"))
        self.assertTrue(hasattr(dialogs_mod, "UpdateURLDialog"))
        self.assertTrue(hasattr(dialogs_mod, "confirm_job_removal_dialog"))

    def test_entrypoint_check_and_job_list_item(self):
        import contextlib
        import io

        gui_entry = load_gui_entrypoint()
        old_display = os.environ.pop("DISPLAY", None)
        old_wayland = os.environ.pop("WAYLAND_DISPLAY", None)
        try:
            # Running without any display server must exit 1 cleanly via probe guard
            buf_out = io.StringIO()
            buf_err = io.StringIO()
            with contextlib.redirect_stdout(buf_out), contextlib.redirect_stderr(buf_err):
                rc = gui_entry.main(["--check"])
                rc_run = gui_entry.main([])
            self.assertEqual(rc, 1)
            self.assertEqual(rc_run, 1)
            self.assertFalse(json.loads(buf_out.getvalue())["available"])
        finally:
            if old_display is not None:
                os.environ["DISPLAY"] = old_display
            if old_wayland is not None:
                os.environ["WAYLAND_DISPLAY"] = old_wayland

        from hydra_gui.window import JobListItem

        emitted = []
        item = JobListItem(
            {"id": "j99", "file_name": "clip.mp4", "status": "DOWNLOADING", "progress": 10.0, "speed": "1.00 MB/s"},
            "Video",
            0,
        )
        item.connect("updated", lambda obj: emitted.append(obj.progress))
        struct_changed, prev = item.apply(
            {"id": "j99", "file_name": "clip.mp4", "status": "DOWNLOADING", "progress": 75.0, "speed": "2.00 MB/s"},
            "Video",
            0,
        )
        self.assertFalse(struct_changed)
        self.assertEqual(prev, "DOWNLOADING")
        self.assertEqual(emitted, [75.0])


if __name__ == "__main__":
    unittest.main()
