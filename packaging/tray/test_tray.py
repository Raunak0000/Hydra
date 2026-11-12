import importlib.machinery
import importlib.util
import json
import os
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path


def load_tray_module():
    tray_path = Path(__file__).resolve().parent / "hydra-tray"
    loader = importlib.machinery.SourceFileLoader("hydra_tray", str(tray_path))
    spec = importlib.util.spec_from_loader("hydra_tray", loader)
    assert spec is not None
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


tray = load_tray_module()


class TrayControllerLogicTest(unittest.TestCase):
    def test_headless_probe_reports_unavailable_cleanly(self):
        info = tray.probe_tray_backend(env={})
        self.assertFalse(info["available"])
        self.assertIsNone(info["backend"])
        self.assertIn("DISPLAY", info["reason"])

    def test_headless_autostart_exits_zero(self):
        old_env = os.environ.copy()
        try:
            os.environ.pop("DISPLAY", None)
            os.environ.pop("WAYLAND_DISPLAY", None)
            code = tray.main(["--autostart"])
            self.assertEqual(code, 0)
        finally:
            os.environ.clear()
            os.environ.update(old_env)

    def test_summarize_jobs_states(self):
        offline = tray.summarize_jobs([], online=False)
        self.assertEqual(offline["status_text"], "Engine: Offline")
        self.assertEqual(offline["badge"], "")

        downloading = tray.summarize_jobs(
            [
                {"id": "1", "status": "DOWNLOADING", "file_name": "ubuntu.iso", "progress": 42.5, "speed": "4.2 MB/s"},
                {"id": "2", "status": "QUEUED", "file_name": "notes.pdf"},
                {"id": "3", "status": "PAUSED", "file_name": "archive.tar.gz"},
                {"id": "4", "status": "COMPLETED", "file_name": "done.bin"},
            ],
            online=True,
        )
        self.assertEqual(downloading["active"], 1)
        self.assertEqual(downloading["queued"], 1)
        self.assertEqual(downloading["paused"], 1)
        self.assertEqual(downloading["completed"], 1)
        self.assertEqual(downloading["badge"], "1")
        self.assertIn("1 downloading", downloading["status_text"])
        self.assertEqual(len(downloading["actionable"]), 3)

    def test_terminal_command_builder_uses_matching_exec_flag(self):
        cmd = tray.build_terminal_command(
            ["/usr/bin/hydra-tui", "--ensure-daemon"],
            which_fn=lambda name: "/usr/bin/gnome-terminal" if name == "gnome-terminal" else None,
        )
        self.assertEqual(cmd, ["/usr/bin/gnome-terminal", "--", "/usr/bin/hydra-tui", "--ensure-daemon"])

        missing = tray.build_terminal_command(
            ["/usr/bin/hydra-tui"],
            which_fn=lambda _name: None,
        )
        self.assertIsNone(missing)

    def test_resolve_token_prefers_explicit_then_env_then_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            cfg_dir = Path(tmp) / "hydra"
            cfg_dir.mkdir(parents=True)
            token_path = cfg_dir / "api.token"
            token_path.write_text("file-token-123\n", encoding="utf-8")

            env = {"XDG_CONFIG_HOME": tmp}
            self.assertEqual(tray.resolve_token("", env=env), "file-token-123")
            self.assertEqual(tray.resolve_token("", env={"XDG_CONFIG_HOME": tmp, "HYDRA_API_TOKEN": "env-tok"}), "env-tok")
            self.assertEqual(tray.resolve_token("explicit-tok", env={"HYDRA_API_TOKEN": "env-tok"}), "explicit-tok")

    def test_hydra_client_actions_against_mock_daemon(self):
        requests_seen = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, format, *args):
                pass

            def do_GET(self):
                requests_seen.append(("GET", self.path, self.headers.get("X-Hydra-Token")))
                if self.path == "/api/jobs":
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.end_headers()
                    self.wfile.write(json.dumps([{"id": "job1", "status": "DOWNLOADING", "file_name": "a.zip"}]).encode())
                    return
                self.send_response(404)
                self.end_headers()

            def do_POST(self):
                requests_seen.append(("POST", self.path, self.headers.get("X-Hydra-Token")))
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"status":"ok"}')

        server = HTTPServer(("127.0.0.1", 0), Handler)
        t = threading.Thread(target=server.serve_forever, daemon=True)
        t.start()
        try:
            client = tray.HydraClient(f"http://127.0.0.1:{server.server_port}", token="tray-tok")
            online, jobs = client.get_jobs()
            self.assertTrue(online)
            self.assertEqual(len(jobs), 1)
            self.assertTrue(client.pause_all())
            self.assertTrue(client.resume_all())
            self.assertTrue(client.pause_job("job1"))
            self.assertTrue(client.resume_job("job1"))
        finally:
            server.shutdown()
            server.server_close()

        self.assertIn(("POST", "/api/jobs/pause-all", "tray-tok"), requests_seen)
        self.assertIn(("POST", "/api/jobs/resume-all", "tray-tok"), requests_seen)
        self.assertIn(("POST", "/api/jobs/job1/pause", "tray-tok"), requests_seen)
        self.assertIn(("POST", "/api/jobs/job1/resume", "tray-tok"), requests_seen)

    def test_resolve_icon_paths_returns_svg_parent_for_hicolor_layout(self):
        with tempfile.TemporaryDirectory() as tmp:
            svg_dir = Path(tmp) / "icons" / "hicolor" / "scalable" / "apps"
            svg_dir.mkdir(parents=True)
            svg_file = svg_dir / "hydra.svg"
            svg_file.write_text("<svg xmlns='http://www.w3.org/2000/svg' width='64' height='64'></svg>", encoding="utf-8")

            icon_name, icon_svg, theme_dir = tray.resolve_icon_paths(env={"XDG_DATA_HOME": tmp})
            self.assertEqual(icon_name, "hydra")
            self.assertEqual(icon_svg, str(svg_file))
            self.assertEqual(theme_dir, str(svg_dir))

            try:
                import gi

                gi.require_version("Gtk", "3.0")
                from gi.repository import Gtk

                theme = Gtk.IconTheme.new()
                theme.set_search_path([theme_dir])
                info = theme.lookup_icon(icon_name, 24, 0)
                self.assertIsNotNone(info)
                assert info is not None
                self.assertEqual(info.get_filename(), str(svg_file))
            except (ImportError, ValueError):
                pass


if __name__ == "__main__":
    unittest.main()

