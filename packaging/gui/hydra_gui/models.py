"""Pure data models, filtering, sorting, and telemetry helpers for Hydra GUI."""

from __future__ import annotations

import os
import re
from collections import deque
from datetime import datetime
from pathlib import Path
from typing import Any, Deque, Dict, Iterable, List, Optional, Tuple
from urllib.parse import unquote, urlparse


DEFAULT_CATEGORIES: Dict[str, List[str]] = {
    "Video": ["mp4", "mkv", "avi", "mov", "flv", "webm", "wmv", "m4v", "mpg", "mpeg"],
    "Music": ["mp3", "flac", "wav", "aac", "ogg", "m4a", "opus", "wma"],
    "Compressed": ["zip", "tar", "gz", "bz2", "xz", "7z", "rar", "iso", "img", "bin"],
    "Programs": ["deb", "rpm", "appimage", "apk", "xapk", "snap", "exe", "msi"],
    "Documents": ["pdf", "epub", "djvu", "doc", "docx", "xls", "xlsx"],
}

CATEGORY_ORDER: Tuple[str, ...] = (
    "Video",
    "Music",
    "Compressed",
    "Programs",
    "Documents",
    "Other",
)

STATUS_FILTERS: Tuple[Tuple[str, str, str], ...] = (
    ("all", "All Downloads", "view-list-symbolic"),
    ("active", "Active & Queued", "folder-download-symbolic"),
    ("paused", "Paused", "media-playback-pause-symbolic"),
    ("scheduled", "Scheduled", "alarm-symbolic"),
    ("completed", "Completed", "emblem-ok-symbolic"),
    ("failed", "Failed", "dialog-error-symbolic"),
)

STATUS_META: Dict[str, Dict[str, Any]] = {
    "DOWNLOADING": {
        "label": "Downloading",
        "css_class": "status-downloading",
        "progress_css": "progress-active",
        "icon": "folder-download-symbolic",
        "rank": 0,
    },
    "QUEUED": {
        "label": "Queued",
        "css_class": "status-queued",
        "progress_css": "progress-queued",
        "icon": "content-loading-symbolic",
        "rank": 1,
    },
    "SCHEDULED": {
        "label": "Scheduled",
        "css_class": "status-scheduled",
        "progress_css": "progress-queued",
        "icon": "alarm-symbolic",
        "rank": 2,
    },
    "PENDING_PATH": {
        "label": "Needs Folder",
        "css_class": "status-pending",
        "progress_css": "progress-paused",
        "icon": "folder-new-symbolic",
        "rank": 3,
    },
    "PAUSED": {
        "label": "Paused",
        "css_class": "status-paused",
        "progress_css": "progress-paused",
        "icon": "media-playback-pause-symbolic",
        "rank": 4,
    },
    "FAILED": {
        "label": "Failed",
        "css_class": "status-failed",
        "progress_css": "progress-failed",
        "icon": "dialog-error-symbolic",
        "rank": 5,
    },
    "COMPLETED": {
        "label": "Completed",
        "css_class": "status-completed",
        "progress_css": "progress-completed",
        "icon": "emblem-ok-symbolic",
        "rank": 6,
    },
}

CATEGORY_ICONS: Dict[str, str] = {
    "Video": "video-x-generic-symbolic",
    "Music": "audio-x-generic-symbolic",
    "Compressed": "package-x-generic-symbolic",
    "Programs": "application-x-executable-symbolic",
    "Documents": "x-office-document-symbolic",
    "Other": "text-x-generic-symbolic",
}

SENSITIVE_HEADER_NAMES = frozenset(
    {
        "authorization",
        "proxy-authorization",
        "cookie",
        "set-cookie",
        "x-hydra-token",
        "x-api-key",
        "api-key",
    }
)

_UNIT_MULTIPLIERS: Dict[str, int] = {
    "B": 1,
    "KB": 1024,
    "KIB": 1024,
    "MB": 1024 ** 2,
    "MIB": 1024 ** 2,
    "GB": 1024 ** 3,
    "GIB": 1024 ** 3,
    "TB": 1024 ** 4,
    "TIB": 1024 ** 4,
}

_SIZE_PATTERN = re.compile(r"^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]+)(?:/s)?\s*$")


def parse_byte_string(text: Any) -> int:
    """Parse human-readable byte sizes like '12.50 MB' into integer bytes."""
    if text is None:
        return 0
    if isinstance(text, (int, float)):
        return max(0, int(text))
    raw = str(text).strip()
    if not raw or raw in {"Calculating...", "Unknown Size", "--", "Pending path..."}:
        return 0
    match = _SIZE_PATTERN.match(raw)
    if not match:
        return 0
    val = float(match.group(1))
    unit = match.group(2).upper()
    mult = _UNIT_MULTIPLIERS.get(unit)
    if mult is None:
        return 0
    return max(0, int(val * mult))


def parse_speed_bps(text: Any) -> float:
    """Parse speed strings like '8.45 MB/s' or '512.00 KB/s' into bytes/second."""
    if text is None:
        return 0.0
    if isinstance(text, (int, float)):
        return max(0.0, float(text))
    raw = str(text).strip()
    if not raw or raw == "--":
        return 0.0
    if raw.lower().endswith("/s"):
        raw = raw[:-2].strip()
    match = _SIZE_PATTERN.match(raw)
    if not match:
        return 0.0
    val = float(match.group(1))
    unit = match.group(2).upper()
    mult = _UNIT_MULTIPLIERS.get(unit)
    if mult is None:
        return 0.0
    return max(0.0, val * mult)


def format_bytes(num_bytes: int) -> str:
    """Format byte counts matching Hydra daemon conventions."""
    if num_bytes < 0:
        num_bytes = 0
    if num_bytes >= 1024 ** 3:
        return f"{num_bytes / (1024 ** 3):.2f} GB"
    if num_bytes >= 1024 ** 2:
        return f"{num_bytes / (1024 ** 2):.2f} MB"
    if num_bytes >= 1024:
        return f"{num_bytes / 1024:.2f} KB"
    return f"{int(num_bytes)} B"


def format_speed(bytes_per_sec: float) -> str:
    """Format transfer rate in bytes/sec as human-readable string."""
    if bytes_per_sec <= 0:
        return "0.00 KB/s"
    if bytes_per_sec >= 1024 ** 3:
        return f"{bytes_per_sec / (1024 ** 3):.2f} GB/s"
    if bytes_per_sec >= 1024 ** 2:
        return f"{bytes_per_sec / (1024 ** 2):.2f} MB/s"
    if bytes_per_sec >= 1024:
        return f"{bytes_per_sec / 1024:.2f} KB/s"
    return f"{bytes_per_sec:.2f} B/s"


def effective_total_size_str(job: Dict[str, Any]) -> str:
    """Resolve display total size string, falling back to chunk ends or completed downloaded bytes."""
    raw_total = str(job.get("total_size") or "").strip()
    if raw_total and raw_total not in {"Calculating...", "Unknown Size", "--", "Pending path..."}:
        return raw_total

    chunks = job.get("chunks")
    if isinstance(chunks, list) and chunks:
        max_end = -1
        for ch in chunks:
            if isinstance(ch, dict):
                try:
                    end_val = int(ch.get("end", -1))
                except (TypeError, ValueError):
                    end_val = -1
                if end_val > max_end:
                    max_end = end_val
        if max_end >= 0:
            return format_bytes(max_end + 1)

    status = str(job.get("status") or "").strip().upper()
    downloaded = str(job.get("downloaded") or "").strip()
    if status == "COMPLETED" and downloaded and downloaded not in {"0 B", "--"}:
        return downloaded

    return raw_total or "Calculating..."


def display_name_for_job(job: Dict[str, Any]) -> str:
    """Resolve user-friendly filename matching Hydra TUI resolution rules."""
    save_path = str(job.get("save_path") or "").strip()
    file_name = str(job.get("file_name") or "").strip()
    url = str(job.get("url") or "").strip()

    if save_path and save_path not in {"PENDING", "DEFAULT"} and not save_path.endswith("/"):
        base = os.path.basename(save_path)
        if base:
            return base

    if file_name and file_name not in {"Calculating...", "Pending path..."}:
        return file_name

    if url:
        parsed = urlparse(url)
        candidate = os.path.basename(parsed.path.rstrip("/"))
        if candidate:
            return candidate

    return file_name or str(job.get("id") or "Download")


def infer_category(
    file_name: str,
    save_path: str = "",
    custom_categories: Optional[Dict[str, Any]] = None,
) -> str:
    """Determine Hydra download category from filename or save_path extension."""
    target = file_name or ""
    if save_path and save_path not in {"PENDING", "DEFAULT"} and not save_path.endswith("/"):
        base = os.path.basename(save_path)
        if base:
            target = base

    ext = Path(target).suffix.lstrip(".").lower()
    if not ext:
        return "Other"

    if isinstance(custom_categories, dict) and custom_categories:
        for cat_name, rule in custom_categories.items():
            exts: Iterable[str] = []
            if isinstance(rule, dict):
                exts = rule.get("extensions") or []
            elif isinstance(rule, list):
                exts = rule
            if any(str(e).lstrip(".").lower() == ext for e in exts):
                return str(cat_name)

    for cat_name, exts in DEFAULT_CATEGORIES.items():
        if ext in exts:
            return cat_name

    return "Other"


def category_icon_name(category: str) -> str:
    return CATEGORY_ICONS.get(category, "text-x-generic-symbolic")


def status_info(status: str) -> Dict[str, Any]:
    norm = str(status or "").upper()
    return STATUS_META.get(
        norm,
        {
            "label": norm.title() if norm else "Unknown",
            "css_class": "status-paused",
            "progress_css": "progress-paused",
            "icon": "dialog-information-symbolic",
            "rank": 99,
        },
    )


def url_host(url: str) -> str:
    if not url:
        return ""
    try:
        parsed = urlparse(url)
        return parsed.hostname or ""
    except ValueError:
        return ""


def sanitize_url_for_display(url: str) -> str:
    """Mask embedded userinfo password in URLs while preserving host and path."""
    if not url or "@" not in url:
        return url
    try:
        parsed = urlparse(url)
        if parsed.password:
            user = parsed.username or ""
            netloc = f"{user}:••••@{parsed.hostname or ''}"
            if parsed.port:
                netloc += f":{parsed.port}"
            return parsed._replace(netloc=netloc).geturl()
    except ValueError:
        pass
    return url


def mask_headers(headers: Optional[Dict[str, Any]], reveal: bool = False) -> List[Tuple[str, str, bool]]:
    """Return sorted (key, display_value, is_sensitive) pairs for request headers."""
    if not isinstance(headers, dict) or not headers:
        return []
    result: List[Tuple[str, str, bool]] = []
    for key in sorted(headers.keys(), key=lambda s: s.lower()):
        val = str(headers.get(key) or "")
        sensitive = key.strip().lower() in SENSITIVE_HEADER_NAMES
        if sensitive and not reveal and val:
            display_val = "•••••••• (hidden)"
        else:
            display_val = val
        result.append((str(key), display_val, sensitive))
    return result


def parse_timestamp(raw: Any) -> str:
    """Format RFC3339 or SQLite datetime strings cleanly for UI display."""
    if not raw:
        return "--"
    text = str(raw).strip()
    if not text or text.startswith("0001-01-01"):
        return "--"

    # Python 3.11+ handles trailing Z via fromisoformat, strip subseconds nicely too.
    try:
        dt = datetime.fromisoformat(text.replace("Z", "+00:00"))
        if dt.tzinfo is not None:
            dt = dt.astimezone()
        return dt.strftime("%Y-%m-%d %H:%M:%S")
    except ValueError:
        return text


def compute_chunk_stats(
    chunks: Optional[List[Dict[str, Any]]],
    job_status: str = "",
    job_progress: float = 0.0,
) -> Dict[str, Any]:
    """Normalize per-chunk byte ranges and calculate aggregate segment telemetry."""
    if not chunks:
        return {
            "has_chunks": False,
            "total_chunks": 0,
            "completed_chunks": 0,
            "active_chunks": 0,
            "pending_chunks": 0,
            "total_bytes": 0,
            "downloaded_bytes": 0,
            "overall_fraction": max(0.0, min(1.0, float(job_progress or 0.0) / 100.0)),
            "segments": [],
        }

    ordered = sorted(chunks, key=lambda c: int(c.get("index", 0)))
    segments: List[Dict[str, Any]] = []
    total_bytes = 0
    downloaded_bytes = 0
    completed_count = 0
    active_count = 0
    pending_count = 0
    norm_status = str(job_status or "").upper()

    for pos, ch in enumerate(ordered):
        idx = int(ch.get("index", pos))
        start = max(0, int(ch.get("start", 0) or 0))
        end = int(ch.get("end", -1) if ch.get("end") is not None else -1)
        current = max(start, int(ch.get("current_offset", start) or start))
        completed = bool(ch.get("completed", False))

        if end >= start:
            length = (end - start) + 1
            if completed or norm_status == "COMPLETED":
                completed = True
                current = end + 1
            done_bytes = max(0, min(length, current - start))
            fraction = done_bytes / length if length > 0 else 1.0
            range_label = f"{format_bytes(start)} – {format_bytes(end + 1)}"
        else:
            # Unknown Content-Length single-stream download (end == -1)
            length = 0
            done_bytes = max(0, current - start)
            fraction = 1.0 if completed or norm_status == "COMPLETED" else 0.0
            range_label = "Streaming (unknown total size)"

        if completed:
            seg_state = "completed"
            completed_count += 1
        elif done_bytes > 0 or (norm_status == "DOWNLOADING" and end < 0):
            seg_state = "active"
            active_count += 1
        else:
            seg_state = "pending"
            pending_count += 1

        total_bytes += length
        downloaded_bytes += done_bytes

        segments.append(
            {
                "index": idx,
                "start": start,
                "end": end,
                "current_offset": current,
                "length": length,
                "downloaded": done_bytes,
                "fraction": max(0.0, min(1.0, fraction)),
                "completed": completed,
                "state": seg_state,
                "byte_range_label": range_label,
                "progress_label": (
                    f"{done_bytes / length * 100:.1f}%"
                    if length > 0
                    else (format_bytes(done_bytes) if done_bytes > 0 else "0%")
                ),
            }
        )

    if total_bytes > 0:
        overall_fraction = max(0.0, min(1.0, downloaded_bytes / total_bytes))
    else:
        overall_fraction = max(0.0, min(1.0, float(job_progress or 0.0) / 100.0))

    return {
        "has_chunks": True,
        "total_chunks": len(segments),
        "completed_chunks": completed_count,
        "active_chunks": active_count,
        "pending_chunks": pending_count,
        "total_bytes": total_bytes,
        "downloaded_bytes": downloaded_bytes,
        "overall_fraction": overall_fraction,
        "segments": segments,
    }


def matches_status_filter(status: str, status_filter: str) -> bool:
    norm = str(status or "").upper()
    flt = (status_filter or "all").lower()
    if flt == "all":
        return True
    if flt == "active":
        return norm in {"DOWNLOADING", "QUEUED", "PENDING_PATH"}
    if flt == "downloading":
        return norm == "DOWNLOADING"
    if flt == "queued":
        return norm == "QUEUED"
    if flt == "paused":
        return norm == "PAUSED"
    if flt == "scheduled":
        return norm == "SCHEDULED"
    if flt == "completed":
        return norm == "COMPLETED"
    if flt == "failed":
        return norm == "FAILED"
    return True


def filter_jobs(
    jobs: List[Dict[str, Any]],
    status_filter: str = "all",
    category_filter: str = "all",
    search_query: str = "",
    custom_categories: Optional[Dict[str, Any]] = None,
) -> List[Dict[str, Any]]:
    query = (search_query or "").strip().lower()
    cat_flt = (category_filter or "all").strip()
    out: List[Dict[str, Any]] = []

    for job in jobs:
        status = str(job.get("status") or "").upper()
        if not matches_status_filter(status, status_filter):
            continue

        name = display_name_for_job(job)
        save_path = str(job.get("save_path") or "")
        cat = infer_category(name, save_path, custom_categories)
        if cat_flt != "all" and cat != cat_flt:
            continue

        if query:
            haystack = " ".join(
                [
                    name,
                    str(job.get("file_name") or ""),
                    str(job.get("url") or ""),
                    save_path,
                    str(job.get("batch_id") or ""),
                    str(job.get("id") or ""),
                    status,
                    cat,
                ]
            ).lower()
            if query not in haystack:
                continue

        out.append(job)

    return out


def sort_jobs(
    jobs: List[Dict[str, Any]],
    sort_by: str = "added",
    descending: bool = True,
) -> List[Dict[str, Any]]:
    """Return jobs sorted by selected criterion without mutating input list."""
    mode = (sort_by or "added").lower()

    if mode == "added":
        # Preserves natural daemon insertion ordering cleanly (oldest->newest or newest->oldest).
        indexed = list(enumerate(jobs))
        indexed.sort(
            key=lambda pair: (str(pair[1].get("created_at") or ""), pair[0]),
            reverse=descending,
        )
        return [pair[1] for pair in indexed]

    def key_fn(job: Dict[str, Any]) -> Any:
        if mode == "name":
            return display_name_for_job(job).lower()
        if mode == "progress":
            return float(job.get("progress") or 0.0)
        if mode == "speed":
            return parse_speed_bps(job.get("speed"))
        if mode == "size":
            return parse_byte_string(effective_total_size_str(job))
        if mode == "status":
            rank = status_info(str(job.get("status") or "")).get("rank", 99)
            return (rank, display_name_for_job(job).lower())
        return display_name_for_job(job).lower()

    # For status sort, ascending puts DOWNLOADING first; respect descending flag cleanly.
    return sorted(jobs, key=key_fn, reverse=descending)


def compute_dashboard_stats(
    jobs: List[Dict[str, Any]],
    custom_categories: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """Compute counts per status, category, and total active bandwidth."""
    status_counts = {
        "all": len(jobs),
        "active": 0,
        "paused": 0,
        "scheduled": 0,
        "completed": 0,
        "failed": 0,
        "downloading": 0,
        "queued": 0,
        "pending": 0,
    }
    category_counts: Dict[str, int] = {cat: 0 for cat in CATEGORY_ORDER}
    if isinstance(custom_categories, dict):
        for cat in custom_categories:
            category_counts.setdefault(str(cat), 0)

    total_speed_bps = 0.0

    for job in jobs:
        st = str(job.get("status") or "").upper()
        if st == "DOWNLOADING":
            status_counts["downloading"] += 1
            status_counts["active"] += 1
            total_speed_bps += parse_speed_bps(job.get("speed"))
        elif st == "QUEUED":
            status_counts["queued"] += 1
            status_counts["active"] += 1
        elif st == "PENDING_PATH":
            status_counts["pending"] += 1
            status_counts["active"] += 1
        elif st == "PAUSED":
            status_counts["paused"] += 1
        elif st == "SCHEDULED":
            status_counts["scheduled"] += 1
        elif st == "COMPLETED":
            status_counts["completed"] += 1
        elif st == "FAILED":
            status_counts["failed"] += 1

        name = display_name_for_job(job)
        cat = infer_category(name, str(job.get("save_path") or ""), custom_categories)
        category_counts[cat] = category_counts.get(cat, 0) + 1

    return {
        "status_counts": status_counts,
        "category_counts": category_counts,
        "total_speed_bps": total_speed_bps,
        "total_speed_text": format_speed(total_speed_bps),
    }


class SpeedHistoryTracker:
    """In-memory ring buffer retaining recent transfer speed samples per job."""

    def __init__(self, max_samples: int = 60) -> None:
        self.max_samples = max(4, int(max_samples))
        self._series: Dict[str, Deque[float]] = {}

    def record_jobs(self, jobs: List[Dict[str, Any]]) -> None:
        seen_ids = set()
        for job in jobs:
            job_id = str(job.get("id") or "")
            if not job_id:
                continue
            seen_ids.add(job_id)
            st = str(job.get("status") or "").upper()
            bps = parse_speed_bps(job.get("speed")) if st == "DOWNLOADING" else 0.0
            buf = self._series.get(job_id)
            if buf is None:
                buf = deque(maxlen=self.max_samples)
                self._series[job_id] = buf
            if st == "DOWNLOADING" or len(buf) == 0 or buf[-1] != bps:
                buf.append(bps)

        stale = [jid for jid in self._series if jid not in seen_ids]
        for jid in stale:
            del self._series[jid]

    def get_samples(self, job_id: str) -> List[float]:
        buf = self._series.get(job_id)
        return list(buf) if buf is not None else []


def suggest_filename_from_url(raw_url: str) -> str:
    """Derive a clean filename from a download URL, defaulting to downloaded_file.bin."""
    text = (raw_url or "").strip()
    if not text:
        return "downloaded_file.bin"
    try:
        parsed = urlparse(text)
    except ValueError:
        return "downloaded_file.bin"

    path_part = parsed.path.rstrip("/")
    if not path_part:
        return "downloaded_file.bin"

    raw_base = unquote(os.path.basename(path_part)).strip()
    cleaned = raw_base.replace("/", "_").replace("\\", "_").replace("\x00", "")
    if not cleaned or cleaned in {".", ".."}:
        return "downloaded_file.bin"
    return cleaned


def validate_download_url(raw_url: str) -> Optional[str]:
    """Return validation error string for invalid HTTP/HTTPS URLs, or None when valid."""
    text = (raw_url or "").strip()
    if not text:
        return "Download URL cannot be empty."
    try:
        parsed = urlparse(text)
    except ValueError:
        return "Malformed download URL."
    if parsed.scheme.lower() not in {"http", "https"}:
        return "URL must start with http:// or https://."
    if not parsed.netloc or not parsed.hostname:
        return "URL must include a valid hostname."
    return None


def validate_filename(filename: str) -> Optional[str]:
    """Mirror Hydra backend filename validation rules cleanly for UI forms."""
    name = (filename or "").strip()
    if not name:
        return "Filename cannot be empty."
    if name in {".", ".."}:
        return "Invalid filename."
    if "/" in name or "\\" in name:
        return "Filename cannot contain path separators (/ or \\)."
    if "\x00" in name:
        return "Filename contains invalid null character."
    return None


def _expand_user_path(raw: str) -> str:
    cleaned = (raw or "").strip()
    if not cleaned:
        return ""
    if cleaned == "~":
        return str(Path.home())
    if cleaned.startswith("~/"):
        return str(Path.home() / cleaned[2:])
    return cleaned


def resolve_category_destination(
    filename: str,
    settings: Optional[Dict[str, Any]],
    fallback_dir: str,
) -> str:
    """Suggest the target directory from Hydra's category rules or default save path."""
    ext = Path(filename or "").suffix.lstrip(".").lower()
    categories = settings.get("categories") if isinstance(settings, dict) else None
    default_save = (
        _expand_user_path(str(settings.get("default_save_path") or ""))
        if isinstance(settings, dict)
        else ""
    ) or fallback_dir

    if ext and isinstance(categories, dict):
        for rule in categories.values():
            if not isinstance(rule, dict):
                continue
            exts = rule.get("extensions") or []
            if any(str(e).lstrip(".").lower() == ext for e in exts):
                cat_path = _expand_user_path(str(rule.get("path") or ""))
                if cat_path:
                    return cat_path

    return default_save


def available_job_actions(job: Optional[Dict[str, Any]]) -> Dict[str, bool]:
    """Compute exact enabled action capabilities for a selected download job."""
    if not job:
        return {
            "can_pause": False,
            "can_resume": False,
            "can_retry": False,
            "can_cancel": False,
            "can_delete": False,
            "can_edit_destination": False,
            "can_edit_url": False,
            "can_open_folder": False,
        }

    status = str(job.get("status") or "").upper()
    save_path = str(job.get("save_path") or "").strip()
    chunks = job.get("chunks") or []
    has_written_chunks = bool(chunks) and save_path not in {"", "PENDING", "DEFAULT"}

    return {
        "can_pause": status in {"DOWNLOADING", "QUEUED", "SCHEDULED"},
        "can_resume": status == "PAUSED",
        "can_retry": status == "FAILED",
        "can_cancel": status in {"DOWNLOADING", "QUEUED", "SCHEDULED", "PENDING_PATH", "PAUSED"},
        "can_delete": True,
        "can_edit_destination": status in {"PENDING_PATH", "QUEUED", "SCHEDULED", "PAUSED", "FAILED"}
        and not has_written_chunks,
        "can_edit_url": status in {"PAUSED", "FAILED", "QUEUED", "SCHEDULED", "PENDING_PATH"},
        "can_open_folder": bool(save_path and save_path not in {"PENDING", "DEFAULT"}),
    }
