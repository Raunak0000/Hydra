"""HTTP API client, SSE event stream watcher, and environment probes for Hydra GUI."""

from __future__ import annotations

import json
import os
import shutil
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional, Tuple
from urllib.parse import quote, urlparse


DEFAULT_DAEMON_URL = "http://127.0.0.1:9000"


def has_graphical_display(env: Optional[Dict[str, str]] = None) -> bool:
    source = env if env is not None else os.environ
    return bool(source.get("DISPLAY") or source.get("WAYLAND_DISPLAY"))


def probe_gui_backend(
    env: Optional[Dict[str, str]] = None,
    verify_display: bool = False,
) -> Dict[str, Any]:
    """Detect whether a desktop session and GTK 4 + Libadwaita bindings are available."""
    if not has_graphical_display(env):
        return {
            "available": False,
            "backend": None,
            "reason": "no graphical session (DISPLAY and WAYLAND_DISPLAY are unset)",
        }

    try:
        import gi  # type: ignore
    except ImportError:
        return {
            "available": False,
            "backend": None,
            "reason": "PyGObject (python3-gi) is not installed",
        }

    try:
        gi.require_version("Gtk", "4.0")
        gi.require_version("Adw", "1")
        from gi.repository import Adw, Gdk, Gtk  # type: ignore
    except (ImportError, ValueError) as exc:
        return {
            "available": False,
            "backend": None,
            "reason": f"GTK 4.0 / Libadwaita bindings unavailable: {exc}",
        }

    if verify_display:
        Adw.init()
        if Gdk.Display.get_default() is None:
            return {
                "available": False,
                "backend": None,
                "reason": "display server connection failed (Gdk.Display is None)",
            }

    return {
        "available": True,
        "backend": f"Gtk {Gtk.get_major_version()}.{Gtk.get_minor_version()} + Adw {Adw.get_major_version()}.{Adw.get_minor_version()}",
        "reason": "ok",
    }


def resolve_config_dir(env: Optional[Dict[str, str]] = None) -> Path:
    source = env if env is not None else os.environ
    cfg_home = source.get("XDG_CONFIG_HOME")
    if cfg_home:
        return Path(cfg_home) / "hydra"
    return Path.home() / ".config" / "hydra"


def resolve_token(explicit_token: str = "", env: Optional[Dict[str, str]] = None) -> str:
    if explicit_token.strip():
        return explicit_token.strip()

    source = env if env is not None else os.environ
    env_tok = source.get("HYDRA_API_TOKEN", "").strip()
    if env_tok:
        return env_tok

    token_file = resolve_config_dir(source) / "api.token"
    try:
        if token_file.is_symlink():
            return ""
        if token_file.is_file():
            return token_file.read_text(encoding="utf-8").strip()
    except OSError:
        return ""
    return ""


def expand_path(raw_path: str) -> str:
    if not raw_path:
        return ""
    if raw_path == "~":
        return str(Path.home())
    if raw_path.startswith("~/"):
        return str(Path.home() / raw_path[2:])
    return raw_path


def resolve_downloads_dir(
    client: Optional["HydraGUIClient"] = None,
    env: Optional[Dict[str, str]] = None,
) -> Path:
    source = env if env is not None else os.environ

    if client is not None:
        settings = client.get_settings()
        if settings and settings.get("default_save_path"):
            candidate = Path(expand_path(str(settings["default_save_path"])))
            if candidate.is_dir():
                return candidate

    cfg_file = resolve_config_dir(source) / "config.json"
    try:
        if cfg_file.is_file():
            data = json.loads(cfg_file.read_text(encoding="utf-8"))
            raw = str(data.get("default_save_path", "")).strip()
            if raw:
                candidate = Path(expand_path(raw))
                if candidate.is_dir():
                    return candidate
    except (OSError, ValueError):
        pass

    xdg_dl = source.get("XDG_DOWNLOAD_DIR", "").strip()
    if xdg_dl and Path(xdg_dl).is_dir():
        return Path(xdg_dl)

    default_dl = Path.home() / "Downloads"
    if default_dl.is_dir():
        return default_dl

    return Path.home()


def resolve_icon_paths(
    anchor_path: Optional[Path] = None,
    env: Optional[Dict[str, str]] = None,
) -> Tuple[str, Optional[str]]:
    """Return (icon_name, svg_file_path_or_none)."""
    source = env if env is not None else os.environ
    data_home = source.get("XDG_DATA_HOME") or str(Path.home() / ".local" / "share")

    candidates = [
        Path(data_home) / "icons" / "hicolor" / "scalable" / "apps" / "hydra.svg",
        Path.home() / ".local" / "share" / "icons" / "hicolor" / "scalable" / "apps" / "hydra.svg",
        Path("/usr/share/icons/hicolor/scalable/apps/hydra.svg"),
    ]
    if anchor_path is not None:
        resolved = anchor_path.resolve()
        for parent in resolved.parents:
            candidate = parent / "icons" / "hydra.svg"
            if candidate.is_file():
                candidates.append(candidate)
                break
            pkg_candidate = parent / "packaging" / "icons" / "hydra.svg"
            if pkg_candidate.is_file():
                candidates.append(pkg_candidate)
                break

    for svg in candidates:
        try:
            if svg.is_file():
                return ("hydra", str(svg))
        except OSError:
            continue
    return ("hydra", None)


def daemon_address_from_url(raw_url: str) -> Optional[Tuple[str, int]]:
    try:
        parsed = urlparse(raw_url.strip())
    except ValueError:
        return None
    if not parsed.netloc or not parsed.hostname:
        return None
    host = parsed.hostname.lower()
    if host not in {"localhost", "127.0.0.1", "::1"}:
        return None
    port = parsed.port
    if port is None:
        port = 443 if parsed.scheme == "https" else 80
    return (parsed.hostname, port)


def is_tcp_reachable(host: str, port: int, timeout: float = 0.25) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def find_daemon_binary(anchor_path: Optional[Path] = None) -> Optional[str]:
    if anchor_path is not None:
        resolved = anchor_path.resolve()
        candidates = [
            resolved.parent / "hydra-daemon",
            resolved.parent.parent / "bin" / "hydra-daemon",
            resolved.parent.parent.parent / "bin" / "hydra-daemon",
        ]
        for candidate in candidates:
            try:
                if candidate.is_file() and os.access(candidate, os.X_OK):
                    return str(candidate)
            except OSError:
                continue
    return shutil.which("hydra-daemon")


def systemd_user_available() -> bool:
    if not shutil.which("systemctl"):
        return False
    try:
        res = subprocess.run(
            ["systemctl", "--user", "show-environment"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=2,
        )
        return res.returncode == 0
    except (OSError, subprocess.SubprocessError):
        return False


def ensure_local_daemon(
    daemon_url: str,
    anchor_path: Optional[Path] = None,
    timeout: float = 3.0,
) -> bool:
    """Ensure local hydra-daemon is listening and provisioned; returns reachability."""
    addr = daemon_address_from_url(daemon_url)
    if addr is None:
        return False
    host, port = addr

    if is_tcp_reachable(host, port) and resolve_token():
        return True

    started_via_systemd = False
    if systemd_user_available():
        try:
            res = subprocess.run(
                ["systemctl", "--user", "start", "hydra.service"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=4,
            )
            started_via_systemd = res.returncode == 0
        except (OSError, subprocess.SubprocessError):
            started_via_systemd = False

    if not started_via_systemd:
        bin_path = find_daemon_binary(anchor_path)
        if bin_path:
            try:
                subprocess.Popen(
                    [bin_path, "--autostart"],
                    stdin=subprocess.DEVNULL,
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                    start_new_session=True,
                )
            except OSError:
                pass

    deadline = time.monotonic() + max(0.5, timeout)
    while time.monotonic() < deadline:
        if is_tcp_reachable(host, port) and resolve_token():
            return True
        time.sleep(0.1)

    return is_tcp_reachable(host, port)


def open_path_in_desktop(target_path: str) -> bool:
    """Open an existing file or folder via xdg-open cleanly without blocking."""
    cleaned = expand_path((target_path or "").strip())
    if not cleaned or cleaned in {"PENDING", "DEFAULT"}:
        return False

    path_obj = Path(cleaned)
    target = str(path_obj if path_obj.is_dir() else path_obj.parent)
    if not os.path.isdir(target):
        return False

    xdg_open = shutil.which("xdg-open")
    if not xdg_open:
        return False

    try:
        subprocess.Popen(
            [xdg_open, target],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        return True
    except OSError:
        return False


class HydraGUIClient:
    """Local HTTP + SSE client communicating with hydra-daemon."""

    def __init__(self, base_url: str = DEFAULT_DAEMON_URL, token: str = "") -> None:
        self.base_url = base_url.rstrip("/")
        self.explicit_token = token

    def current_token(self) -> str:
        return resolve_token(self.explicit_token)

    def _request(
        self,
        method: str,
        path: str,
        require_auth: bool = True,
        timeout: float = 2.0,
        body: Optional[Dict[str, Any]] = None,
    ) -> Tuple[bool, Any, str]:
        headers = {"User-Agent": "Hydra-GUI/1.0"}
        if require_auth:
            tok = self.current_token()
            if not tok:
                return False, None, "missing Hydra API token"
            headers["X-Hydra-Token"] = tok

        raw_data = None
        if body is not None:
            raw_data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"

        req = urllib.request.Request(
            self.base_url + path,
            data=raw_data,
            method=method,
            headers=headers,
        )
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                raw = resp.read().decode("utf-8")
                return True, (json.loads(raw) if raw else {}), ""
        except urllib.error.HTTPError as exc:
            detail = f"HTTP {exc.code}"
            try:
                err_body = exc.read().decode("utf-8")
                parsed = json.loads(err_body)
                if isinstance(parsed, dict) and parsed.get("error"):
                    detail = str(parsed["error"])
            except Exception:
                pass
            return False, None, detail
        except (urllib.error.URLError, TimeoutError, OSError, ValueError) as exc:
            return False, None, str(exc)

    def get_jobs(self) -> Tuple[bool, List[Dict[str, Any]], str]:
        ok, payload, err = self._request("GET", "/api/jobs", require_auth=True, timeout=2.5)
        if not ok:
            return False, [], err
        if not isinstance(payload, list):
            return False, [], "unexpected /api/jobs response payload"
        return True, payload, ""

    def get_settings(self) -> Optional[Dict[str, Any]]:
        ok, payload, _ = self._request("GET", "/api/settings", require_auth=False, timeout=1.5)
        if ok and isinstance(payload, dict):
            return payload
        return None

    def check_disk_space(self, path: str, required_bytes: int = 0) -> Optional[Dict[str, Any]]:
        ok, payload, _ = self._request(
            "POST",
            "/api/disk-space",
            require_auth=False,
            timeout=1.5,
            body={"path": path, "required_bytes": int(required_bytes)},
        )
        if ok and isinstance(payload, dict):
            return payload
        return None

    def create_download(
        self,
        url: str,
        save_path: str,
        filename: str = "",
        headers: Optional[Dict[str, str]] = None,
        max_speed_bytes: int = 0,
        expected_checksum: str = "",
        checksum_algo: str = "",
        scheduled_at: str = "",
    ) -> Tuple[bool, Optional[str], str]:
        body: Dict[str, Any] = {
            "url": url.strip(),
            "save_path": save_path.strip(),
            "headers": headers or {},
        }
        if filename.strip():
            body["filename"] = filename.strip()
        if max_speed_bytes > 0:
            body["max_speed_bytes"] = int(max_speed_bytes)
        if expected_checksum.strip():
            body["expected_checksum"] = expected_checksum.strip()
        if checksum_algo.strip():
            body["checksum_algo"] = checksum_algo.strip()
        if scheduled_at.strip():
            body["scheduled_at"] = scheduled_at.strip()

        ok, payload, err = self._request("POST", "/api/jobs", require_auth=True, timeout=4.0, body=body)
        if not ok:
            return False, None, err
        job_id = str(payload.get("job_id") or "") if isinstance(payload, dict) else ""
        return True, job_id or None, ""

    def update_job_destination(
        self,
        job_id: str,
        save_path: str,
        filename: str = "",
        scheduled_at: str = "",
    ) -> Tuple[bool, str]:
        body: Dict[str, Any] = {
            "job_id": job_id,
            "save_path": save_path.strip(),
        }
        if filename.strip():
            body["filename"] = filename.strip()
        if scheduled_at.strip():
            body["scheduled_at"] = scheduled_at.strip()

        ok, _, err = self._request("POST", "/api/jobs", require_auth=True, timeout=4.0, body=body)
        return ok, err

    def update_job_url(self, job_id: str, new_url: str) -> Tuple[bool, str]:
        safe_id = quote(job_id, safe="")
        ok, _, err = self._request(
            "POST",
            f"/api/jobs/{safe_id}/url",
            require_auth=True,
            timeout=3.0,
            body={"url": new_url.strip()},
        )
        return ok, err

    def pause_job(self, job_id: str) -> Tuple[bool, str]:
        safe_id = quote(job_id, safe="")
        ok, _, err = self._request("POST", f"/api/jobs/{safe_id}/pause", require_auth=True, timeout=3.0)
        return ok, err

    def resume_job(self, job_id: str) -> Tuple[bool, str]:
        safe_id = quote(job_id, safe="")
        ok, _, err = self._request("POST", f"/api/jobs/{safe_id}/resume", require_auth=True, timeout=3.0)
        return ok, err

    def delete_job(self, job_id: str) -> Tuple[bool, str]:
        safe_id = quote(job_id, safe="")
        ok, _, err = self._request("DELETE", f"/api/jobs/{safe_id}", require_auth=True, timeout=3.0)
        return ok, err

    def pause_all(self) -> Tuple[bool, int, str]:
        ok, payload, err = self._request("POST", "/api/jobs/pause-all", require_auth=True, timeout=3.0)
        if not ok:
            return False, 0, err
        count = int(payload.get("paused") or 0) if isinstance(payload, dict) else 0
        return True, count, ""

    def resume_all(self) -> Tuple[bool, int, str]:
        ok, payload, err = self._request("POST", "/api/jobs/resume-all", require_auth=True, timeout=3.0)
        if not ok:
            return False, 0, err
        count = int(payload.get("resumed") or 0) if isinstance(payload, dict) else 0
        return True, count, ""


class SSEWatcher:
    """Background Server-Sent Events listener subscribing to /api/events."""

    def __init__(
        self,
        client: HydraGUIClient,
        on_jobs: Callable[[List[Dict[str, Any]]], None],
    ) -> None:
        self.client = client
        self.on_jobs = on_jobs
        self._stop_event = threading.Event()
        self._thread: Optional[threading.Thread] = None

    def start(self) -> None:
        if self._thread is not None and self._thread.is_alive():
            return
        self._stop_event.clear()
        self._thread = threading.Thread(target=self._run, name="hydra-sse-watcher", daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop_event.set()

    def _run(self) -> None:
        backoff = 1.0
        while not self._stop_event.is_set():
            token = self.client.current_token()
            if not token:
                self._stop_event.wait(1.5)
                continue

            req = urllib.request.Request(
                self.client.base_url + "/api/events",
                method="GET",
                headers={
                    "Accept": "text/event-stream",
                    "X-Hydra-Token": token,
                    "User-Agent": "Hydra-GUI/1.0",
                },
            )
            try:
                with urllib.request.urlopen(req, timeout=30.0) as resp:
                    backoff = 1.0
                    event_name = ""
                    data_lines: List[str] = []

                    while not self._stop_event.is_set():
                        raw_line = resp.readline()
                        if not raw_line:
                            break
                        line = raw_line.decode("utf-8", errors="replace").rstrip("\r\n")

                        if line == "":
                            if data_lines and event_name in {"", "queue"}:
                                joined = "\n".join(data_lines)
                                try:
                                    payload = json.loads(joined)
                                    if isinstance(payload, list):
                                        self.on_jobs(payload)
                                except ValueError:
                                    pass
                            event_name = ""
                            data_lines = []
                            continue

                        if line.startswith(":"):
                            continue
                        if line.startswith("event:"):
                            event_name = line[6:].strip()
                        elif line.startswith("data:"):
                            data_lines.append(line[5:].strip())
            except Exception:
                pass

            if self._stop_event.is_set():
                break
            self._stop_event.wait(backoff)
            backoff = min(5.0, backoff * 1.5)

