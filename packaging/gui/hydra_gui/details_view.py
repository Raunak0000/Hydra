"""Download details and live progress views for Hydra GUI."""

from __future__ import annotations

import math
from typing import Any, Callable, Dict, List, Optional, Tuple

import gi  # type: ignore

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GObject, Gtk, Pango  # type: ignore

from .models import (
    available_job_actions,
    category_icon_name,
    compute_chunk_stats,
    display_name_for_job,
    effective_total_size_str,
    format_bytes,
    format_speed,
    infer_category,
    mask_headers,
    parse_timestamp,
    sanitize_url_for_display,
    status_info,
    url_host,
)


def _rounded_rect(cr: Any, x: float, y: float, w: float, h: float, r: float) -> None:
    if w <= 0 or h <= 0:
        return
    radius = max(0.0, min(r, w / 2.0, h / 2.0))
    cr.new_sub_path()
    cr.arc(x + w - radius, y + radius, radius, -math.pi / 2.0, 0.0)
    cr.arc(x + w - radius, y + h - radius, radius, 0.0, math.pi / 2.0)
    cr.arc(x + radius, y + h - radius, radius, math.pi / 2.0, math.pi)
    cr.arc(x + radius, y + radius, radius, math.pi, 3.0 * math.pi / 2.0)
    cr.close_path()


def _fill_color_for_status(status: str) -> Tuple[float, float, float, float]:
    norm = (status or "").upper()
    if norm == "COMPLETED":
        return (0.133, 0.773, 0.369, 0.95)  # #22c55e
    if norm == "PAUSED":
        return (0.961, 0.620, 0.043, 0.92)  # #f59e0b
    if norm == "FAILED":
        return (0.937, 0.267, 0.267, 0.92)  # #ef4444
    if norm in {"QUEUED", "SCHEDULED"}:
        return (0.388, 0.400, 0.945, 0.90)  # #6366f1
    return (0.231, 0.510, 0.965, 0.96)      # #3b82f6


class ChunkMapWidget(Gtk.DrawingArea):
    """IDM-style multi-segment progress visualizer drawn via Cairo."""

    def __init__(self, height: int = 28) -> None:
        super().__init__()
        self._chunk_stats: Dict[str, Any] = {
            "has_chunks": False,
            "segments": [],
            "overall_fraction": 0.0,
            "total_bytes": 0,
        }
        self._status = ""
        self.set_content_height(height)
        self.set_hexpand(True)
        self.set_draw_func(self._on_draw, None)

    def update_state(self, chunk_stats: Dict[str, Any], status: str) -> None:
        self._chunk_stats = chunk_stats
        self._status = status
        if chunk_stats.get("has_chunks"):
            self.set_tooltip_text(
                f"{chunk_stats['total_chunks']} segments • "
                f"{chunk_stats['completed_chunks']} completed • "
                f"{chunk_stats['active_chunks']} active • "
                f"{chunk_stats['pending_chunks']} pending"
            )
        else:
            pct = float(chunk_stats.get("overall_fraction", 0.0)) * 100.0
            self.set_tooltip_text(f"Overall progress: {pct:.1f}%")
        self.queue_draw()

    def _on_draw(self, _area: Gtk.DrawingArea, cr: Any, width: int, height: int, _data: Any) -> None:
        if width <= 4 or height <= 4:
            return

        pad = 2.0
        track_w = width - 2.0 * pad
        track_h = height - 2.0 * pad

        # Base translucent background track
        _rounded_rect(cr, pad, pad, track_w, track_h, 7.0)
        cr.set_source_rgba(0.58, 0.64, 0.72, 0.16)
        cr.fill_preserve()
        cr.set_source_rgba(0.58, 0.64, 0.72, 0.28)
        cr.set_line_width(1.0)
        cr.stroke()

        segments = self._chunk_stats.get("segments") or []
        has_chunks = bool(self._chunk_stats.get("has_chunks"))
        total_bytes = int(self._chunk_stats.get("total_bytes") or 0)
        overall_fraction = max(0.0, min(1.0, float(self._chunk_stats.get("overall_fraction") or 0.0)))
        active_rgba = _fill_color_for_status(self._status)

        if not has_chunks or not segments:
            fill_w = track_w * overall_fraction
            if fill_w > 1.0:
                cr.save()
                _rounded_rect(cr, pad + 1.0, pad + 1.0, track_w - 2.0, track_h - 2.0, 6.0)
                cr.clip()
                cr.rectangle(pad + 1.0, pad + 1.0, fill_w, track_h - 2.0)
                cr.set_source_rgba(*active_rgba)
                cr.fill()
                cr.restore()
            return

        n = len(segments)
        gap = 2.0 if n > 1 else 0.0
        usable_w = max(1.0, track_w - gap * (n - 1) - 2.0)
        cursor_x = pad + 1.0
        seg_y = pad + 1.0
        seg_h = max(4.0, track_h - 2.0)

        for idx, seg in enumerate(segments):
            if total_bytes > 0 and seg["length"] > 0:
                seg_w = usable_w * (seg["length"] / total_bytes)
            else:
                seg_w = usable_w / n

            # Ensure tiny final rounding doesn't overflow track width
            if idx == n - 1:
                seg_w = max(2.0, (pad + track_w - 1.0) - cursor_x)
            else:
                seg_w = max(2.0, seg_w)

            # Segment track box
            cr.save()
            _rounded_rect(cr, cursor_x, seg_y, seg_w, seg_h, min(4.0, seg_w / 3.0))
            cr.clip()

            cr.set_source_rgba(0.58, 0.64, 0.72, 0.14)
            cr.paint()

            frac = max(0.0, min(1.0, float(seg.get("fraction") or 0.0)))
            if seg.get("completed"):
                cr.set_source_rgba(0.133, 0.773, 0.369, 0.94)  # completed emerald
                cr.rectangle(cursor_x, seg_y, seg_w, seg_h)
                cr.fill()
            elif frac > 0.0:
                fill_w = seg_w * frac
                cr.set_source_rgba(*active_rgba)
                cr.rectangle(cursor_x, seg_y, fill_w, seg_h)
                cr.fill()

            cr.restore()

            # Subtle divider outline around each chunk block
            _rounded_rect(cr, cursor_x, seg_y, seg_w, seg_h, min(4.0, seg_w / 3.0))
            cr.set_source_rgba(0.58, 0.64, 0.72, 0.25)
            cr.set_line_width(0.8)
            cr.stroke()

            cursor_x += seg_w + gap


class SpeedSparklineWidget(Gtk.DrawingArea):
    """Real-time Cairo area chart displaying recent transfer speed samples."""

    def __init__(self, height: int = 84) -> None:
        super().__init__()
        self._samples: List[float] = []
        self._status = ""
        self.set_content_height(height)
        self.set_hexpand(True)
        self.set_draw_func(self._on_draw, None)

    def update_samples(self, samples: List[float], status: str) -> None:
        self._samples = list(samples)
        self._status = status
        self.queue_draw()

    def _on_draw(self, _area: Gtk.DrawingArea, cr: Any, width: int, height: int, _data: Any) -> None:
        if width <= 10 or height <= 10:
            return

        _rounded_rect(cr, 1.0, 1.0, width - 2.0, height - 2.0, 8.0)
        cr.set_source_rgba(0.58, 0.64, 0.72, 0.08)
        cr.fill_preserve()
        cr.set_source_rgba(0.58, 0.64, 0.72, 0.22)
        cr.set_line_width(1.0)
        cr.stroke()

        pad_x = 10.0
        pad_top = 20.0
        pad_bottom = 8.0
        chart_w = max(10.0, width - 2.0 * pad_x)
        chart_h = max(10.0, height - pad_top - pad_bottom)

        samples = self._samples[-60:]
        peak = max(samples) if samples else 0.0

        # Header caption directly inside chart frame
        cr.set_source_rgba(0.58, 0.64, 0.72, 0.85)
        cr.set_font_size(10.5)
        cr.move_to(pad_x, 14.0)
        if peak > 0:
            current = samples[-1] if samples else 0.0
            cr.show_text(f"Throughput History  •  Current: {format_speed(current)}  •  Peak: {format_speed(peak)}")
        elif (self._status or "").upper() == "DOWNLOADING":
            cr.show_text("Throughput History  •  Sampling transfer rate...")
        else:
            cr.show_text("Throughput History  •  No active transfer samples")

        # Baseline dashed reference line
        cr.save()
        cr.set_dash([3.0, 3.0])
        cr.set_source_rgba(0.58, 0.64, 0.72, 0.22)
        cr.set_line_width(1.0)
        cr.move_to(pad_x, pad_top + chart_h / 2.0)
        cr.line_to(pad_x + chart_w, pad_top + chart_h / 2.0)
        cr.stroke()
        cr.restore()

        if len(samples) < 2 or peak <= 0:
            # Draw flat baseline when idle or only 1 point exists
            cr.set_source_rgba(0.58, 0.64, 0.72, 0.35)
            cr.set_line_width(1.5)
            y_base = pad_top + chart_h - 1.0
            cr.move_to(pad_x, y_base)
            cr.line_to(pad_x + chart_w, y_base)
            cr.stroke()
            return

        r, g, b, _ = _fill_color_for_status(self._status or "DOWNLOADING")
        n = len(samples)
        step_x = chart_w / max(1, n - 1)
        points = [
            (
                pad_x + i * step_x,
                pad_top + chart_h - (max(0.0, val) / peak) * chart_h,
            )
            for i, val in enumerate(samples)
        ]

        # Filled gradient under curve
        cr.save()
        _rounded_rect(cr, 2.0, 2.0, width - 4.0, height - 4.0, 7.0)
        cr.clip()

        cr.move_to(points[0][0], pad_top + chart_h)
        for px, py in points:
            cr.line_to(px, py)
        cr.line_to(points[-1][0], pad_top + chart_h)
        cr.close_path()

        import cairo  # type: ignore

        grad = cairo.LinearGradient(0, pad_top, 0, pad_top + chart_h)
        grad.add_color_stop_rgba(0.0, r, g, b, 0.36)
        grad.add_color_stop_rgba(1.0, r, g, b, 0.03)
        cr.set_source(grad)
        cr.fill()
        cr.restore()

        # Crisp top stroke
        cr.move_to(points[0][0], points[0][1])
        for px, py in points[1:]:
            cr.line_to(px, py)
        cr.set_source_rgba(r, g, b, 0.95)
        cr.set_line_width(2.0)
        cr.set_line_join(1)
        cr.stroke()

        # Current sample dot
        lx, ly = points[-1]
        cr.arc(lx, ly, 3.2, 0.0, 2.0 * math.pi)
        cr.set_source_rgba(r, g, b, 1.0)
        cr.fill()


class _SegmentRowWidget(Gtk.Box):
    """Compact row representing one parallel chunk worker range."""

    def __init__(self) -> None:
        super().__init__(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        self.add_css_class("chunk-row-card")

        self.index_label = Gtk.Label(xalign=0.0)
        self.index_label.add_css_class("metric-strong")
        self.index_label.set_width_chars(9)

        self.range_label = Gtk.Label(xalign=0.0)
        self.range_label.add_css_class("job-subtitle-label")
        self.range_label.add_css_class("tabular-metric")
        self.range_label.set_width_chars(21)

        self.progress_bar = Gtk.ProgressBar()
        self.progress_bar.add_css_class("compact")
        self.progress_bar.set_hexpand(True)
        self.progress_bar.set_valign(Gtk.Align.CENTER)

        self.pct_label = Gtk.Label(xalign=1.0)
        self.pct_label.add_css_class("tabular-metric")
        self.pct_label.set_width_chars(8)

        self.state_badge = Gtk.Label()
        self.state_badge.add_css_class("status-pill")
        self.state_badge.set_width_chars(9)

        self.append(self.index_label)
        self.append(self.range_label)
        self.append(self.progress_bar)
        self.append(self.pct_label)
        self.append(self.state_badge)

        self._last_progress_css = ""
        self._last_badge_css = ""

    def update_segment(self, seg: Dict[str, Any], job_status: str) -> None:
        self.index_label.set_text(f"Stream #{seg['index']}")
        self.range_label.set_text(seg["byte_range_label"])
        self.progress_bar.set_fraction(seg["fraction"])
        self.pct_label.set_text(seg["progress_label"])

        state = seg["state"]
        if state == "completed":
            badge_text = "Done"
            badge_css = "status-completed"
            prog_css = "progress-completed"
        elif state == "active":
            if (job_status or "").upper() == "PAUSED":
                badge_text = "Paused"
                badge_css = "status-paused"
                prog_css = "progress-paused"
            elif (job_status or "").upper() == "FAILED":
                badge_text = "Stopped"
                badge_css = "status-failed"
                prog_css = "progress-failed"
            else:
                badge_text = "Active"
                badge_css = "status-downloading"
                prog_css = "progress-active"
        else:
            badge_text = "Queued"
            badge_css = "status-queued"
            prog_css = "progress-queued"

        self.state_badge.set_text(badge_text)
        if self._last_badge_css and self._last_badge_css != badge_css:
            self.state_badge.remove_css_class(self._last_badge_css)
        if self._last_badge_css != badge_css:
            self.state_badge.add_css_class(badge_css)
            self._last_badge_css = badge_css

        if self._last_progress_css and self._last_progress_css != prog_css:
            self.progress_bar.remove_css_class(self._last_progress_css)
        if self._last_progress_css != prog_css:
            self.progress_bar.add_css_class(prog_css)
            self._last_progress_css = prog_css


def _make_telemetry_tile(title: str) -> Tuple[Gtk.Box, Gtk.Label]:
    box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4)
    box.add_css_class("telemetry-tile")
    box.set_hexpand(True)

    lbl = Gtk.Label(label=title, xalign=0.0)
    lbl.add_css_class("telemetry-label")

    val = Gtk.Label(label="--", xalign=0.0)
    val.add_css_class("telemetry-value")
    val.set_ellipsize(Pango.EllipsizeMode.END)

    box.append(lbl)
    box.append(val)
    return box, val


class DownloadDetailsView(Gtk.Box):
    """Tabbed Download Details & Progress inspector pane."""

    def __init__(
        self,
        on_copy_text: Callable[[str, str], None],
        on_open_folder: Callable[[str], None],
        on_popout: Callable[[str], None],
        on_edit_destination: Optional[Callable[[str], None]] = None,
    ) -> None:
        super().__init__(orientation=Gtk.Orientation.VERTICAL, spacing=0)
        self.add_css_class("inspector-panel")

        self._on_copy_text = on_copy_text
        self._on_open_folder = on_open_folder
        self._on_popout = on_popout
        self._on_edit_destination = on_edit_destination

        self._current_job: Optional[Dict[str, Any]] = None
        self._current_job_id = ""
        self._reveal_headers = False
        self._last_status_css = ""
        self._last_prog_css = ""
        self._last_headers_sig: Optional[Any] = None
        self._segment_rows: List[_SegmentRowWidget] = []

        self._root_stack = Gtk.Stack()
        self._root_stack.set_vexpand(True)
        self._root_stack.set_hexpand(True)
        self.append(self._root_stack)

        # Empty state page
        empty_page = Adw.StatusPage()
        empty_page.set_icon_name("folder-download-symbolic")
        empty_page.set_title("Select a Download")
        empty_page.set_description(
            "Choose any job from the list to inspect live progress, parallel segments, transfer speed history, and metadata."
        )
        self._root_stack.add_named(empty_page, "empty")

        # Active inspector container
        content_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=0)
        self._root_stack.add_named(content_box, "details")

        # ── Header Summary Strip ──
        header_strip = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=8)
        header_strip.add_css_class("inspector-header-strip")

        top_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        self._icon = Gtk.Image.new_from_icon_name("text-x-generic-symbolic")
        self._icon.set_pixel_size(24)

        title_col = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2)
        title_col.set_hexpand(True)
        self._title_label = Gtk.Label(xalign=0.0)
        self._title_label.add_css_class("job-title-label")
        self._title_label.set_ellipsize(Pango.EllipsizeMode.MIDDLE)

        self._subtitle_label = Gtk.Label(xalign=0.0)
        self._subtitle_label.add_css_class("job-subtitle-label")
        self._subtitle_label.set_ellipsize(Pango.EllipsizeMode.MIDDLE)

        title_col.append(self._title_label)
        title_col.append(self._subtitle_label)

        self._status_badge = Gtk.Label()
        self._status_badge.add_css_class("status-pill")
        self._status_badge.set_valign(Gtk.Align.CENTER)

        self._switcher = Adw.ViewSwitcher()
        self._switcher.set_policy(Adw.ViewSwitcherPolicy.WIDE)

        actions_box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=6)

        self._popout_btn = Gtk.Button.new_from_icon_name("window-new-symbolic")
        self._popout_btn.set_tooltip_text("Open live progress window")
        self._popout_btn.add_css_class("flat")
        self._popout_btn.connect("clicked", self._handle_popout)

        self._folder_btn = Gtk.Button.new_from_icon_name("folder-open-symbolic")
        self._folder_btn.set_tooltip_text("Open containing folder")
        self._folder_btn.add_css_class("flat")
        self._folder_btn.connect("clicked", self._handle_open_folder)

        self._copy_url_btn = Gtk.Button.new_from_icon_name("edit-copy-symbolic")
        self._copy_url_btn.set_tooltip_text("Copy download URL")
        self._copy_url_btn.add_css_class("flat")
        self._copy_url_btn.connect("clicked", self._handle_copy_url)

        actions_box.append(self._copy_url_btn)
        actions_box.append(self._folder_btn)
        actions_box.append(self._popout_btn)

        top_row.append(self._icon)
        top_row.append(title_col)
        top_row.append(self._status_badge)
        top_row.append(self._switcher)
        top_row.append(actions_box)

        prog_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=12)
        self._main_progress = Gtk.ProgressBar()
        self._main_progress.add_css_class("compact")
        self._main_progress.set_hexpand(True)
        self._main_progress.set_valign(Gtk.Align.CENTER)

        self._main_prog_summary = Gtk.Label(xalign=1.0)
        self._main_prog_summary.add_css_class("tabular-metric")

        prog_row.append(self._main_progress)
        prog_row.append(self._main_prog_summary)

        header_strip.append(top_row)
        header_strip.append(prog_row)
        content_box.append(header_strip)

        # ── Tabbed Body ──
        self._view_stack = Adw.ViewStack()
        self._view_stack.set_vexpand(True)
        self._switcher.set_stack(self._view_stack)
        content_box.append(self._view_stack)

        # Tab 1: Progress & Segments
        prog_scroll = Gtk.ScrolledWindow()
        prog_scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        prog_scroll.set_vexpand(True)

        prog_body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
        prog_body.set_margin_top(12)
        prog_body.set_margin_bottom(14)
        prog_body.set_margin_start(16)
        prog_body.set_margin_end(16)
        prog_scroll.set_child(prog_body)

        # Error diagnostics banner (only visible on failed / error message)
        self._error_banner = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        self._error_banner.add_css_class("error-banner-card")
        err_icon = Gtk.Image.new_from_icon_name("dialog-error-symbolic")
        self._error_label = Gtk.Label(xalign=0.0, wrap=True)
        self._error_label.set_selectable(True)
        self._error_label.set_hexpand(True)
        self._error_banner.append(err_icon)
        self._error_banner.append(self._error_label)
        self._error_banner.set_visible(False)
        prog_body.append(self._error_banner)

        # 4 Telemetry tiles
        tiles_box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        tiles_box.set_homogeneous(True)
        t_speed, self._tile_speed = _make_telemetry_tile("Transfer Speed")
        t_prog, self._tile_progress = _make_telemetry_tile("Completion")
        t_bytes, self._tile_bytes = _make_telemetry_tile("Transferred")
        t_eta, self._tile_eta = _make_telemetry_tile("ETA / Connections")
        tiles_box.append(t_speed)
        tiles_box.append(t_prog)
        tiles_box.append(t_bytes)
        tiles_box.append(t_eta)
        prog_body.append(tiles_box)

        # Chunk Segment Map card
        chunk_header = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        self._chunk_title = Gtk.Label(label="Parallel Segment Map", xalign=0.0)
        self._chunk_title.add_css_class("metric-strong")
        self._chunk_title.set_hexpand(True)

        legend_label = Gtk.Label(label="Green: Done  •  Blue: Downloaded  •  Gray: Remaining")
        legend_label.add_css_class("legend-dot")
        chunk_header.append(self._chunk_title)
        chunk_header.append(legend_label)
        prog_body.append(chunk_header)

        self._chunk_map = ChunkMapWidget(height=28)
        prog_body.append(self._chunk_map)

        # Live throughput chart
        self._sparkline = SpeedSparklineWidget(height=78)
        prog_body.append(self._sparkline)

        # Per-segment detail breakdown
        self._segments_header = Gtk.Label(label="Segment Breakdown", xalign=0.0)
        self._segments_header.add_css_class("metric-strong")
        prog_body.append(self._segments_header)

        self._segments_empty = Gtk.Label(
            label="Segment boundaries will appear once parallel connections initialize.",
            xalign=0.0,
        )
        self._segments_empty.add_css_class("job-subtitle-label")
        prog_body.append(self._segments_empty)

        self._segments_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=6)
        prog_body.append(self._segments_box)

        self._view_stack.add_titled_with_icon(
            prog_scroll,
            "progress",
            "Progress & Segments",
            "view-grid-symbolic",
        )

        # Tab 2: Details & Metadata
        meta_scroll = Gtk.ScrolledWindow()
        meta_scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        meta_scroll.set_vexpand(True)

        meta_body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=14)
        meta_body.set_margin_top(12)
        meta_body.set_margin_bottom(16)
        meta_body.set_margin_start(16)
        meta_body.set_margin_end(16)
        meta_scroll.set_child(meta_body)

        # Group 1: Source & Destination
        src_group = Adw.PreferencesGroup(title="Source &amp; Destination")
        self._row_url = Adw.ActionRow(title="Source URL")
        self._row_url.set_use_markup(False)
        self._row_url.set_subtitle_selectable(True)
        url_copy = Gtk.Button.new_from_icon_name("edit-copy-symbolic")
        url_copy.add_css_class("flat")
        url_copy.set_valign(Gtk.Align.CENTER)
        url_copy.set_tooltip_text("Copy URL")
        url_copy.connect("clicked", self._handle_copy_url)
        self._row_url.add_suffix(url_copy)
        src_group.add(self._row_url)

        self._row_path = Adw.ActionRow(title="Save Location")
        self._row_path.set_use_markup(False)
        self._row_path.set_subtitle_selectable(True)
        path_copy = Gtk.Button.new_from_icon_name("edit-copy-symbolic")
        path_copy.add_css_class("flat")
        path_copy.set_valign(Gtk.Align.CENTER)
        path_copy.set_tooltip_text("Copy destination path")
        path_copy.connect("clicked", self._handle_copy_path)
        self._row_path_open = Gtk.Button.new_from_icon_name("folder-open-symbolic")
        self._row_path_open.add_css_class("flat")
        self._row_path_open.set_valign(Gtk.Align.CENTER)
        self._row_path_open.set_tooltip_text("Open containing folder")
        self._row_path_open.connect("clicked", self._handle_open_folder)
        self._row_path_edit = Gtk.Button.new_from_icon_name("document-edit-symbolic")
        self._row_path_edit.add_css_class("flat")
        self._row_path_edit.set_valign(Gtk.Align.CENTER)
        self._row_path_edit.set_tooltip_text("Change destination folder & filename")
        self._row_path_edit.connect("clicked", self._handle_edit_destination)
        self._row_path.add_suffix(path_copy)
        self._row_path.add_suffix(self._row_path_edit)
        self._row_path.add_suffix(self._row_path_open)
        src_group.add(self._row_path)

        meta_body.append(src_group)

        # Group 2: Transfer & Integrity Properties
        prop_group = Adw.PreferencesGroup(title="Properties &amp; Integrity")
        self._row_id = Adw.ActionRow(title="Job ID &amp; Category")
        self._row_id.set_use_markup(False)
        self._row_id.set_subtitle_selectable(True)
        self._row_batch = Adw.ActionRow(title="Batch Group")
        self._row_batch.set_use_markup(False)
        self._row_batch.set_subtitle_selectable(True)
        self._row_limit = Adw.ActionRow(title="Speed Limit &amp; Checksum")
        self._row_limit.set_use_markup(False)
        self._row_limit.set_subtitle_selectable(True)
        self._row_times = Adw.ActionRow(title="Timeline")
        self._row_times.set_use_markup(False)
        self._row_times.set_subtitle_selectable(True)

        prop_group.add(self._row_id)
        prop_group.add(self._row_batch)
        prop_group.add(self._row_limit)
        prop_group.add(self._row_times)
        meta_body.append(prop_group)

        # Group 3: Request Headers
        self._headers_group = Adw.PreferencesGroup(title="Captured Request Headers")
        self._reveal_toggle = Gtk.ToggleButton(label="Show Sensitive")
        self._reveal_toggle.add_css_class("flat")
        self._reveal_toggle.connect("toggled", self._on_reveal_headers_toggled)
        self._headers_group.set_header_suffix(self._reveal_toggle)
        self._header_rows: List[Adw.ActionRow] = []
        meta_body.append(self._headers_group)

        self._view_stack.add_titled_with_icon(
            meta_scroll,
            "metadata",
            "Details & Metadata",
            "dialog-information-symbolic",
        )

        self._root_stack.set_visible_child_name("empty")

    def _handle_copy_url(self, *_args: Any) -> None:
        if self._current_job and self._current_job.get("url"):
            self._on_copy_text(str(self._current_job["url"]), "Download URL copied")

    def _handle_copy_path(self, *_args: Any) -> None:
        if self._current_job and self._current_job.get("save_path"):
            self._on_copy_text(str(self._current_job["save_path"]), "Destination path copied")

    def _handle_open_folder(self, *_args: Any) -> None:
        if self._current_job and self._current_job.get("save_path"):
            self._on_open_folder(str(self._current_job["save_path"]))

    def _handle_edit_destination(self, *_args: Any) -> None:
        if self._current_job and self._current_job.get("id") and self._on_edit_destination is not None:
            self._on_edit_destination(str(self._current_job["id"]))

    def _handle_popout(self, *_args: Any) -> None:
        if self._current_job and self._current_job.get("id"):
            self._on_popout(str(self._current_job["id"]))

    def _on_reveal_headers_toggled(self, btn: Gtk.ToggleButton) -> None:
        self._reveal_headers = btn.get_active()
        self._last_headers_sig = None
        if self._current_job is not None:
            self._sync_headers(self._current_job.get("headers"))

    def clear(self) -> None:
        self._current_job = None
        self._current_job_id = ""
        self._root_stack.set_visible_child_name("empty")

    def update_job(
        self,
        job: Optional[Dict[str, Any]],
        speed_samples: List[float],
        custom_categories: Optional[Dict[str, Any]] = None,
    ) -> None:
        if job is None:
            self.clear()
            return

        self._root_stack.set_visible_child_name("details")
        job_id = str(job.get("id") or "")
        switched_job = job_id != self._current_job_id
        self._current_job = job
        self._current_job_id = job_id

        if switched_job and self._reveal_headers:
            self._reveal_toggle.set_active(False)
            self._reveal_headers = False
            self._last_headers_sig = None

        name = display_name_for_job(job)
        save_path = str(job.get("save_path") or "")
        url = str(job.get("url") or "")
        status = str(job.get("status") or "").upper()
        info = status_info(status)
        cat = infer_category(name, save_path, custom_categories)

        self._icon.set_from_icon_name(category_icon_name(cat))
        self._title_label.set_text(name)
        host = url_host(url)
        self._subtitle_label.set_text(
            f"{cat} • {host}" if host else f"{cat} • {save_path or 'Pending path'}"
        )

        # Status badge styling
        self._status_badge.set_text(info["label"])
        badge_css = info["css_class"]
        if self._last_status_css != badge_css:
            if self._last_status_css:
                self._status_badge.remove_css_class(self._last_status_css)
            self._status_badge.add_css_class(badge_css)
            self._last_status_css = badge_css

        progress_pct = max(0.0, min(100.0, float(job.get("progress") or 0.0)))
        self._main_progress.set_fraction(progress_pct / 100.0)
        prog_css = info["progress_css"]
        if self._last_prog_css != prog_css:
            if self._last_prog_css:
                self._main_progress.remove_css_class(self._last_prog_css)
            self._main_progress.add_css_class(prog_css)
            self._last_prog_css = prog_css

        downloaded_str = str(job.get("downloaded") or "0 B")
        total_str = effective_total_size_str(job)
        speed_str = str(job.get("speed") or "0.00 KB/s")
        eta_str = "Completed" if status == "COMPLETED" else str(job.get("eta") or "--")

        self._main_prog_summary.set_text(
            f"{progress_pct:.1f}%  •  {downloaded_str} / {total_str}"
        )

        caps = available_job_actions(job)
        self._folder_btn.set_sensitive(caps["can_open_folder"])
        self._row_path_open.set_sensitive(caps["can_open_folder"])
        self._row_path_edit.set_visible(self._on_edit_destination is not None)
        self._row_path_edit.set_sensitive(caps["can_edit_destination"])

        # Error diagnostics
        err_msg = str(job.get("error_message") or "").strip()
        if err_msg:
            self._error_label.set_text(err_msg)
            self._error_banner.set_visible(True)
        else:
            self._error_banner.set_visible(False)

        # Chunks & telemetry tiles
        chunk_stats = compute_chunk_stats(job.get("chunks"), status, progress_pct)
        self._tile_speed.set_text(speed_str if status == "DOWNLOADING" else "0.00 KB/s")
        self._tile_progress.set_text(f"{progress_pct:.1f}%")
        self._tile_bytes.set_text(f"{downloaded_str} / {total_str}")
        if chunk_stats["has_chunks"]:
            self._tile_eta.set_text(
                f"{eta_str}  •  {chunk_stats['total_chunks']} streams"
            )
            self._chunk_title.set_text(
                f"Parallel Segment Map ({chunk_stats['completed_chunks']}/{chunk_stats['total_chunks']} done)"
            )
        else:
            self._tile_eta.set_text(eta_str)
            self._chunk_title.set_text("Download Progress Ribbon")

        self._chunk_map.update_state(chunk_stats, status)
        self._sparkline.update_samples(speed_samples, status)
        self._sync_segment_rows(chunk_stats, status)

        # Metadata tab values
        self._row_url.set_subtitle(sanitize_url_for_display(url) or "--")
        self._row_path.set_subtitle(save_path or "--")
        self._row_id.set_subtitle(f"ID: {job_id}  •  Category: {cat}")

        batch_id = str(job.get("batch_id") or "").strip()
        self._row_batch.set_subtitle(batch_id if batch_id else "Single download (not part of a batch)")

        max_speed = int(job.get("max_speed_bytes") or 0)
        limit_text = f"{format_speed(float(max_speed))}" if max_speed > 0 else "Unlimited"
        exp_ck = str(job.get("expected_checksum") or "").strip()
        algo_ck = str(job.get("checksum_algo") or "").strip()
        verified_ck = bool(job.get("checksum_verified"))
        if exp_ck:
            status_ck = "Verified ✓" if verified_ck else "Pending / Unverified"
            ck_text = f"{(algo_ck or 'hash').upper()}: {exp_ck[:20]}{'...' if len(exp_ck) > 20 else ''} ({status_ck})"
        else:
            ck_text = "No checksum configured"
        self._row_limit.set_subtitle(f"Speed Cap: {limit_text}  •  Checksum: {ck_text}")

        created_s = parse_timestamp(job.get("created_at"))
        sched_s = parse_timestamp(job.get("scheduled_at"))
        done_s = parse_timestamp(job.get("completed_at"))
        timeline_parts = [f"Created: {created_s}"]
        if sched_s != "--":
            timeline_parts.append(f"Scheduled: {sched_s}")
        if done_s != "--":
            timeline_parts.append(f"Completed: {done_s}")
        self._row_times.set_subtitle("  •  ".join(timeline_parts))

        self._sync_headers(job.get("headers"))

    def _sync_segment_rows(self, chunk_stats: Dict[str, Any], status: str) -> None:
        segments = chunk_stats.get("segments") or []
        if not segments:
            self._segments_empty.set_visible(True)
            self._segments_box.set_visible(False)
            return

        self._segments_empty.set_visible(False)
        self._segments_box.set_visible(True)

        while len(self._segment_rows) < len(segments):
            row = _SegmentRowWidget()
            self._segment_rows.append(row)
            self._segments_box.append(row)

        while len(self._segment_rows) > len(segments):
            row = self._segment_rows.pop()
            self._segments_box.remove(row)

        for row, seg in zip(self._segment_rows, segments):
            row.update_segment(seg, status)

    def _sync_headers(self, raw_headers: Any) -> None:
        headers_dict = raw_headers if isinstance(raw_headers, dict) else {}
        sig = (
            self._reveal_headers,
            tuple(sorted((str(k), str(v)) for k, v in headers_dict.items())),
        )
        if sig == self._last_headers_sig:
            return
        self._last_headers_sig = sig

        for row in self._header_rows:
            self._headers_group.remove(row)
        self._header_rows.clear()

        entries = mask_headers(headers_dict, reveal=self._reveal_headers)
        if not entries:
            self._reveal_toggle.set_visible(False)
            empty_row = Adw.ActionRow(title="No custom HTTP headers captured")
            empty_row.set_use_markup(False)
            empty_row.set_subtitle("Standard Hydra User-Agent headers used.")
            self._headers_group.add(empty_row)
            self._header_rows.append(empty_row)
            return

        has_sensitive = any(sens for _, _, sens in entries)
        self._reveal_toggle.set_visible(has_sensitive)

        for key, display_val, _sens in entries:
            row = Adw.ActionRow(title=key)
            row.set_use_markup(False)
            row.set_subtitle(display_val)
            row.set_subtitle_selectable(True)
            self._headers_group.add(row)
            self._header_rows.append(row)


class LiveProgressWindow(Adw.Window):
    """Compact IDM-style pop-out live transfer monitor for a single download job."""

    def __init__(
        self,
        parent: Gtk.Window,
        job_id: str,
        on_open_folder: Callable[[str], None],
        on_closed: Callable[[str], None],
        on_pause: Optional[Callable[[str], None]] = None,
        on_resume: Optional[Callable[[str], None]] = None,
        on_retry: Optional[Callable[[str], None]] = None,
        on_cancel: Optional[Callable[[str], None]] = None,
    ) -> None:
        super().__init__(transient_for=parent)
        self.job_id = job_id
        self._on_open_folder = on_open_folder
        self._on_closed = on_closed
        self._on_pause = on_pause
        self._on_resume = on_resume
        self._on_retry = on_retry
        self._on_cancel = on_cancel
        self._current_save_path = ""
        self._last_status_css = ""
        self._last_prog_css = ""

        self.set_default_size(560, 420)
        self.set_title("Download Progress")

        toolbar_view = Adw.ToolbarView()
        header = Adw.HeaderBar()
        toolbar_view.add_top_bar(header)

        self._pause_btn = Gtk.Button.new_from_icon_name("media-playback-pause-symbolic")
        self._pause_btn.set_tooltip_text("Pause download")
        self._pause_btn.connect("clicked", lambda *_: self._on_pause and self._on_pause(self.job_id))
        header.pack_start(self._pause_btn)

        self._resume_btn = Gtk.Button.new_from_icon_name("media-playback-start-symbolic")
        self._resume_btn.set_tooltip_text("Resume download")
        self._resume_btn.connect("clicked", lambda *_: self._on_resume and self._on_resume(self.job_id))
        header.pack_start(self._resume_btn)

        self._retry_btn = Gtk.Button.new_from_icon_name("view-refresh-symbolic")
        self._retry_btn.set_tooltip_text("Retry failed download")
        self._retry_btn.connect("clicked", lambda *_: self._on_retry and self._on_retry(self.job_id))
        header.pack_start(self._retry_btn)

        self._cancel_btn = Gtk.Button.new_from_icon_name("process-stop-symbolic")
        self._cancel_btn.set_tooltip_text("Cancel download")
        self._cancel_btn.connect("clicked", lambda *_: self._on_cancel and self._on_cancel(self.job_id))
        header.pack_start(self._cancel_btn)

        self._open_folder_btn = Gtk.Button.new_from_icon_name("folder-open-symbolic")
        self._open_folder_btn.set_tooltip_text("Open containing folder")
        self._open_folder_btn.connect("clicked", lambda *_: self._on_open_folder(self._current_save_path))
        header.pack_end(self._open_folder_btn)

        body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
        body.set_margin_top(14)
        body.set_margin_bottom(16)
        body.set_margin_start(18)
        body.set_margin_end(18)

        title_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        self._name_label = Gtk.Label(xalign=0.0)
        self._name_label.add_css_class("job-title-label")
        self._name_label.set_ellipsize(Pango.EllipsizeMode.MIDDLE)
        self._name_label.set_hexpand(True)

        self._status_badge = Gtk.Label()
        self._status_badge.add_css_class("status-pill")
        title_row.append(self._name_label)
        title_row.append(self._status_badge)
        body.append(title_row)

        self._url_label = Gtk.Label(xalign=0.0)
        self._url_label.add_css_class("job-subtitle-label")
        self._url_label.set_ellipsize(Pango.EllipsizeMode.MIDDLE)
        body.append(self._url_label)

        self._progress_bar = Gtk.ProgressBar()
        self._progress_bar.add_css_class("compact")
        body.append(self._progress_bar)

        tiles = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        tiles.set_homogeneous(True)
        b1, self._speed_val = _make_telemetry_tile("Speed")
        b2, self._pct_val = _make_telemetry_tile("Progress")
        b3, self._bytes_val = _make_telemetry_tile("Downloaded")
        b4, self._eta_val = _make_telemetry_tile("Time Left")
        tiles.append(b1)
        tiles.append(b2)
        tiles.append(b3)
        tiles.append(b4)
        body.append(tiles)

        self._sparkline = SpeedSparklineWidget(height=74)
        body.append(self._sparkline)

        self._chunk_caption = Gtk.Label(label="Parallel Segment Progress", xalign=0.0)
        self._chunk_caption.add_css_class("telemetry-label")
        body.append(self._chunk_caption)

        self._chunk_map = ChunkMapWidget(height=26)
        body.append(self._chunk_map)

        self._err_label = Gtk.Label(xalign=0.0, wrap=True)
        self._err_label.add_css_class("error-banner-card")
        self._err_label.set_visible(False)
        body.append(self._err_label)

        toolbar_view.set_content(body)
        self.set_content(toolbar_view)
        self.connect("close-request", self._handle_close)

    def _handle_close(self, *_args: Any) -> bool:
        self._on_closed(self.job_id)
        return False

    def update_job(self, job: Optional[Dict[str, Any]], speed_samples: List[float]) -> None:
        if job is None:
            self._status_badge.set_text("Removed")
            return

        name = display_name_for_job(job)
        status = str(job.get("status") or "").upper()
        info = status_info(status)
        pct = max(0.0, min(100.0, float(job.get("progress") or 0.0)))
        self._current_save_path = str(job.get("save_path") or "")

        self.set_title(f"{pct:.0f}% — {name}")
        self._name_label.set_text(name)
        self._url_label.set_text(sanitize_url_for_display(str(job.get("url") or "")))
        caps = available_job_actions(job)
        self._open_folder_btn.set_sensitive(caps["can_open_folder"])
        self._pause_btn.set_visible(self._on_pause is not None)
        self._pause_btn.set_sensitive(caps["can_pause"])
        self._resume_btn.set_visible(self._on_resume is not None)
        self._resume_btn.set_sensitive(caps["can_resume"])
        self._retry_btn.set_visible(self._on_retry is not None)
        self._retry_btn.set_sensitive(caps["can_retry"])
        self._cancel_btn.set_visible(self._on_cancel is not None)
        self._cancel_btn.set_sensitive(caps["can_cancel"])

        self._status_badge.set_text(info["label"])
        badge_css = info["css_class"]
        if self._last_status_css != badge_css:
            if self._last_status_css:
                self._status_badge.remove_css_class(self._last_status_css)
            self._status_badge.add_css_class(badge_css)
            self._last_status_css = badge_css

        self._progress_bar.set_fraction(pct / 100.0)
        prog_css = info["progress_css"]
        if self._last_prog_css != prog_css:
            if self._last_prog_css:
                self._progress_bar.remove_css_class(self._last_prog_css)
            self._progress_bar.add_css_class(prog_css)
            self._last_prog_css = prog_css

        self._speed_val.set_text(str(job.get("speed") or "0.00 KB/s") if status == "DOWNLOADING" else "0.00 KB/s")
        self._pct_val.set_text(f"{pct:.1f}%")
        total_str = effective_total_size_str(job)
        self._bytes_val.set_text(f"{job.get('downloaded') or '0 B'} / {total_str}")
        self._eta_val.set_text("Completed" if status == "COMPLETED" else str(job.get("eta") or "--"))

        chunk_stats = compute_chunk_stats(job.get("chunks"), status, pct)
        if chunk_stats["has_chunks"]:
            self._chunk_caption.set_text(
                f"Parallel Segments: {chunk_stats['completed_chunks']} completed / {chunk_stats['total_chunks']} total"
            )
        else:
            self._chunk_caption.set_text("Progress Ribbon")
        self._chunk_map.update_state(chunk_stats, status)
        self._sparkline.update_samples(speed_samples, status)

        err = str(job.get("error_message") or "").strip()
        if err:
            self._err_label.set_text(err)
            self._err_label.set_visible(True)
        else:
            self._err_label.set_visible(False)

