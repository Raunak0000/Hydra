"""Main GTK 4 + Libadwaita window and graphical download list for Hydra."""

from __future__ import annotations

import os
import threading
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

import gi  # type: ignore

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gdk, Gio, GLib, GObject, Gtk, Pango  # type: ignore

from .client import (
    HydraGUIClient,
    SSEWatcher,
    ensure_local_daemon,
    open_path_in_desktop,
    resolve_downloads_dir,
    resolve_icon_paths,
)
from .details_view import DownloadDetailsView, LiveProgressWindow
from .dialogs import (
    EditDestinationDialog,
    NewDownloadDialog,
    UpdateURLDialog,
    confirm_job_removal_dialog,
)
from .models import (
    CATEGORY_ORDER,
    STATUS_FILTERS,
    SpeedHistoryTracker,
    available_job_actions,
    category_icon_name,
    compute_chunk_stats,
    compute_dashboard_stats,
    display_name_for_job,
    effective_total_size_str,
    infer_category,
    matches_status_filter,
    parse_byte_string,
    parse_speed_bps,
    status_info,
    url_host,
)
from .theme import HYDRA_CSS


SORT_OPTIONS: Tuple[Tuple[str, str], ...] = (
    ("added", "Sort: Added Order"),
    ("status", "Sort: Status"),
    ("progress", "Sort: Progress"),
    ("speed", "Sort: Speed"),
    ("name", "Sort: Name"),
    ("size", "Sort: File Size"),
)


class JobListItem(GObject.Object):
    """Observable list item backing Gtk.ListView rows without widget churn."""

    __gtype_name__ = "HydraJobListItem"
    __gsignals__ = {
        "updated": (GObject.SignalFlags.RUN_FIRST, None, ()),
    }

    def __init__(self, job: Dict[str, Any], category: str, order_index: int) -> None:
        super().__init__()
        self.job_id: str = str(job.get("id") or "")
        self.raw: Dict[str, Any] = job
        self.category: str = category
        self.order_index: int = order_index
        self.display_name: str = display_name_for_job(job)
        self.status: str = str(job.get("status") or "").upper()
        self.progress: float = float(job.get("progress") or 0.0)
        self.speed_bps: float = parse_speed_bps(job.get("speed"))
        self.size_bytes: int = parse_byte_string(effective_total_size_str(job))

    def apply(self, job: Dict[str, Any], category: str, order_index: int) -> Tuple[bool, str]:
        """Update item in place and return (structure_key_changed, previous_status)."""
        prev_status = self.status
        prev_cat = self.category
        prev_name = self.display_name

        self.raw = job
        self.category = category
        self.order_index = order_index
        self.display_name = display_name_for_job(job)
        self.status = str(job.get("status") or "").upper()
        self.progress = float(job.get("progress") or 0.0)
        self.speed_bps = parse_speed_bps(job.get("speed"))
        self.size_bytes = parse_byte_string(job.get("total_size"))

        structure_changed = (
            prev_status != self.status
            or prev_cat != self.category
            or prev_name != self.display_name
        )
        self.emit("updated")
        return structure_changed, prev_status


class DownloadRowWidget(Gtk.Box):
    """Graphical list row rendering icon, title, progress bar, speed, ETA, status pill, and quick action."""

    def __init__(
        self,
        on_quick_action: Any = None,
        on_context_menu: Any = None,
    ) -> None:
        super().__init__(orientation=Gtk.Orientation.HORIZONTAL, spacing=12)
        self.add_css_class("download-row-box")

        self._bound_item: Optional[JobListItem] = None
        self._signal_id: int = 0
        self._last_status_css = ""
        self._last_prog_css = ""
        self._on_quick_action = on_quick_action
        self._on_context_menu = on_context_menu
        self._quick_mode = ""

        # File category icon
        self.file_icon = Gtk.Image.new_from_icon_name("text-x-generic-symbolic")
        self.file_icon.set_pixel_size(28)
        self.file_icon.set_valign(Gtk.Align.CENTER)
        self.append(self.file_icon)

        # File title + metadata subtitle (host, dir, streams/error)
        name_col = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2)
        name_col.set_hexpand(True)
        name_col.set_valign(Gtk.Align.CENTER)

        self.name_label = Gtk.Label(xalign=0.0)
        self.name_label.add_css_class("job-title-label")
        self.name_label.set_ellipsize(Pango.EllipsizeMode.MIDDLE)

        self.meta_label = Gtk.Label(xalign=0.0)
        self.meta_label.add_css_class("job-subtitle-label")
        self.meta_label.set_ellipsize(Pango.EllipsizeMode.END)

        name_col.append(self.name_label)
        name_col.append(self.meta_label)
        self.append(name_col)

        # Transferred / total size
        self.size_label = Gtk.Label(xalign=1.0)
        self.size_label.add_css_class("tabular-metric")
        self.size_label.set_width_chars(19)
        self.size_label.set_ellipsize(Pango.EllipsizeMode.END)
        self.size_label.set_valign(Gtk.Align.CENTER)
        self.append(self.size_label)

        # Graphical ProgressBar + exact %
        prog_col = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4)
        prog_col.set_size_request(165, -1)
        prog_col.set_valign(Gtk.Align.CENTER)

        self.progress_bar = Gtk.ProgressBar()
        self.progress_bar.add_css_class("compact")

        self.pct_label = Gtk.Label(xalign=1.0)
        self.pct_label.add_css_class("job-subtitle-label")
        self.pct_label.add_css_class("tabular-metric")

        prog_col.append(self.progress_bar)
        prog_col.append(self.pct_label)
        self.append(prog_col)

        # Speed & ETA
        speed_col = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2)
        speed_col.set_size_request(110, -1)
        speed_col.set_valign(Gtk.Align.CENTER)

        self.speed_label = Gtk.Label(xalign=1.0)
        self.speed_label.add_css_class("metric-strong")

        self.eta_label = Gtk.Label(xalign=1.0)
        self.eta_label.add_css_class("job-subtitle-label")
        self.eta_label.add_css_class("tabular-metric")

        speed_col.append(self.speed_label)
        speed_col.append(self.eta_label)
        self.append(speed_col)

        # Status badge pill
        pill_wrap = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL)
        pill_wrap.set_size_request(106, -1)
        pill_wrap.set_halign(Gtk.Align.END)
        pill_wrap.set_valign(Gtk.Align.CENTER)

        self.status_badge = Gtk.Label()
        self.status_badge.add_css_class("status-pill")
        self.status_badge.set_halign(Gtk.Align.END)
        pill_wrap.append(self.status_badge)
        self.append(pill_wrap)

        # Inline quick-action button
        self.quick_action_btn = Gtk.Button()
        self.quick_action_btn.add_css_class("flat")
        self.quick_action_btn.set_valign(Gtk.Align.CENTER)
        self.quick_action_btn.connect("clicked", self._handle_quick_click)
        self.append(self.quick_action_btn)

        # Right-click context menu gesture
        right_click = Gtk.GestureClick()
        right_click.set_button(3)
        right_click.connect("pressed", self._handle_right_click)
        self.add_controller(right_click)

    def _handle_quick_click(self, *_args: Any) -> None:
        if self._bound_item is not None and self._on_quick_action is not None and self._quick_mode:
            self._on_quick_action(self._bound_item.job_id, self._quick_mode)

    def _handle_right_click(self, _gesture: Gtk.GestureClick, _n_press: int, x: float, y: float) -> None:
        if self._bound_item is not None and self._on_context_menu is not None:
            self._on_context_menu(self, self._bound_item.job_id, x, y)

    def bind(self, item: JobListItem) -> None:
        self.unbind()
        self._bound_item = item
        self._signal_id = item.connect("updated", self._on_item_updated)
        self._render(item)

    def unbind(self) -> None:
        if self._bound_item is not None and self._signal_id:
            try:
                self._bound_item.disconnect(self._signal_id)
            except Exception:
                pass
        self._bound_item = None
        self._signal_id = 0

    def _on_item_updated(self, item: JobListItem) -> None:
        self._render(item)

    def _render(self, item: JobListItem) -> None:
        job = item.raw
        status = item.status
        info = status_info(status)
        pct = max(0.0, min(100.0, item.progress))

        self.file_icon.set_from_icon_name(category_icon_name(item.category))
        self.name_label.set_text(item.display_name)

        # Informative subtitle: host • destination dir • stream info (or failure reason)
        err_msg = str(job.get("error_message") or "").strip()
        if status == "FAILED" and err_msg:
            subtitle = f"Error: {err_msg}"
        else:
            parts: List[str] = [item.category]
            host = url_host(str(job.get("url") or ""))
            if host:
                parts.append(host)
            save_path = str(job.get("save_path") or "").strip()
            if save_path and save_path not in {"PENDING", "DEFAULT"}:
                parent_dir = os.path.dirname(save_path)
                if parent_dir:
                    parts.append(parent_dir)
            chunks = job.get("chunks") or []
            if chunks:
                parts.append(f"{len(chunks)} streams")
            subtitle = "  •  ".join(parts)
        self.meta_label.set_text(subtitle)

        downloaded = str(job.get("downloaded") or "0 B")
        total = effective_total_size_str(job)
        self.size_label.set_text(f"{downloaded} / {total}")

        self.progress_bar.set_fraction(pct / 100.0)
        prog_css = info["progress_css"]
        if self._last_prog_css != prog_css:
            if self._last_prog_css:
                self.progress_bar.remove_css_class(self._last_prog_css)
            self.progress_bar.add_css_class(prog_css)
            self._last_prog_css = prog_css

        cstats = compute_chunk_stats(job.get("chunks"), status, pct)
        if cstats["has_chunks"]:
            self.pct_label.set_text(
                f"{pct:.1f}%  ({cstats['completed_chunks']}/{cstats['total_chunks']} seg)"
            )
        else:
            self.pct_label.set_text(f"{pct:.1f}%")

        speed = str(job.get("speed") or "0.00 KB/s")
        self.speed_label.set_text(speed if status == "DOWNLOADING" else "--")
        eta = str(job.get("eta") or "--")
        eta_display = "--" if status == "COMPLETED" else (f"ETA {eta}" if status == "DOWNLOADING" and eta != "--" else eta)
        self.eta_label.set_text(eta_display)

        self.status_badge.set_text(info["label"])
        badge_css = info["css_class"]
        if self._last_status_css != badge_css:
            if self._last_status_css:
                self.status_badge.remove_css_class(self._last_status_css)
            self.status_badge.add_css_class(badge_css)
            self._last_status_css = badge_css

        if status in {"DOWNLOADING", "QUEUED", "SCHEDULED"}:
            self._quick_mode = "pause"
            self.quick_action_btn.set_icon_name("media-playback-pause-symbolic")
            self.quick_action_btn.set_tooltip_text("Pause download")
            self.quick_action_btn.set_sensitive(True)
        elif status == "PAUSED":
            self._quick_mode = "resume"
            self.quick_action_btn.set_icon_name("media-playback-start-symbolic")
            self.quick_action_btn.set_tooltip_text("Resume download")
            self.quick_action_btn.set_sensitive(True)
        elif status == "FAILED":
            self._quick_mode = "retry"
            self.quick_action_btn.set_icon_name("view-refresh-symbolic")
            self.quick_action_btn.set_tooltip_text("Retry failed download")
            self.quick_action_btn.set_sensitive(True)
        elif status == "PENDING_PATH":
            self._quick_mode = "edit_dest"
            self.quick_action_btn.set_icon_name("document-edit-symbolic")
            self.quick_action_btn.set_tooltip_text("Set destination folder & filename")
            self.quick_action_btn.set_sensitive(True)
        elif status == "COMPLETED":
            self._quick_mode = "open_folder"
            self.quick_action_btn.set_icon_name("folder-open-symbolic")
            self.quick_action_btn.set_tooltip_text("Open containing folder")
            self.quick_action_btn.set_sensitive(True)
        else:
            self._quick_mode = ""
            self.quick_action_btn.set_sensitive(False)


class _SidebarFilterRow(Gtk.ListBoxRow):
    """Navigation sidebar row with icon, title, and live job count badge."""

    def __init__(self, kind: str, key: str, title: str, icon_name: str) -> None:
        super().__init__()
        self.filter_kind = kind
        self.filter_key = key
        self.add_css_class("sidebar-filter-row")

        box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        icon = Gtk.Image.new_from_icon_name(icon_name)
        label = Gtk.Label(label=title, xalign=0.0)
        label.set_hexpand(True)
        label.set_ellipsize(Pango.EllipsizeMode.END)

        self.badge = Gtk.Label(label="0")
        self.badge.add_css_class("count-badge")

        box.append(icon)
        box.append(label)
        box.append(self.badge)
        self.set_child(box)

    def set_count(self, count: int) -> None:
        self.badge.set_text(str(count))


class HydraWindow(Adw.ApplicationWindow):
    """Primary graphical window for inspecting downloads and monitoring live progress."""

    def __init__(
        self,
        app: Adw.Application,
        client: HydraGUIClient,
        anchor_path: Optional[Path] = None,
        initial_job_id: str = "",
        poll_interval_sec: float = 0.75,
        open_new_download: bool = False,
        initial_url: str = "",
    ) -> None:
        super().__init__(application=app)
        self.client = client
        self.anchor_path = anchor_path
        self._pending_select_job_id = initial_job_id
        self._base_poll_ms = max(400, int(poll_interval_sec * 1000))
        self._open_new_on_launch = open_new_download
        self._initial_url = initial_url

        self.set_title("Hydra Downloader")
        self.set_default_size(1160, 740)
        self.set_size_request(760, 480)

        self._status_filter = "all"
        self._category_filter = "all"
        self._search_query = ""
        self._sort_by = "added"
        self._sort_desc = True
        self._custom_filter: Optional[Gtk.CustomFilter] = None
        self._custom_sorter: Optional[Gtk.CustomSorter] = None

        self._online = False
        self._last_error = ""
        self._raw_jobs: List[Dict[str, Any]] = []
        self._jobs_by_id: Dict[str, Dict[str, Any]] = {}
        self._latest_settings: Optional[Dict[str, Any]] = None
        self._custom_categories: Optional[Dict[str, Any]] = None
        self._speed_tracker = SpeedHistoryTracker(max_samples=60)
        self._items_by_id: Dict[str, JobListItem] = {}
        self._popout_windows: Dict[str, LiveProgressWindow] = {}
        self._poll_timer_id: int = 0
        self._refresh_in_flight = False

        # Toast overlay wrapping entire window content
        self._toast_overlay = Adw.ToastOverlay()
        toolbar_view = Adw.ToolbarView()
        self._toast_overlay.set_child(toolbar_view)
        self.set_content(self._toast_overlay)

        # ── HeaderBar ──
        header = Adw.HeaderBar()
        toolbar_view.add_top_bar(header)

        self._sidebar_toggle = Gtk.ToggleButton()
        self._sidebar_toggle.set_icon_name("view-sidebar-symbolic")
        self._sidebar_toggle.set_active(True)
        self._sidebar_toggle.set_tooltip_text("Toggle categories sidebar")
        header.pack_start(self._sidebar_toggle)

        new_btn = Gtk.Button()
        new_btn_box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=6)
        new_btn_box.append(Gtk.Image.new_from_icon_name("list-add-symbolic"))
        new_btn_box.append(Gtk.Label(label="New Download"))
        new_btn.set_child(new_btn_box)
        new_btn.add_css_class("suggested-action")
        new_btn.set_tooltip_text("Add a new download (Ctrl+N)")
        new_btn.connect("clicked", lambda *_: self.open_new_download_dialog())
        header.pack_start(new_btn)

        brand_box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        icon_name, icon_svg = resolve_icon_paths(self.anchor_path)
        if icon_svg:
            brand_img = Gtk.Image.new_from_file(icon_svg)
        else:
            brand_img = Gtk.Image.new_from_icon_name(icon_name)
        brand_img.set_pixel_size(20)
        brand_title = Gtk.Label(label="Hydra")
        brand_title.add_css_class("title")
        brand_box.append(brand_img)
        brand_box.append(brand_title)
        header.pack_start(brand_box)

        self._status_pill = Gtk.Label(label="Connecting...")
        self._status_pill.add_css_class("header-status-pill")
        self._status_pill.add_css_class("header-status-offline")
        header.set_title_widget(self._status_pill)

        open_dl_btn = Gtk.Button.new_from_icon_name("folder-open-symbolic")
        open_dl_btn.set_tooltip_text("Open Downloads folder")
        open_dl_btn.connect("clicked", self._on_open_downloads_folder)
        header.pack_end(open_dl_btn)

        refresh_btn = Gtk.Button.new_from_icon_name("view-refresh-symbolic")
        refresh_btn.set_tooltip_text("Refresh download list (Ctrl+R)")
        refresh_btn.connect("clicked", lambda *_: self.trigger_refresh(fetch_settings=True))
        header.pack_end(refresh_btn)

        self._inspector_toggle = Gtk.ToggleButton()
        self._inspector_toggle.set_icon_name("view-dual-symbolic")
        self._inspector_toggle.set_active(True)
        self._inspector_toggle.set_tooltip_text("Toggle details & progress pane")
        self._inspector_toggle.connect("toggled", self._on_toggle_inspector)
        header.pack_end(self._inspector_toggle)

        self._resume_all_btn = Gtk.Button.new_from_icon_name("media-playback-start-symbolic")
        self._resume_all_btn.set_tooltip_text("Resume all paused downloads")
        self._resume_all_btn.set_sensitive(False)
        self._resume_all_btn.connect("clicked", lambda *_: self._pause_or_resume_all(resume=True))
        header.pack_end(self._resume_all_btn)

        self._pause_all_btn = Gtk.Button.new_from_icon_name("media-playback-pause-symbolic")
        self._pause_all_btn.set_tooltip_text("Pause all active/queued downloads")
        self._pause_all_btn.set_sensitive(False)
        self._pause_all_btn.connect("clicked", lambda *_: self._pause_or_resume_all(resume=False))
        header.pack_end(self._pause_all_btn)

        # ── Navigation Split View (Sidebar + Content) ──
        self._split_view = Adw.OverlaySplitView()
        self._split_view.set_min_sidebar_width(210)
        self._split_view.set_max_sidebar_width(260)
        self._split_view.set_show_sidebar(True)
        self._sidebar_toggle.connect(
            "toggled",
            lambda btn: self._split_view.set_show_sidebar(btn.get_active()),
        )
        toolbar_view.set_content(self._split_view)

        # Sidebar
        self._sidebar_rows: Dict[Tuple[str, str], _SidebarFilterRow] = {}
        self._split_view.set_sidebar(self._build_sidebar())

        # Main Vertical Paned: Top List Pane + Bottom Details & Progress Inspector
        self._paned = Gtk.Paned(orientation=Gtk.Orientation.VERTICAL)
        self._paned.set_wide_handle(True)
        self._paned.set_resize_start_child(True)
        self._paned.set_resize_end_child(True)
        self._paned.set_shrink_start_child(False)
        self._paned.set_shrink_end_child(False)
        self._paned.set_position(360)

        self._paned.set_start_child(self._build_list_pane())

        self._details_view = DownloadDetailsView(
            on_copy_text=self._copy_to_clipboard,
            on_open_folder=self._open_job_folder,
            on_popout=self._open_popout_window,
            on_edit_destination=self._open_edit_destination_dialog,
        )
        self._paned.set_end_child(self._details_view)
        self._split_view.set_content(self._paned)

        # Keyboard shortcuts controller
        key_ctrl = Gtk.EventControllerKey()
        key_ctrl.connect("key-pressed", self._on_key_pressed)
        self.add_controller(key_ctrl)

        # Real-time SSE listener + Adaptive Poll Timer
        self._sse_watcher = SSEWatcher(self.client, self._on_sse_jobs)
        self._sse_watcher.start()
        self.trigger_refresh(fetch_settings=True)
        self._schedule_next_poll()
        self.connect("close-request", self._on_close_request)

        if self._open_new_on_launch:
            GLib.idle_add(self._open_initial_new_download)

    def _build_sidebar(self) -> Gtk.Widget:
        scroll = Gtk.ScrolledWindow()
        scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        scroll.add_css_class("hydra-sidebar")

        container = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=4)
        container.set_margin_top(6)
        container.set_margin_bottom(12)
        scroll.set_child(container)

        sec1 = Gtk.Label(label="FILTERS", xalign=0.0)
        sec1.add_css_class("sidebar-section-label")
        container.append(sec1)

        self._status_listbox = Gtk.ListBox()
        self._status_listbox.set_selection_mode(Gtk.SelectionMode.SINGLE)
        self._status_listbox.add_css_class("navigation-sidebar")
        self._status_listbox.connect("row-selected", self._on_status_filter_selected)

        for key, label, icon in STATUS_FILTERS:
            row = _SidebarFilterRow("status", key, label, icon)
            self._status_rows_insert(self._status_listbox, "status", key, row)

        self._status_listbox.select_row(self._sidebar_rows[("status", "all")])
        container.append(self._status_listbox)

        sec2 = Gtk.Label(label="CATEGORIES", xalign=0.0)
        sec2.add_css_class("sidebar-section-label")
        container.append(sec2)

        self._category_listbox = Gtk.ListBox()
        self._category_listbox.set_selection_mode(Gtk.SelectionMode.SINGLE)
        self._category_listbox.add_css_class("navigation-sidebar")
        self._category_listbox.connect("row-selected", self._on_category_filter_selected)

        all_cat_row = _SidebarFilterRow("category", "all", "All File Types", "folder-symbolic")
        self._status_rows_insert(self._category_listbox, "category", "all", all_cat_row)
        for cat in CATEGORY_ORDER:
            row = _SidebarFilterRow("category", cat, cat, category_icon_name(cat))
            self._status_rows_insert(self._category_listbox, "category", cat, row)

        self._category_listbox.select_row(self._sidebar_rows[("category", "all")])
        container.append(self._category_listbox)

        return scroll

    def _status_rows_insert(self, listbox: Gtk.ListBox, kind: str, key: str, row: _SidebarFilterRow) -> None:
        self._sidebar_rows[(kind, key)] = row
        listbox.append(row)

    def _build_list_pane(self) -> Gtk.Widget:
        box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=0)

        # Offline recovery banner
        self._offline_banner = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        self._offline_banner.add_css_class("offline-banner")
        warn_icon = Gtk.Image.new_from_icon_name("network-offline-symbolic")
        self._offline_label = Gtk.Label(
            label="Hydra background engine is offline. Start it to track live downloads.",
            xalign=0.0,
        )
        self._offline_label.set_hexpand(True)
        self._offline_label.set_ellipsize(Pango.EllipsizeMode.END)

        start_engine_btn = Gtk.Button(label="Start Background Engine")
        start_engine_btn.add_css_class("suggested-action")
        start_engine_btn.connect("clicked", self._on_start_engine_clicked)

        retry_btn = Gtk.Button(label="Retry")
        retry_btn.connect("clicked", lambda *_: self.trigger_refresh(fetch_settings=True))

        self._offline_banner.append(warn_icon)
        self._offline_banner.append(self._offline_label)
        self._offline_banner.append(start_engine_btn)
        self._offline_banner.append(retry_btn)
        self._offline_banner.set_visible(False)
        box.append(self._offline_banner)

        # Toolbar: Search + Sort + Visible Count
        toolbar = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=10)
        toolbar.add_css_class("summary-toolbar")

        self._search_entry = Gtk.SearchEntry()
        self._search_entry.set_placeholder_text("Search downloads by name, URL, folder, or batch...")
        self._search_entry.set_hexpand(True)
        self._search_entry.connect("search-changed", self._on_search_changed)
        toolbar.append(self._search_entry)

        sort_labels = [label for _, label in SORT_OPTIONS]
        self._sort_dropdown = Gtk.DropDown.new_from_strings(sort_labels)
        self._sort_dropdown.set_selected(0)
        self._sort_dropdown.connect("notify::selected", self._on_sort_changed)
        toolbar.append(self._sort_dropdown)

        self._sort_dir_btn = Gtk.ToggleButton()
        self._sort_dir_btn.set_icon_name("view-sort-descending-symbolic")
        self._sort_dir_btn.set_active(True)
        self._sort_dir_btn.set_tooltip_text("Toggle ascending / descending order")
        self._sort_dir_btn.connect("toggled", self._on_sort_dir_toggled)
        toolbar.append(self._sort_dir_btn)

        self._visible_count_label = Gtk.Label(label="0 shown")
        self._visible_count_label.add_css_class("count-badge")
        toolbar.append(self._visible_count_label)
        box.append(toolbar)

        # Selection Action Bar (Pause, Resume, Retry, Destination/Rename, URL, Cancel, Delete)
        action_bar = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=6)
        action_bar.add_css_class("action-strip")

        self._act_pause = self._make_action_button(
            "Pause", "media-playback-pause-symbolic", "Pause selected download",
            lambda *_: self._selected_job_id() and self._pause_job(self._selected_job_id()),
        )
        self._act_resume = self._make_action_button(
            "Resume", "media-playback-start-symbolic", "Resume paused download",
            lambda *_: self._selected_job_id() and self._resume_job(self._selected_job_id()),
        )
        self._act_retry = self._make_action_button(
            "Retry", "view-refresh-symbolic", "Retry failed download",
            lambda *_: self._selected_job_id() and self._retry_job(self._selected_job_id()),
        )
        self._act_dest = self._make_action_button(
            "Destination / Rename", "document-edit-symbolic", "Set destination folder & filename (F2)",
            lambda *_: self._selected_job_id() and self._open_edit_destination_dialog(self._selected_job_id()),
        )
        self._act_url = self._make_action_button(
            "Update URL", "insert-link-symbolic", "Replace expired link and retry",
            lambda *_: self._selected_job_id() and self._open_update_url_dialog(self._selected_job_id()),
        )
        self._act_cancel = self._make_action_button(
            "Cancel", "process-stop-symbolic", "Abort transfer and discard partial data",
            lambda *_: self._selected_job_id() and self._request_remove_job(self._selected_job_id(), "cancel"),
        )
        self._act_delete = self._make_action_button(
            "Delete", "user-trash-symbolic", "Delete download and file (Del)",
            lambda *_: self._selected_job_id() and self._request_remove_job(self._selected_job_id(), "delete"),
        )
        self._act_delete.add_css_class("destructive-action")

        spacer = Gtk.Box()
        spacer.set_hexpand(True)

        action_bar.append(self._act_pause)
        action_bar.append(self._act_resume)
        action_bar.append(self._act_retry)
        action_bar.append(Gtk.Separator(orientation=Gtk.Orientation.VERTICAL))
        action_bar.append(self._act_dest)
        action_bar.append(self._act_url)
        action_bar.append(spacer)
        action_bar.append(self._act_cancel)
        action_bar.append(self._act_delete)
        box.append(action_bar)

        # Column Header Row
        col_hdr = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=12)
        col_hdr.add_css_class("download-list-header")

        h_spacer = Gtk.Box()
        h_spacer.set_size_request(28, -1)
        col_hdr.append(h_spacer)

        h_name = Gtk.Label(label="FILE", xalign=0.0)
        h_name.set_hexpand(True)
        col_hdr.append(h_name)

        h_size = Gtk.Label(label="SIZE", xalign=1.0)
        h_size.set_width_chars(19)
        col_hdr.append(h_size)

        h_prog = Gtk.Label(label="PROGRESS", xalign=1.0)
        h_prog.set_size_request(165, -1)
        col_hdr.append(h_prog)

        h_speed = Gtk.Label(label="SPEED / ETA", xalign=1.0)
        h_speed.set_size_request(110, -1)
        col_hdr.append(h_speed)

        h_status = Gtk.Label(label="STATUS", xalign=1.0)
        h_status.set_size_request(106, -1)
        col_hdr.append(h_status)

        h_act_spacer = Gtk.Box()
        h_act_spacer.set_size_request(34, -1)
        col_hdr.append(h_act_spacer)

        box.append(col_hdr)

        # Stack: ListView vs Empty State
        self._list_stack = Gtk.Stack()
        self._list_stack.set_vexpand(True)
        box.append(self._list_stack)

        # Observable Gtk.ListView Pipeline
        self._store = Gio.ListStore.new(JobListItem)
        self._custom_filter = Gtk.CustomFilter.new(self._filter_func)
        self._filter_model = Gtk.FilterListModel.new(self._store, self._custom_filter)
        self._custom_sorter = Gtk.CustomSorter.new(self._sort_cmp_func)
        self._sort_model = Gtk.SortListModel.new(self._filter_model, self._custom_sorter)
        self._selection = Gtk.SingleSelection.new(self._sort_model)
        self._selection.set_autoselect(False)
        self._selection.set_can_unselect(True)
        self._selection.connect("notify::selected-item", self._on_selection_changed)

        factory = Gtk.SignalListItemFactory()
        factory.connect("setup", self._on_row_setup)
        factory.connect("bind", self._on_row_bind)
        factory.connect("unbind", self._on_row_unbind)
        factory.connect("teardown", self._on_row_teardown)

        self._list_view = Gtk.ListView.new(self._selection, factory)
        self._list_view.add_css_class("download-listview")
        self._list_view.set_single_click_activate(False)
        self._list_view.connect("activate", self._on_row_activated)

        list_scroll = Gtk.ScrolledWindow()
        list_scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        list_scroll.set_vexpand(True)
        list_scroll.set_child(self._list_view)

        self._list_stack.add_named(list_scroll, "list")

        self._empty_list_page = Adw.StatusPage()
        self._empty_list_page.set_icon_name("folder-download-symbolic")
        self._empty_list_page.set_title("No Downloads Yet")
        self._empty_list_page.set_description(
            "Downloads started via the Firefox extension, hydra CLI, or TUI will appear here live."
        )
        self._list_stack.add_named(self._empty_list_page, "empty")

        return box

    def _on_row_setup(self, _factory: Gtk.SignalListItemFactory, list_item: Gtk.ListItem) -> None:
        list_item.set_child(
            DownloadRowWidget(
                on_quick_action=self._handle_row_quick_action,
                on_context_menu=self._open_row_context_menu,
            )
        )

    def _on_row_bind(self, _factory: Gtk.SignalListItemFactory, list_item: Gtk.ListItem) -> None:
        widget = list_item.get_child()
        item = list_item.get_item()
        if isinstance(widget, DownloadRowWidget) and isinstance(item, JobListItem):
            widget.bind(item)

    def _on_row_unbind(self, _factory: Gtk.SignalListItemFactory, list_item: Gtk.ListItem) -> None:
        widget = list_item.get_child()
        if isinstance(widget, DownloadRowWidget):
            widget.unbind()

    def _on_row_teardown(self, _factory: Gtk.SignalListItemFactory, list_item: Gtk.ListItem) -> None:
        widget = list_item.get_child()
        if isinstance(widget, DownloadRowWidget):
            widget.unbind()

    def _filter_func(self, item: JobListItem) -> bool:
        if not matches_status_filter(item.status, self._status_filter):
            return False
        if self._category_filter != "all" and item.category != self._category_filter:
            return False
        if self._search_query:
            job = item.raw
            haystack = " ".join(
                [
                    item.display_name,
                    str(job.get("file_name") or ""),
                    str(job.get("url") or ""),
                    str(job.get("save_path") or ""),
                    str(job.get("batch_id") or ""),
                    item.job_id,
                    item.status,
                    item.category,
                ]
            ).lower()
            if self._search_query not in haystack:
                return False
        return True

    def _sort_cmp_func(self, a: JobListItem, b: JobListItem, *_args: Any) -> int:
        mode = self._sort_by
        if mode == "added":
            ka: Any = (str(a.raw.get("created_at") or ""), a.order_index)
            kb: Any = (str(b.raw.get("created_at") or ""), b.order_index)
        elif mode == "name":
            ka = a.display_name.lower()
            kb = b.display_name.lower()
        elif mode == "progress":
            ka = a.progress
            kb = b.progress
        elif mode == "speed":
            ka = a.speed_bps
            kb = b.speed_bps
        elif mode == "size":
            ka = a.size_bytes
            kb = b.size_bytes
        elif mode == "status":
            ka = (status_info(a.status).get("rank", 99), a.display_name.lower())
            kb = (status_info(b.status).get("rank", 99), b.display_name.lower())
        else:
            ka = a.order_index
            kb = b.order_index

        if ka < kb:
            cmp_val = -1
        elif ka > kb:
            cmp_val = 1
        else:
            cmp_val = -1 if a.order_index < b.order_index else (1 if a.order_index > b.order_index else 0)

        return -cmp_val if self._sort_desc else cmp_val

    def _on_status_filter_selected(self, _listbox: Gtk.ListBox, row: Optional[Gtk.ListBoxRow]) -> None:
        if isinstance(row, _SidebarFilterRow):
            self._status_filter = row.filter_key
            self._apply_filter_change()

    def _on_category_filter_selected(self, _listbox: Gtk.ListBox, row: Optional[Gtk.ListBoxRow]) -> None:
        if isinstance(row, _SidebarFilterRow):
            self._category_filter = row.filter_key
            self._apply_filter_change()

    def _on_search_changed(self, entry: Gtk.SearchEntry) -> None:
        self._search_query = entry.get_text().strip().lower()
        self._apply_filter_change()

    def _on_sort_changed(self, dropdown: Gtk.DropDown, *_args: Any) -> None:
        idx = dropdown.get_selected()
        if 0 <= idx < len(SORT_OPTIONS):
            self._sort_by = SORT_OPTIONS[idx][0]
            if getattr(self, "_custom_sorter", None) is not None:
                self._custom_sorter.changed(Gtk.SorterChange.DIFFERENT)
                self._sync_selection_and_empty_state()

    def _on_sort_dir_toggled(self, btn: Gtk.ToggleButton) -> None:
        self._sort_desc = btn.get_active()
        btn.set_icon_name(
            "view-sort-descending-symbolic" if self._sort_desc else "view-sort-ascending-symbolic"
        )
        if getattr(self, "_custom_sorter", None) is not None:
            self._custom_sorter.changed(Gtk.SorterChange.INVERTED)

    def _on_toggle_inspector(self, btn: Gtk.ToggleButton) -> None:
        self._details_view.set_visible(btn.get_active())

    def _apply_filter_change(self) -> None:
        if getattr(self, "_custom_filter", None) is not None:
            self._custom_filter.changed(Gtk.FilterChange.DIFFERENT)
            self._sync_selection_and_empty_state()

    def _on_selection_changed(self, selection: Gtk.SingleSelection, *_args: Any) -> None:
        item = selection.get_selected_item()
        if isinstance(item, JobListItem):
            self._details_view.update_job(
                item.raw,
                self._speed_tracker.get_samples(item.job_id),
                self._custom_categories,
            )
            self._update_action_controls(item.raw)
        else:
            self._details_view.clear()
            self._update_action_controls(None)

    def _on_row_activated(self, _list_view: Gtk.ListView, position: int) -> None:
        item = self._sort_model.get_item(position)
        if isinstance(item, JobListItem) and item.job_id:
            self._open_popout_window(item.job_id)

    def _open_popout_window(self, job_id: str) -> None:
        existing = self._popout_windows.get(job_id)
        if existing is not None:
            existing.present()
            return

        win = LiveProgressWindow(
            parent=self,
            job_id=job_id,
            on_open_folder=self._open_job_folder,
            on_closed=lambda jid: self._popout_windows.pop(jid, None),
            on_pause=self._pause_job,
            on_resume=self._resume_job,
            on_retry=self._retry_job,
            on_cancel=lambda jid: self._request_remove_job(jid, "cancel"),
        )
        self._popout_windows[job_id] = win
        job = self._jobs_by_id.get(job_id)
        win.update_job(job, self._speed_tracker.get_samples(job_id))
        win.present()

    def _make_action_button(
        self,
        label: str,
        icon_name: str,
        tooltip: str,
        callback: Any,
    ) -> Gtk.Button:
        btn = Gtk.Button()
        box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=5)
        box.append(Gtk.Image.new_from_icon_name(icon_name))
        box.append(Gtk.Label(label=label))
        btn.set_child(box)
        btn.add_css_class("flat")
        btn.set_tooltip_text(tooltip)
        btn.set_sensitive(False)
        btn.connect("clicked", callback)
        return btn

    def _selected_job_id(self) -> str:
        item = self._selection.get_selected_item()
        return item.job_id if isinstance(item, JobListItem) else ""

    def _update_action_controls(self, job: Optional[Dict[str, Any]]) -> None:
        caps = available_job_actions(job)
        self._act_pause.set_sensitive(caps["can_pause"])
        self._act_resume.set_sensitive(caps["can_resume"])
        self._act_retry.set_sensitive(caps["can_retry"])
        self._act_dest.set_sensitive(caps["can_edit_destination"])
        self._act_url.set_sensitive(caps["can_edit_url"])
        self._act_cancel.set_sensitive(caps["can_cancel"])
        self._act_delete.set_sensitive(caps["can_delete"])

    def _handle_row_quick_action(self, job_id: str, mode: str) -> None:
        if mode == "pause":
            self._pause_job(job_id)
        elif mode == "resume":
            self._resume_job(job_id)
        elif mode == "retry":
            self._retry_job(job_id)
        elif mode == "edit_dest":
            self._open_edit_destination_dialog(job_id)
        elif mode == "open_folder":
            job = self._jobs_by_id.get(job_id)
            if job and job.get("save_path"):
                self._open_job_folder(str(job["save_path"]))

    def _open_row_context_menu(self, row_widget: Gtk.Widget, job_id: str, x: float, y: float) -> None:
        job = self._jobs_by_id.get(job_id)
        if job is None:
            return

        # Select the right-clicked row cleanly
        n_items = self._sort_model.get_n_items()
        for pos in range(n_items):
            it = self._sort_model.get_item(pos)
            if isinstance(it, JobListItem) and it.job_id == job_id:
                self._selection.set_selected(pos)
                break

        caps = available_job_actions(job)
        popover = Gtk.Popover()
        popover.set_parent(row_widget)
        popover.set_has_arrow(False)

        rect = Gdk.Rectangle()
        rect.x = int(x)
        rect.y = int(y)
        rect.width = 1
        rect.height = 1
        popover.set_pointing_to(rect)

        menu_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=2)
        menu_box.set_margin_top(6)
        menu_box.set_margin_bottom(6)
        menu_box.set_margin_start(6)
        menu_box.set_margin_end(6)

        def add_menu_item(title: str, icon_name: str, enabled: bool, handler: Any, destructive: bool = False) -> None:
            btn = Gtk.Button()
            b = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
            b.append(Gtk.Image.new_from_icon_name(icon_name))
            lbl = Gtk.Label(label=title, xalign=0.0)
            b.append(lbl)
            btn.set_child(b)
            btn.add_css_class("flat")
            if destructive:
                btn.add_css_class("destructive-action")
            btn.set_sensitive(enabled)

            def _on_click(*_args: Any) -> None:
                popover.popdown()
                handler()

            btn.connect("clicked", _on_click)
            menu_box.append(btn)

        add_menu_item("Pause", "media-playback-pause-symbolic", caps["can_pause"], lambda: self._pause_job(job_id))
        add_menu_item("Resume", "media-playback-start-symbolic", caps["can_resume"], lambda: self._resume_job(job_id))
        add_menu_item("Retry", "view-refresh-symbolic", caps["can_retry"], lambda: self._retry_job(job_id))
        add_menu_item(
            "Update URL & Retry...",
            "insert-link-symbolic",
            caps["can_edit_url"],
            lambda: self._open_update_url_dialog(job_id),
        )
        add_menu_item(
            "Set Destination & Filename...",
            "document-edit-symbolic",
            caps["can_edit_destination"],
            lambda: self._open_edit_destination_dialog(job_id),
        )
        menu_box.append(Gtk.Separator(orientation=Gtk.Orientation.HORIZONTAL))
        add_menu_item(
            "Live Progress Window",
            "window-new-symbolic",
            True,
            lambda: self._open_popout_window(job_id),
        )
        add_menu_item(
            "Open Containing Folder",
            "folder-open-symbolic",
            caps["can_open_folder"],
            lambda: self._open_job_folder(str(job.get("save_path") or "")),
        )
        add_menu_item(
            "Copy Download URL",
            "edit-copy-symbolic",
            bool(job.get("url")),
            lambda: self._copy_to_clipboard(str(job.get("url") or ""), "Download URL copied"),
        )
        menu_box.append(Gtk.Separator(orientation=Gtk.Orientation.HORIZONTAL))
        add_menu_item(
            "Cancel Download...",
            "process-stop-symbolic",
            caps["can_cancel"],
            lambda: self._request_remove_job(job_id, "cancel"),
        )
        add_menu_item(
            "Delete Download...",
            "user-trash-symbolic",
            caps["can_delete"],
            lambda: self._request_remove_job(job_id, "delete"),
            destructive=True,
        )

        popover.set_child(menu_box)
        popover.connect("closed", lambda pop: pop.unparent())
        popover.popup()

    def _run_job_action(self, fn: Any, success_msg: str, select_id: str = "") -> None:
        def worker() -> None:
            ok, err_msg = fn()

            def _done() -> bool:
                if ok:
                    if select_id:
                        self._pending_select_job_id = select_id
                    if success_msg:
                        self._show_toast(success_msg)
                    self.trigger_refresh(fetch_settings=False)
                else:
                    self._show_toast(f"Action failed: {err_msg or 'unknown error'}")
                return False

            GLib.idle_add(_done)

        threading.Thread(target=worker, daemon=True).start()

    def _pause_job(self, job_id: str) -> None:
        self._run_job_action(lambda: self.client.pause_job(job_id), "Download paused")

    def _resume_job(self, job_id: str) -> None:
        self._run_job_action(lambda: self.client.resume_job(job_id), "Download resumed")

    def _retry_job(self, job_id: str) -> None:
        self._run_job_action(lambda: self.client.resume_job(job_id), "Retrying download...")

    def _request_remove_job(self, job_id: str, mode: str) -> None:
        job = self._jobs_by_id.get(job_id)
        if job is None:
            return

        def _confirmed() -> None:
            toast = "Download cancelled" if mode == "cancel" else "Download deleted"
            self._run_job_action(lambda: self.client.delete_job(job_id), toast)

        confirm_job_removal_dialog(self, job, mode, _confirmed)

    def _pause_or_resume_all(self, resume: bool) -> None:
        def worker() -> None:
            if resume:
                ok, count, err = self.client.resume_all()
                msg = f"Queued {count} paused download(s)"
            else:
                ok, count, err = self.client.pause_all()
                msg = f"Paused {count} download(s)"

            def _done() -> bool:
                if ok:
                    self._show_toast(msg)
                    self.trigger_refresh(fetch_settings=False)
                else:
                    self._show_toast(f"Batch action failed: {err or 'unknown error'}")
                return False

            GLib.idle_add(_done)

        threading.Thread(target=worker, daemon=True).start()

    def open_new_download_dialog(self, initial_url: str = "") -> None:
        def _on_created(new_job_id: Optional[str]) -> None:
            if new_job_id:
                self._pending_select_job_id = new_job_id
            self._show_toast("Download queued")
            self.trigger_refresh(fetch_settings=True)

        dlg = NewDownloadDialog(
            parent=self,
            client=self.client,
            settings=self._latest_settings,
            on_created=_on_created,
            initial_url=initial_url,
        )
        dlg.present()

    def _open_edit_destination_dialog(self, job_id: str) -> None:
        job = self._jobs_by_id.get(job_id)
        if job is None:
            return

        def _on_saved(updated_id: str) -> None:
            self._pending_select_job_id = updated_id
            self._show_toast("Destination updated and queued")
            self.trigger_refresh(fetch_settings=False)

        dlg = EditDestinationDialog(
            parent=self,
            client=self.client,
            job=job,
            settings=self._latest_settings,
            on_saved=_on_saved,
        )
        dlg.present()

    def _open_update_url_dialog(self, job_id: str) -> None:
        job = self._jobs_by_id.get(job_id)
        if job is None:
            return

        def _on_retried(updated_id: str) -> None:
            self._pending_select_job_id = updated_id
            self._show_toast("URL updated and download retrying")
            self.trigger_refresh(fetch_settings=False)

        dlg = UpdateURLDialog(
            parent=self,
            client=self.client,
            job=job,
            on_retried=_on_retried,
        )
        dlg.present()

    def _open_initial_new_download(self) -> bool:
        self.open_new_download_dialog(self._initial_url)
        self._initial_url = ""
        return False

    def _on_key_pressed(
        self,
        _ctrl: Gtk.EventControllerKey,
        keyval: int,
        _keycode: int,
        state: Gdk.ModifierType,
    ) -> bool:
        ctrl_held = bool(state & Gdk.ModifierType.CONTROL_MASK)
        if ctrl_held and keyval in (Gdk.KEY_n, Gdk.KEY_N):
            self.open_new_download_dialog()
            return True
        if ctrl_held and keyval in (Gdk.KEY_r, Gdk.KEY_R) or keyval == Gdk.KEY_F5:
            self.trigger_refresh(fetch_settings=True)
            return True
        if keyval == Gdk.KEY_F2:
            jid = self._selected_job_id()
            if jid and available_job_actions(self._jobs_by_id.get(jid))["can_edit_destination"]:
                self._open_edit_destination_dialog(jid)
                return True
        if keyval == Gdk.KEY_Delete:
            focus = self.get_focus()
            if not isinstance(focus, (Gtk.Text, Gtk.SearchEntry, Gtk.Entry)):
                jid = self._selected_job_id()
                if jid:
                    caps = available_job_actions(self._jobs_by_id.get(jid))
                    mode = "cancel" if caps["can_cancel"] else "delete"
                    self._request_remove_job(jid, mode)
                    return True
        return False

    def _copy_to_clipboard(self, text: str, toast_msg: str) -> None:
        try:
            clipboard = self.get_clipboard()
            val = GObject.Value(GObject.TYPE_STRING, text)
            clipboard.set(val)
            self._show_toast(toast_msg)
        except Exception as exc:
            self._show_toast(f"Clipboard unavailable: {exc}")

    def _open_job_folder(self, path: str) -> None:
        if not open_path_in_desktop(path):
            self._show_toast("Folder does not exist on disk yet.")

    def _on_open_downloads_folder(self, *_args: Any) -> None:
        def worker() -> None:
            dl_dir = str(resolve_downloads_dir(self.client))

            def _open_on_main() -> bool:
                if not open_path_in_desktop(dl_dir):
                    self._show_toast("Downloads folder could not be opened.")
                return False

            GLib.idle_add(_open_on_main)

        threading.Thread(target=worker, daemon=True).start()

    def _on_start_engine_clicked(self, *_args: Any) -> None:
        self._show_toast("Starting Hydra background engine...")

        def worker() -> None:
            ok = ensure_local_daemon(self.client.base_url, self.anchor_path, timeout=4.0)
            GLib.idle_add(self._on_engine_started, ok)

        threading.Thread(target=worker, daemon=True).start()

    def _on_engine_started(self, ok: bool) -> bool:
        if ok:
            self._show_toast("Hydra background engine started.")
            self.trigger_refresh(fetch_settings=True)
        else:
            self._show_toast("Could not start local hydra-daemon automatically.")
        return False

    def _show_toast(self, message: str) -> None:
        toast = Adw.Toast.new(message)
        toast.set_timeout(2)
        self._toast_overlay.add_toast(toast)

    def _schedule_next_poll(self) -> None:
        if self._poll_timer_id:
            GLib.source_remove(self._poll_timer_id)
        has_active = any(
            str(j.get("status") or "").upper() in {"DOWNLOADING", "QUEUED"}
            for j in self._raw_jobs
        )
        interval_ms = self._base_poll_ms if has_active else max(1500, self._base_poll_ms * 2)
        self._poll_timer_id = GLib.timeout_add(interval_ms, self._on_poll_tick)

    def _on_poll_tick(self) -> bool:
        self.trigger_refresh(fetch_settings=False)
        self._schedule_next_poll()
        return False

    def trigger_refresh(self, fetch_settings: bool = False) -> None:
        if self._refresh_in_flight:
            return
        self._refresh_in_flight = True

        def worker() -> None:
            try:
                settings = self.client.get_settings() if fetch_settings else None
                online, jobs, err = self.client.get_jobs()
                GLib.idle_add(self._apply_daemon_snapshot, online, jobs, err, settings)
            finally:
                self._refresh_in_flight = False

        threading.Thread(target=worker, daemon=True).start()

    def _on_sse_jobs(self, jobs: List[Dict[str, Any]]) -> None:
        GLib.idle_add(self._apply_daemon_snapshot, True, jobs, "", None)

    def _apply_daemon_snapshot(
        self,
        online: bool,
        jobs: List[Dict[str, Any]],
        err: str,
        settings: Optional[Dict[str, Any]],
    ) -> bool:
        self._online = online
        self._last_error = err

        if settings and isinstance(settings, dict):
            self._latest_settings = settings
            if isinstance(settings.get("categories"), dict):
                self._custom_categories = settings["categories"]

        if online:
            self._raw_jobs = jobs
            self._jobs_by_id = {str(j.get("id") or ""): j for j in jobs if j.get("id")}
            self._speed_tracker.record_jobs(jobs)
            self._offline_banner.set_visible(False)
            self._sync_store(jobs)
        else:
            if err:
                self._offline_label.set_text(
                    f"Hydra background engine is offline ({err})."
                )
            self._offline_banner.set_visible(True)

        self._update_header_and_sidebar_badges()
        self._sync_selection_and_empty_state()
        self._update_popouts()
        return False

    def _sync_store(self, jobs: List[Dict[str, Any]]) -> None:
        selected_item = self._selection.get_selected_item()
        prev_selected_id = (
            selected_item.job_id if isinstance(selected_item, JobListItem) else ""
        )

        incoming_ids = {str(j.get("id") or "") for j in jobs if j.get("id")}
        list_mutated = False
        filter_needs_refresh = False
        sort_needs_refresh = False

        # Remove jobs that no longer exist
        removed_ids = [jid for jid in self._items_by_id if jid not in incoming_ids]
        if removed_ids:
            list_mutated = True
            for jid in removed_ids:
                item = self._items_by_id.pop(jid)
                found, idx = self._store.find(item)
                if found:
                    self._store.remove(idx)

        # Update existing jobs in place or append new jobs
        for idx, job in enumerate(jobs):
            jid = str(job.get("id") or "")
            if not jid:
                continue
            name = display_name_for_job(job)
            cat = infer_category(name, str(job.get("save_path") or ""), self._custom_categories)

            existing = self._items_by_id.get(jid)
            if existing is not None:
                prev_prog = existing.progress
                prev_speed = existing.speed_bps
                prev_size = existing.size_bytes
                struct_changed, _ = existing.apply(job, cat, idx)
                if struct_changed:
                    filter_needs_refresh = True
                    sort_needs_refresh = True
                elif (
                    (self._sort_by == "progress" and prev_prog != existing.progress)
                    or (self._sort_by == "speed" and prev_speed != existing.speed_bps)
                    or (self._sort_by == "size" and prev_size != existing.size_bytes)
                ):
                    sort_needs_refresh = True
            else:
                item = JobListItem(job, cat, idx)
                self._items_by_id[jid] = item
                self._store.append(item)
                list_mutated = True

        if list_mutated or filter_needs_refresh:
            self._custom_filter.changed(Gtk.FilterChange.DIFFERENT)
        if list_mutated or sort_needs_refresh:
            self._custom_sorter.changed(Gtk.SorterChange.DIFFERENT)

        target_id = self._pending_select_job_id or prev_selected_id
        if self._pending_select_job_id and self._pending_select_job_id in self._jobs_by_id:
            self._pending_select_job_id = ""

        if target_id:
            n_items = self._sort_model.get_n_items()
            for pos in range(n_items):
                it = self._sort_model.get_item(pos)
                if isinstance(it, JobListItem) and it.job_id == target_id:
                    if self._selection.get_selected() != pos:
                        self._selection.set_selected(pos)
                    break
        elif self._selection.get_selected() == Gtk.INVALID_LIST_POSITION and self._sort_model.get_n_items() > 0:
            self._selection.set_selected(0)

    def _update_header_and_sidebar_badges(self) -> None:
        stats = compute_dashboard_stats(self._raw_jobs, self._custom_categories)
        sc = stats["status_counts"]
        cc = stats["category_counts"]

        if self._online:
            self._status_pill.remove_css_class("header-status-offline")
            self._status_pill.add_css_class("header-status-online")
            active_dl = sc["downloading"]
            queued = sc["queued"]
            if active_dl > 0:
                self._status_pill.set_text(
                    f"● Connected  •  {active_dl} active  •  {stats['total_speed_text']}"
                )
            elif queued > 0:
                self._status_pill.set_text(f"● Connected  •  {queued} queued")
            else:
                self._status_pill.set_text(f"● Connected  •  {sc['all']} jobs")
        else:
            self._status_pill.remove_css_class("header-status-online")
            self._status_pill.add_css_class("header-status-offline")
            self._status_pill.set_text("○ Engine Offline")

        for key, _label, _icon in STATUS_FILTERS:
            row = self._sidebar_rows.get(("status", key))
            if row is not None:
                row.set_count(sc.get(key, 0))

        all_cat_row = self._sidebar_rows.get(("category", "all"))
        if all_cat_row is not None:
            all_cat_row.set_count(sc["all"])

        for cat in CATEGORY_ORDER:
            row = self._sidebar_rows.get(("category", cat))
            if row is not None:
                row.set_count(cc.get(cat, 0))

        self._pause_all_btn.set_sensitive(self._online and (sc["downloading"] + sc["queued"] > 0))
        self._resume_all_btn.set_sensitive(self._online and sc["paused"] > 0)

    def _sync_selection_and_empty_state(self) -> None:
        n_visible = self._sort_model.get_n_items()
        self._visible_count_label.set_text(f"{n_visible} shown")

        if n_visible > 0:
            self._list_stack.set_visible_child_name("list")
        else:
            if not self._online and len(self._raw_jobs) == 0:
                self._empty_list_page.set_title("Background Engine Offline")
                self._empty_list_page.set_description(
                    "Click 'Start Background Engine' above or run 'hydra-daemon' to view downloads."
                )
            elif len(self._raw_jobs) > 0:
                self._empty_list_page.set_title("No Matching Downloads")
                self._empty_list_page.set_description(
                    "Adjust your sidebar filter or search query to see other downloads."
                )
            else:
                self._empty_list_page.set_title("No Downloads Yet")
                self._empty_list_page.set_description(
                    "Downloads added via the Firefox extension, CLI, or TUI appear here automatically."
                )
            self._list_stack.set_visible_child_name("empty")

        selected = self._selection.get_selected_item()
        if isinstance(selected, JobListItem):
            self._details_view.update_job(
                selected.raw,
                self._speed_tracker.get_samples(selected.job_id),
                self._custom_categories,
            )
            self._update_action_controls(selected.raw)
        else:
            self._details_view.clear()
            self._update_action_controls(None)

    def _update_popouts(self) -> None:
        for job_id, win in list(self._popout_windows.items()):
            win.update_job(
                self._jobs_by_id.get(job_id),
                self._speed_tracker.get_samples(job_id),
            )

    def _on_close_request(self, *_args: Any) -> bool:
        self._sse_watcher.stop()
        if self._poll_timer_id:
            GLib.source_remove(self._poll_timer_id)
            self._poll_timer_id = 0
        for win in list(self._popout_windows.values()):
            win.close()
        return False


class HydraApplication(Adw.Application):
    """Libadwaita application wrapper managing CSS theme and window lifecycle."""

    def __init__(
        self,
        client: HydraGUIClient,
        anchor_path: Optional[Path] = None,
        initial_job_id: str = "",
        poll_interval_sec: float = 0.75,
        open_new_download: bool = False,
        initial_url: str = "",
    ) -> None:
        super().__init__(
            application_id="org.hydra.Downloader",
            flags=Gio.ApplicationFlags.NON_UNIQUE,
        )
        self.client = client
        self.anchor_path = anchor_path
        self.initial_job_id = initial_job_id
        self.poll_interval_sec = poll_interval_sec
        self.open_new_download = open_new_download
        self.initial_url = initial_url
        self._window: Optional[HydraWindow] = None
        self._css_loaded = False

    def do_activate(self) -> None:
        if not self._css_loaded:
            display = Gdk.Display.get_default()
            if display is not None:
                provider = Gtk.CssProvider()
                provider.load_from_string(HYDRA_CSS)
                Gtk.StyleContext.add_provider_for_display(
                    display,
                    provider,
                    Gtk.STYLE_PROVIDER_PRIORITY_APPLICATION,
                )
                _, icon_svg = resolve_icon_paths(self.anchor_path)
                if icon_svg:
                    theme = Gtk.IconTheme.get_for_display(display)
                    svg_path = Path(icon_svg)
                    if svg_path.parent.name == "apps" and svg_path.parent.parent.name == "scalable":
                        theme.add_search_path(str(svg_path.parent.parent.parent))
                    else:
                        theme.add_search_path(str(svg_path.parent))
                self._css_loaded = True

        if self._window is None:
            self._window = HydraWindow(
                self,
                client=self.client,
                anchor_path=self.anchor_path,
                initial_job_id=self.initial_job_id,
                poll_interval_sec=self.poll_interval_sec,
                open_new_download=self.open_new_download,
                initial_url=self.initial_url,
            )
        self._window.present()
