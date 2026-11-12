"""Libadwaita Preferences & Global Download Rules dialogs for Hydra Desktop GUI."""

from __future__ import annotations

import copy
import threading
from typing import Any, Callable, Dict, List, Optional

import gi  # type: ignore

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, GLib, Gtk  # type: ignore

from .client import HydraGUIClient, expand_path, open_path_in_desktop
from .dialogs import _open_folder_picker, _probe_disk_space_async
from .models import (
    DEFAULT_RULE_DEFINITIONS,
    DEFAULT_SETTINGS_CONFIG,
    category_icon_name,
    describe_schedule_status,
    format_speed,
    match_download_rule,
    normalize_extensions_input,
    validate_category_rule,
    validate_settings_payload,
)


def _merge_with_default_settings(settings: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    merged = copy.deepcopy(DEFAULT_SETTINGS_CONFIG)
    if not isinstance(settings, dict):
        return merged

    for key in (
        "max_concurrent_downloads",
        "default_threads",
        "max_threads",
        "default_save_path",
        "speed_limit_bytes",
        "disable_auto_categorize",
    ):
        if key in settings and settings[key] is not None:
            merged[key] = settings[key]

    sched = settings.get("schedule")
    if isinstance(sched, dict):
        merged["schedule"].update(sched)

    cats = settings.get("categories")
    if isinstance(cats, dict):
        merged["categories"] = copy.deepcopy(cats)

    return merged


class RuleEditorDialog(Adw.Window):
    """Modal editor for adding or editing a global file-type download rule."""

    def __init__(
        self,
        parent: Gtk.Window,
        client: HydraGUIClient,
        *,
        existing_name: Optional[str] = None,
        existing_rule: Optional[Dict[str, Any]] = None,
        taken_names: Optional[set[str]] = None,
        default_save_path: str = "~/Downloads",
        max_threads_cap: int = 32,
        on_save: Callable[[Optional[str], str, Dict[str, Any]], None],
    ) -> None:
        super().__init__(transient_for=parent, modal=True)
        self.client = client
        self._original_name = existing_name
        self._taken_names = {n for n in (taken_names or set()) if n != existing_name}
        self._on_save = on_save

        is_edit = existing_name is not None
        rule = existing_rule if isinstance(existing_rule, dict) else {}

        self.set_title("Edit Download Rule" if is_edit else "Add Download Rule")
        self.set_default_size(520, 560)
        self.set_resizable(True)

        toolbar = Adw.ToolbarView()
        header = Adw.HeaderBar()
        header.set_show_end_title_buttons(True)
        toolbar.add_top_bar(header)

        cancel_btn = Gtk.Button(label="Cancel")
        cancel_btn.connect("clicked", lambda *_: self.close())
        header.pack_start(cancel_btn)

        save_btn = Gtk.Button(label="Apply Rule" if is_edit else "Add Rule")
        save_btn.add_css_class("suggested-action")
        save_btn.connect("clicked", self._on_submit)
        header.pack_end(save_btn)

        scroll = Gtk.ScrolledWindow()
        scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        scroll.set_vexpand(True)

        content = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
        content.set_margin_top(14)
        content.set_margin_bottom(18)
        content.set_margin_start(16)
        content.set_margin_end(16)
        scroll.set_child(content)
        toolbar.set_content(scroll)
        self.set_content(toolbar)

        self._err_label = Gtk.Label(xalign=0.0, wrap=True)
        self._err_label.add_css_class("error-banner-card")
        self._err_label.set_visible(False)
        content.append(self._err_label)

        # Basic routing fields
        main_group = Adw.PreferencesGroup(
            title="Rule Routing",
            description="Route matching file extensions automatically into the target folder.",
        )

        self._name_row = Adw.EntryRow()
        self._name_row.set_title("Category / Rule Name")
        self._name_row.set_text(existing_name or "")
        main_group.add(self._name_row)

        self._exts_row = Adw.EntryRow()
        self._exts_row.set_title("File Extensions (comma-separated, e.g. iso, tar.gz, xz)")
        exts = rule.get("extensions") or []
        self._exts_row.set_text(", ".join(str(e) for e in exts))
        main_group.add(self._exts_row)

        self._path_row = Adw.EntryRow()
        self._path_row.set_title("Destination Folder")
        self._path_row.set_text(str(rule.get("path") or default_save_path or "~/Downloads"))
        browse_btn = Gtk.Button.new_from_icon_name("folder-open-symbolic")
        browse_btn.add_css_class("flat")
        browse_btn.set_valign(Gtk.Align.CENTER)
        browse_btn.set_tooltip_text("Browse destination folder...")
        browse_btn.connect("clicked", self._on_browse_folder)
        self._path_row.add_suffix(browse_btn)
        self._path_row.connect("changed", lambda *_: self._refresh_space())
        main_group.add(self._path_row)

        self._space_row = Adw.ActionRow()
        self._space_row.set_use_markup(False)
        self._space_row.set_title("Destination Free Space")
        self._space_row.set_subtitle("Checking...")
        main_group.add(self._space_row)

        content.append(main_group)

        # Priority & optional transfer overrides
        adv_group = Adw.PreferencesGroup(
            title="Priority and Transfer Overrides",
            description="Higher priority wins when extensions overlap. Leave threads or speed at 0 to inherit global settings.",
        )

        self._enabled_row = Adw.SwitchRow()
        self._enabled_row.set_title("Rule Active")
        self._enabled_row.set_subtitle("Inactive rules remain saved but do not intercept routing.")
        self._enabled_row.set_active(not bool(rule.get("disabled", False)))
        adv_group.add(self._enabled_row)

        self._priority_row = Adw.SpinRow.new_with_range(-100, 100, 1)
        self._priority_row.set_title("Rule Priority")
        self._priority_row.set_subtitle("Higher values match before lower-priority rules (default: 0).")
        self._priority_row.set_value(float(int(rule.get("priority") or 0)))
        adv_group.add(self._priority_row)

        self._threads_row = Adw.SpinRow.new_with_range(0, max(1, int(max_threads_cap)), 1)
        self._threads_row.set_title("Default Connections Override")
        self._threads_row.set_subtitle("0 = inherit global Default Connections.")
        self._threads_row.set_value(float(int(rule.get("default_threads") or 0)))
        adv_group.add(self._threads_row)

        self._speed_row = Adw.SpinRow.new_with_range(0, 1048576, 128)
        self._speed_row.set_title("Speed Limit Override (KB/s)")
        self._speed_row.set_subtitle("0 = inherit global Speed Limit.")
        speed_kbps = int(int(rule.get("speed_limit_bytes") or 0) / 1024)
        self._speed_row.set_value(float(speed_kbps))
        adv_group.add(self._speed_row)

        content.append(adv_group)
        self._refresh_space()

    def _on_browse_folder(self, *_args: Any) -> None:
        _open_folder_picker(self, self._path_row.get_text(), lambda p: self._path_row.set_text(p))

    def _refresh_space(self) -> None:
        folder = expand_path(self._path_row.get_text().strip())
        if not folder:
            self._space_row.set_subtitle("Enter a folder path")
            return
        _probe_disk_space_async(self.client, folder, self._space_row.set_subtitle)

    def _on_submit(self, *_args: Any) -> None:
        name = self._name_row.get_text().strip()
        if name in self._taken_names:
            self._err_label.set_text(f"A rule named '{name}' already exists.")
            self._err_label.set_visible(True)
            return

        rule_obj: Dict[str, Any] = {
            "extensions": normalize_extensions_input(self._exts_row.get_text()),
            "path": self._path_row.get_text().strip(),
            "disabled": not self._enabled_row.get_active(),
            "priority": int(self._priority_row.get_value()),
            "default_threads": int(self._threads_row.get_value()),
            "speed_limit_bytes": int(self._speed_row.get_value()) * 1024,
        }

        if err := validate_category_rule(name, rule_obj):
            self._err_label.set_text(err)
            self._err_label.set_visible(True)
            return

        if not rule_obj["extensions"]:
            self._err_label.set_text("Specify at least one file extension (e.g. zip, iso).")
            self._err_label.set_visible(True)
            return

        self._err_label.set_visible(False)
        self._on_save(self._original_name, name, rule_obj)
        self.close()


class SettingsDialog(Adw.Window):
    """Complete settings window for folders, limits, connections, scheduling, and rules."""

    def __init__(
        self,
        parent: Gtk.Window,
        client: HydraGUIClient,
        settings: Optional[Dict[str, Any]],
        on_saved: Callable[[Dict[str, Any]], None],
        initial_tab: str = "folders",
    ) -> None:
        super().__init__(transient_for=parent, modal=True)
        self.client = client
        self.on_saved = on_saved
        self._draft = _merge_with_default_settings(settings)
        self._submitting = False
        self._syncing_form = False
        self._rule_rows: List[Gtk.Widget] = []

        self.set_title("Hydra Settings")
        self.set_default_size(760, 640)
        self.set_size_request(620, 480)
        self.set_resizable(True)

        self._toast_overlay = Adw.ToastOverlay()
        toolbar_view = Adw.ToolbarView()
        self._toast_overlay.set_child(toolbar_view)
        self.set_content(self._toast_overlay)

        # HeaderBar with ViewSwitcher + Save / Cancel
        header = Adw.HeaderBar()
        header.set_show_end_title_buttons(True)
        toolbar_view.add_top_bar(header)

        cancel_btn = Gtk.Button(label="Cancel")
        cancel_btn.connect("clicked", lambda *_: self.close())
        header.pack_start(cancel_btn)

        self._stack = Adw.ViewStack()
        switcher = Adw.ViewSwitcher()
        switcher.set_stack(self._stack)
        switcher.set_policy(Adw.ViewSwitcherPolicy.WIDE)
        header.set_title_widget(switcher)

        self._save_btn = Gtk.Button(label="Save Settings")
        self._save_btn.add_css_class("suggested-action")
        self._save_btn.connect("clicked", self._on_save_clicked)
        header.pack_end(self._save_btn)

        # Outer container holding optional error bar + ViewStack
        outer_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=0)
        toolbar_view.set_content(outer_box)

        self._error_banner = Gtk.Label(xalign=0.0, wrap=True)
        self._error_banner.add_css_class("error-banner-card")
        self._error_banner.set_margin_top(10)
        self._error_banner.set_margin_start(14)
        self._error_banner.set_margin_end(14)
        self._error_banner.set_visible(False)
        outer_box.append(self._error_banner)

        self._stack.set_vexpand(True)
        outer_box.append(self._stack)

        # Build the 4 pages covering all 5 roadmap settings topics
        self._build_folders_page()
        self._build_limits_connections_page()
        self._build_scheduling_page()
        self._build_rules_page()

        self._populate_from_draft()
        if initial_tab in {"folders", "limits", "scheduling", "rules"}:
            self._stack.set_visible_child_name(initial_tab)

        self._refresh_from_daemon_async()

    def _build_folders_page(self) -> None:
        page = Adw.PreferencesPage()

        dir_group = Adw.PreferencesGroup(
            title="Default Download Folder",
            description="Fallback destination used when no file-type rule matches or automatic sorting is turned off.",
        )

        self._save_path_row = Adw.EntryRow()
        self._save_path_row.set_title("Default Save Folder")
        browse_btn = Gtk.Button(label="Browse...")
        browse_btn.set_valign(Gtk.Align.CENTER)
        browse_btn.connect("clicked", self._on_browse_default_folder)
        open_btn = Gtk.Button.new_from_icon_name("folder-open-symbolic")
        open_btn.add_css_class("flat")
        open_btn.set_valign(Gtk.Align.CENTER)
        open_btn.set_tooltip_text("Open folder in file manager")
        open_btn.connect("clicked", lambda *_: open_path_in_desktop(self._save_path_row.get_text()))
        self._save_path_row.add_suffix(browse_btn)
        self._save_path_row.add_suffix(open_btn)
        self._save_path_row.connect("changed", lambda *_: self._refresh_default_space())
        dir_group.add(self._save_path_row)

        self._default_space_row = Adw.ActionRow()
        self._default_space_row.set_use_markup(False)
        self._default_space_row.set_title("Available Disk Space")
        self._default_space_row.set_subtitle("Checking...")
        dir_group.add(self._default_space_row)
        page.add(dir_group)

        behavior_group = Adw.PreferencesGroup(
            title="Global Routing and Destination Safety",
        )

        self._auto_cat_row = Adw.SwitchRow()
        self._auto_cat_row.set_use_markup(False)
        self._auto_cat_row.set_title("Automatically Route Downloads by File Type")
        self._auto_cat_row.set_subtitle(
            "Sort archives, video, music, packages, and documents into category folders defined under Download Rules."
        )
        self._auto_cat_row.connect("notify::active", self._on_auto_categorize_toggled)
        behavior_group.add(self._auto_cat_row)

        conflict_row = Adw.ActionRow()
        conflict_row.set_use_markup(False)
        conflict_row.set_title("Filename Conflict and Path Safety")
        conflict_row.set_subtitle(
            "Existing filenames append (1), (2), ... atomically without overwriting files or following symbolic links."
        )
        conflict_row.add_prefix(Gtk.Image.new_from_icon_name("security-high-symbolic"))
        behavior_group.add(conflict_row)

        page.add(behavior_group)
        stack_page = self._stack.add_titled(page, "folders", "Folders")
        stack_page.set_icon_name("folder-open-symbolic")

    def _build_limits_connections_page(self) -> None:
        page = Adw.PreferencesPage()

        limits_group = Adw.PreferencesGroup(
            title="Queue and Speed Limits",
            description="Changes apply immediately to queued transfers and active downloads without a per-job cap.",
        )

        self._max_conc_row = Adw.SpinRow.new_with_range(1, 64, 1)
        self._max_conc_row.set_use_markup(False)
        self._max_conc_row.set_title("Max Concurrent Downloads")
        self._max_conc_row.set_subtitle("Number of downloads actively transferring at the same time.")
        limits_group.add(self._max_conc_row)

        self._speed_kbps_row = Adw.SpinRow.new_with_range(0, 1048576, 128)
        self._speed_kbps_row.set_use_markup(False)
        self._speed_kbps_row.set_title("Global Speed Limit (KB/s)")
        self._speed_kbps_row.set_subtitle("Unlimited")
        self._speed_kbps_row.connect("notify::value", self._on_speed_spin_changed)
        limits_group.add(self._speed_kbps_row)

        preset_row = Adw.ActionRow()
        preset_row.set_use_markup(False)
        preset_row.set_title("Quick Speed Presets")
        preset_row.set_subtitle("Apply common bandwidth ceilings with one click.")
        preset_box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=6)
        preset_box.set_valign(Gtk.Align.CENTER)
        for label, kbps in (("Unlimited", 0), ("512 KB/s", 512), ("2 MB/s", 2048), ("10 MB/s", 10240)):
            btn = Gtk.Button(label=label)
            btn.add_css_class("flat")
            btn.connect("clicked", lambda _b, val=kbps: self._speed_kbps_row.set_value(float(val)))
            preset_box.append(btn)
        preset_row.add_suffix(preset_box)
        limits_group.add(preset_row)
        page.add(limits_group)

        conn_group = Adw.PreferencesGroup(
            title="Parallel HTTP Connections",
            description="Controls byte-range splitting when the remote server supports HTTP 206 Partial Content.",
        )

        self._def_threads_row = Adw.SpinRow.new_with_range(1, 64, 1)
        self._def_threads_row.set_use_markup(False)
        self._def_threads_row.set_title("Default Connections per Download")
        self._def_threads_row.set_subtitle("Parallel chunk workers used by default when not overridden by a rule.")
        self._def_threads_row.connect("notify::value", self._on_default_threads_changed)
        conn_group.add(self._def_threads_row)

        self._max_threads_row = Adw.SpinRow.new_with_range(1, 64, 1)
        self._max_threads_row.set_use_markup(False)
        self._max_threads_row.set_title("Maximum Connections Cap")
        self._max_threads_row.set_subtitle("Upper ceiling enforced across default settings and category rules.")
        self._max_threads_row.connect("notify::value", self._on_max_threads_changed)
        conn_group.add(self._max_threads_row)

        adaptive_row = Adw.ActionRow()
        adaptive_row.set_use_markup(False)
        adaptive_row.set_title("Adaptive Host and Range Safety")
        adaptive_row.set_subtitle(
            "Single-stream fallback applies automatically if range requests are unsupported; strict rate-limited workers hosts cap safely."
        )
        adaptive_row.add_prefix(Gtk.Image.new_from_icon_name("network-transmit-receive-symbolic"))
        conn_group.add(adaptive_row)

        page.add(conn_group)
        stack_page = self._stack.add_titled(page, "limits", "Limits & Connections")
        stack_page.set_icon_name("network-transmit-receive-symbolic")

    def _build_scheduling_page(self) -> None:
        page = Adw.PreferencesPage()

        status_group = Adw.PreferencesGroup(title="Scheduler Status")
        self._sched_status_row = Adw.ActionRow()
        self._sched_status_row.set_use_markup(False)
        self._sched_status_row.set_title("Daily download window disabled (24/7)")
        self._sched_status_row.set_subtitle("Queued downloads start immediately.")
        self._sched_status_icon = Gtk.Image.new_from_icon_name("alarm-symbolic")
        self._sched_status_row.add_prefix(self._sched_status_icon)
        status_group.add(self._sched_status_row)
        page.add(status_group)

        win_group = Adw.PreferencesGroup(
            title="Daily Download Window (Off-Peak Queue)",
            description="Limit automatic queue promotion to a daily 24-hour local time window (supports overnight spans such as 23:00 → 07:00).",
        )

        self._sched_enabled_row = Adw.SwitchRow()
        self._sched_enabled_row.set_title("Enable Daily Download Window")
        self._sched_enabled_row.set_subtitle("Hold new queued downloads until the scheduled window opens.")
        self._sched_enabled_row.connect("notify::active", lambda *_: self._refresh_schedule_status_banner())
        win_group.add(self._sched_enabled_row)

        self._sched_start_row = Adw.EntryRow()
        self._sched_start_row.set_title("Window Start Time (24h HH:MM)")
        self._sched_start_row.connect("changed", lambda *_: self._refresh_schedule_status_banner())
        win_group.add(self._sched_start_row)

        self._sched_end_row = Adw.EntryRow()
        self._sched_end_row.set_title("Window End Time (24h HH:MM)")
        self._sched_end_row.connect("changed", lambda *_: self._refresh_schedule_status_banner())
        win_group.add(self._sched_end_row)

        preset_row = Adw.ActionRow()
        preset_row.set_title("Quick Window Presets")
        preset_box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=6)
        preset_box.set_valign(Gtk.Align.CENTER)
        for label, s_t, e_t in (
            ("Off-Peak (01:00–07:00)", "01:00", "07:00"),
            ("Overnight (23:00–06:00)", "23:00", "06:00"),
            ("Workday (09:00–17:00)", "09:00", "17:00"),
        ):
            btn = Gtk.Button(label=label)
            btn.add_css_class("flat")
            btn.connect("clicked", lambda _b, st=s_t, en=e_t: self._apply_schedule_preset(st, en))
            preset_box.append(btn)
        preset_row.add_suffix(preset_box)
        win_group.add(preset_row)

        self._sched_pause_outside_row = Adw.SwitchRow()
        self._sched_pause_outside_row.set_title("Pause Active Downloads When Window Closes")
        self._sched_pause_outside_row.set_subtitle(
            "Automatically pause running jobs at End Time and requeue them when Start Time arrives again."
        )
        self._sched_pause_outside_row.connect("notify::active", lambda *_: self._refresh_schedule_status_banner())
        win_group.add(self._sched_pause_outside_row)
        page.add(win_group)

        preset_group = Adw.PreferencesGroup(
            title="Per-Download Schedule Default",
            description="Suggested default time offered when scheduling an individual download.",
        )
        self._sched_default_time_row = Adw.EntryRow()
        self._sched_default_time_row.set_title("Default Scheduled Start Time (24h HH:MM)")
        preset_group.add(self._sched_default_time_row)
        page.add(preset_group)

        stack_page = self._stack.add_titled(page, "scheduling", "Scheduling")
        stack_page.set_icon_name("alarm-symbolic")

    def _build_rules_page(self) -> None:
        page = Adw.PreferencesPage()

        header_actions = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        reset_btn = Gtk.Button(label="Reset Defaults")
        reset_btn.set_tooltip_text("Restore built-in Video, Music, Compressed, Programs, and Documents rules")
        reset_btn.connect("clicked", self._on_reset_rules_clicked)

        add_btn = Gtk.Button()
        add_btn_box = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=6)
        add_btn_box.append(Gtk.Image.new_from_icon_name("list-add-symbolic"))
        add_btn_box.append(Gtk.Label(label="Add Rule"))
        add_btn.set_child(add_btn_box)
        add_btn.add_css_class("suggested-action")
        add_btn.connect("clicked", self._on_add_rule_clicked)

        header_actions.append(reset_btn)
        header_actions.append(add_btn)

        self._rules_group = Adw.PreferencesGroup(
            title="Global and Default Rules by File Type and Destination",
            description="Matched in priority order (then longest compound extension). Active rules determine automatic save folders and optional transfer overrides.",
        )
        self._rules_group.set_header_suffix(header_actions)
        page.add(self._rules_group)

        tester_group = Adw.PreferencesGroup(
            title="Test File Type Routing",
            description="Enter any filename below to preview which rule, folder, threads, and speed cap will apply.",
        )
        self._tester_entry = Adw.EntryRow()
        self._tester_entry.set_use_markup(False)
        self._tester_entry.set_title("Sample Filename (e.g. ubuntu-24.04.iso or archive.tar.gz)")
        self._tester_entry.set_text("ubuntu-24.04.iso")
        self._tester_entry.connect("changed", lambda *_: self._refresh_rule_tester())
        tester_group.add(self._tester_entry)

        self._tester_result_row = Adw.ActionRow()
        self._tester_result_row.set_use_markup(False)
        self._tester_result_row.set_title("Matched Route")
        self._tester_result_row.set_subtitle("--")
        self._tester_icon = Gtk.Image.new_from_icon_name("folder-symbolic")
        self._tester_result_row.add_prefix(self._tester_icon)
        tester_group.add(self._tester_result_row)
        page.add(tester_group)

        stack_page = self._stack.add_titled(page, "rules", "Download Rules")
        stack_page.set_icon_name("view-list-symbolic")

    def _populate_from_draft(self) -> None:
        self._syncing_form = True
        try:
            self._save_path_row.set_text(str(self._draft.get("default_save_path") or "~/Downloads"))
            self._auto_cat_row.set_active(not bool(self._draft.get("disable_auto_categorize", False)))

            self._max_conc_row.set_value(float(max(1, int(self._draft.get("max_concurrent_downloads") or 2))))
            speed_kbps = int(int(self._draft.get("speed_limit_bytes") or 0) / 1024)
            self._speed_kbps_row.set_value(float(speed_kbps))

            def_t = max(1, int(self._draft.get("default_threads") or 8))
            max_t = max(def_t, int(self._draft.get("max_threads") or 32))
            self._def_threads_row.set_value(float(def_t))
            self._max_threads_row.set_value(float(max_t))

            sched = self._draft.get("schedule") or {}
            self._sched_enabled_row.set_active(bool(sched.get("enabled", False)))
            self._sched_start_row.set_text(str(sched.get("start_time") or "01:00"))
            self._sched_end_row.set_text(str(sched.get("end_time") or "07:00"))
            self._sched_pause_outside_row.set_active(bool(sched.get("pause_outside_window", False)))
            self._sched_default_time_row.set_text(str(sched.get("default_time") or "23:00"))
        finally:
            self._syncing_form = False

        self._on_speed_spin_changed()
        self._refresh_default_space()
        self._refresh_schedule_status_banner()
        self._rebuild_rules_list()
        self._refresh_rule_tester()

    def _collect_draft_from_form(self) -> Dict[str, Any]:
        payload = copy.deepcopy(self._draft)
        payload["default_save_path"] = self._save_path_row.get_text().strip()
        payload["disable_auto_categorize"] = not self._auto_cat_row.get_active()
        payload["max_concurrent_downloads"] = int(self._max_conc_row.get_value())
        payload["speed_limit_bytes"] = int(self._speed_kbps_row.get_value()) * 1024
        payload["default_threads"] = int(self._def_threads_row.get_value())
        payload["max_threads"] = int(self._max_threads_row.get_value())
        payload["schedule"] = {
            "enabled": self._sched_enabled_row.get_active(),
            "start_time": self._sched_start_row.get_text().strip(),
            "end_time": self._sched_end_row.get_text().strip(),
            "default_time": self._sched_default_time_row.get_text().strip(),
            "pause_outside_window": self._sched_pause_outside_row.get_active(),
        }
        return payload

    def _on_auto_categorize_toggled(self, *_args: Any) -> None:
        if not self._syncing_form:
            self._refresh_rule_tester()

    def _on_speed_spin_changed(self, *_args: Any) -> None:
        kbps = int(self._speed_kbps_row.get_value())
        if kbps <= 0:
            self._speed_kbps_row.set_subtitle("0 KB/s (Unlimited)")
        else:
            self._speed_kbps_row.set_subtitle(f"Cap: {format_speed(kbps * 1024)} (0 = unlimited)")
        if not self._syncing_form:
            self._refresh_rule_tester()

    def _on_default_threads_changed(self, *_args: Any) -> None:
        if self._syncing_form:
            return
        def_val = int(self._def_threads_row.get_value())
        if int(self._max_threads_row.get_value()) < def_val:
            self._max_threads_row.set_value(float(def_val))
        self._refresh_rule_tester()

    def _on_max_threads_changed(self, *_args: Any) -> None:
        if self._syncing_form:
            return
        max_val = int(self._max_threads_row.get_value())
        if int(self._def_threads_row.get_value()) > max_val:
            self._def_threads_row.set_value(float(max_val))
        self._refresh_rule_tester()

    def _on_browse_default_folder(self, *_args: Any) -> None:
        _open_folder_picker(self, self._save_path_row.get_text(), lambda p: self._save_path_row.set_text(p))

    def _refresh_default_space(self) -> None:
        folder = expand_path(self._save_path_row.get_text().strip())
        if not folder:
            self._default_space_row.set_subtitle("Enter a default folder path")
            return
        _probe_disk_space_async(self.client, folder, self._default_space_row.set_subtitle)

    def _apply_schedule_preset(self, start_time: str, end_time: str) -> None:
        self._sched_enabled_row.set_active(True)
        self._sched_start_row.set_text(start_time)
        self._sched_end_row.set_text(end_time)
        self._refresh_schedule_status_banner()

    def _refresh_schedule_status_banner(self) -> None:
        if self._syncing_form:
            return
        sched = {
            "enabled": self._sched_enabled_row.get_active(),
            "start_time": self._sched_start_row.get_text().strip(),
            "end_time": self._sched_end_row.get_text().strip(),
            "pause_outside_window": self._sched_pause_outside_row.get_active(),
        }
        status = describe_schedule_status(sched)
        self._sched_status_row.set_title(status["title"])
        self._sched_status_row.set_subtitle(status["detail"])
        state = status["state"]
        icon_name = {
            "open": "emblem-ok-symbolic",
            "closed": "media-playback-pause-symbolic",
            "invalid": "dialog-warning-symbolic",
        }.get(state, "alarm-symbolic")
        self._sched_status_icon.set_from_icon_name(icon_name)

    def _rebuild_rules_list(self) -> None:
        for row in self._rule_rows:
            self._rules_group.remove(row)
        self._rule_rows.clear()

        categories = self._draft.get("categories")
        if not isinstance(categories, dict) or not categories:
            empty_row = Adw.ActionRow()
            empty_row.set_title("No File-Type Rules Configured")
            empty_row.set_subtitle("All downloads use Default Save Folder, or click Add Rule / Reset Defaults.")
            self._rules_group.add(empty_row)
            self._rule_rows.append(empty_row)
            return

        sorted_items = sorted(
            categories.items(),
            key=lambda kv: (
                -int((kv[1] or {}).get("priority") or 0) if isinstance(kv[1], dict) else 0,
                str(kv[0]).lower(),
            ),
        )

        for cat_name, rule in sorted_items:
            if not isinstance(rule, dict):
                continue
            disabled = bool(rule.get("disabled", False))
            priority = int(rule.get("priority") or 0)
            exts = [str(e) for e in (rule.get("extensions") or [])]
            ext_preview = ", ".join(exts[:10])
            if len(exts) > 10:
                ext_preview += f" (+{len(exts) - 10} more)"
            if not ext_preview:
                ext_preview = "no extensions"

            title_parts = [cat_name]
            if disabled:
                title_parts.append("(Disabled)")
            elif priority != 0:
                title_parts.append(f"(Priority {priority})")

            sub_parts = [str(rule.get("path") or "~/Downloads"), ext_preview]
            rule_threads = int(rule.get("default_threads") or 0)
            rule_speed = int(rule.get("speed_limit_bytes") or 0)
            if rule_threads > 0:
                sub_parts.append(f"{rule_threads} conn")
            if rule_speed > 0:
                sub_parts.append(format_speed(rule_speed))

            row = Adw.ActionRow()
            row.set_use_markup(False)
            row.set_title("  ".join(title_parts))
            row.set_subtitle("  •  ".join(sub_parts))
            row.add_prefix(Gtk.Image.new_from_icon_name(category_icon_name(cat_name)))

            toggle = Gtk.Switch()
            toggle.set_active(not disabled)
            toggle.set_valign(Gtk.Align.CENTER)
            toggle.set_tooltip_text("Enable or disable this routing rule")
            toggle.connect("state-set", self._on_rule_toggle, cat_name)

            edit_btn = Gtk.Button.new_from_icon_name("document-edit-symbolic")
            edit_btn.add_css_class("flat")
            edit_btn.set_valign(Gtk.Align.CENTER)
            edit_btn.set_tooltip_text(f"Edit '{cat_name}' rule")
            edit_btn.connect("clicked", lambda _b, n=cat_name: self._open_rule_editor(n))

            del_btn = Gtk.Button.new_from_icon_name("user-trash-symbolic")
            del_btn.add_css_class("flat")
            del_btn.set_valign(Gtk.Align.CENTER)
            del_btn.set_tooltip_text(f"Delete '{cat_name}' rule")
            del_btn.connect("clicked", lambda _b, n=cat_name: self._confirm_delete_rule(n))

            row.add_suffix(toggle)
            row.add_suffix(edit_btn)
            row.add_suffix(del_btn)
            self._rules_group.add(row)
            self._rule_rows.append(row)

    def _on_rule_toggle(self, _switch: Gtk.Switch, active: bool, cat_name: str) -> bool:
        cats = self._draft.get("categories")
        if isinstance(cats, dict) and isinstance(cats.get(cat_name), dict):
            cats[cat_name]["disabled"] = not active
            GLib.idle_add(lambda: self._rebuild_rules_list() and False)
            GLib.idle_add(lambda: self._refresh_rule_tester() and False)
        return False

    def _open_rule_editor(self, cat_name: Optional[str] = None) -> None:
        cats = self._draft.setdefault("categories", {})
        existing = copy.deepcopy(cats.get(cat_name)) if cat_name and isinstance(cats.get(cat_name), dict) else None
        max_cap = max(1, int(self._max_threads_row.get_value()))
        def_save = self._save_path_row.get_text().strip() or "~/Downloads"

        def _applied(orig_name: Optional[str], new_name: str, rule_obj: Dict[str, Any]) -> None:
            if orig_name and orig_name != new_name and orig_name in cats:
                del cats[orig_name]
            cats[new_name] = rule_obj
            self._rebuild_rules_list()
            self._refresh_rule_tester()
            self._show_toast(f"Rule '{new_name}' staged — click Save Settings to apply")

        dlg = RuleEditorDialog(
            self,
            self.client,
            existing_name=cat_name,
            existing_rule=existing,
            taken_names=set(cats.keys()),
            default_save_path=def_save,
            max_threads_cap=max_cap,
            on_save=_applied,
        )
        dlg.present()

    def _on_add_rule_clicked(self, *_args: Any) -> None:
        self._open_rule_editor(None)

    def _confirm_delete_rule(self, cat_name: str) -> None:
        dlg = Adw.AlertDialog()
        dlg.set_heading("Delete Download Rule?")
        dlg.set_body(
            f"Remove the “{cat_name}” routing rule? Files with its extensions will fall back to other matching rules or your Default Save Folder."
        )
        dlg.add_response("keep", "Cancel")
        dlg.add_response("delete", "Delete Rule")
        dlg.set_response_appearance("delete", Adw.ResponseAppearance.DESTRUCTIVE)
        dlg.set_default_response("keep")
        dlg.set_close_response("keep")

        def _on_resp(_d: Adw.AlertDialog, response: str) -> None:
            if response == "delete":
                cats = self._draft.get("categories")
                if isinstance(cats, dict) and cat_name in cats:
                    del cats[cat_name]
                    self._rebuild_rules_list()
                    self._refresh_rule_tester()
                    self._show_toast(f"Removed rule '{cat_name}' — click Save Settings to apply")

        dlg.connect("response", _on_resp)
        dlg.present(self)

    def _on_reset_rules_clicked(self, *_args: Any) -> None:
        dlg = Adw.AlertDialog()
        dlg.set_heading("Restore Default Rules?")
        dlg.set_body(
            "Reset download routing rules back to Hydra defaults (Video, Music, Compressed, Programs, and Documents)?"
        )
        dlg.add_response("cancel", "Cancel")
        dlg.add_response("reset", "Restore Defaults")
        dlg.set_response_appearance("reset", Adw.ResponseAppearance.SUGGESTED)
        dlg.set_default_response("cancel")
        dlg.set_close_response("cancel")

        def _on_resp(_d: Adw.AlertDialog, response: str) -> None:
            if response == "reset":
                self._draft["categories"] = copy.deepcopy(DEFAULT_RULE_DEFINITIONS)
                self._rebuild_rules_list()
                self._refresh_rule_tester()
                self._show_toast("Restored default rules — click Save Settings to apply")

        dlg.connect("response", _on_resp)
        dlg.present(self)

    def _refresh_rule_tester(self) -> None:
        sample = self._tester_entry.get_text().strip()
        if not sample:
            self._tester_result_row.set_title("Enter a sample filename above")
            self._tester_result_row.set_subtitle("--")
            return

        draft_cfg = self._collect_draft_from_form()
        fallback = expand_path(str(draft_cfg.get("default_save_path") or "~/Downloads")) or "~/Downloads"
        res = match_download_rule(sample, draft_cfg, fallback)
        cat = str(res["category"])
        dest = str(res["destination_dir"])
        ext = str(res.get("matched_extension") or "")
        threads = int(res["effective_threads"])
        speed_bps = int(res["effective_speed_bytes"])
        speed_txt = format_speed(speed_bps) if speed_bps > 0 else "Unlimited"

        if not res["auto_categorize_used"]:
            match_label = f"Default Folder (auto-routing disabled)"
        elif cat == "Default":
            match_label = "Default Fallback (no file-type rule matched)"
        else:
            match_label = f"Rule: {cat} (.{ext})"

        self._tester_icon.set_from_icon_name(
            "folder-symbolic" if cat == "Default" else category_icon_name(cat)
        )
        self._tester_result_row.set_title(match_label)
        self._tester_result_row.set_subtitle(
            f"Destination: {dest}  •  Connections: {threads}  •  Speed Cap: {speed_txt}"
        )

    def _refresh_from_daemon_async(self) -> None:
        def worker() -> None:
            ok, remote, _err = self.client.get_settings_with_status()
            if not ok or not remote:
                return

            def _apply() -> bool:
                self._draft = _merge_with_default_settings(remote)
                self._populate_from_draft()
                return False

            GLib.idle_add(_apply)

        threading.Thread(target=worker, daemon=True).start()

    def _show_error(self, msg: str) -> None:
        if msg:
            self._error_banner.set_text(msg)
            self._error_banner.set_visible(True)
        else:
            self._error_banner.set_visible(False)

    def _show_toast(self, msg: str) -> None:
        toast = Adw.Toast.new(msg)
        toast.set_timeout(2)
        self._toast_overlay.add_toast(toast)

    def _on_save_clicked(self, *_args: Any) -> None:
        if self._submitting:
            return
        payload = self._collect_draft_from_form()
        if err := validate_settings_payload(payload):
            self._show_error(err)
            return

        self._show_error("")
        self._submitting = True
        self._save_btn.set_sensitive(False)
        self._save_btn.set_label("Saving...")

        def worker() -> None:
            ok, saved_cfg, err_msg = self.client.save_settings(payload)

            def _done() -> bool:
                self._submitting = False
                self._save_btn.set_sensitive(True)
                self._save_btn.set_label("Save Settings")
                if ok:
                    self.on_saved(saved_cfg or payload)
                    self.close()
                else:
                    self._show_error(err_msg or "Failed to save settings to Hydra daemon.")
                return False

            GLib.idle_add(_done)

        threading.Thread(target=worker, daemon=True).start()
