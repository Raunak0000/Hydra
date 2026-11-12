"""Interactive GTK 4 + Libadwaita dialogs for creating and controlling Hydra downloads."""

from __future__ import annotations

from datetime import datetime, timedelta
import os
import threading
from pathlib import Path
from typing import Any, Callable, Dict, List, Optional

import gi  # type: ignore

gi.require_version("Gtk", "4.0")
gi.require_version("Adw", "1")
from gi.repository import Adw, Gdk, Gio, GLib, Gtk  # type: ignore

from .client import HydraGUIClient, expand_path, resolve_downloads_dir
from .models import (
    display_name_for_job,
    format_bytes,
    parse_batch_urls_text,
    parse_clock_minutes,
    parse_headers_text,
    resolve_category_destination,
    suggest_filename_from_url,
    validate_download_url,
    validate_filename,
)


def _open_folder_picker(
    parent: Gtk.Window,
    initial_dir: str,
    on_selected: Callable[[str], None],
) -> None:
    dialog = Gtk.FileDialog()
    dialog.set_title("Choose Destination Folder")

    cleaned = expand_path((initial_dir or "").strip())
    if cleaned and os.path.isdir(cleaned):
        dialog.set_initial_folder(Gio.File.new_for_path(cleaned))

    def _done(_dlg: Gtk.FileDialog, res: Gio.AsyncResult) -> None:
        try:
            folder = dialog.select_folder_finish(res)
            if folder is not None:
                path = folder.get_path()
                if path:
                    on_selected(path)
        except GLib.Error:
            # User cancelled dialog cleanly
            pass

    dialog.select_folder(parent, None, _done)


def _probe_disk_space_async(
    client: HydraGUIClient,
    folder_path: str,
    callback: Callable[[str], None],
) -> None:
    expanded = expand_path((folder_path or "").strip())
    if not expanded:
        return

    def worker() -> None:
        info = client.check_disk_space(expanded, 0)
        if info and "available_bytes" in info:
            avail = int(info.get("available_bytes") or 0)
            msg = f"Free disk space: {format_bytes(avail)}"
        else:
            msg = "Free disk space: check when daemon is connected"

        def _apply() -> bool:
            callback(msg)
            return False

        GLib.idle_add(_apply)

    threading.Thread(target=worker, daemon=True).start()


class NewDownloadDialog(Adw.Window):
    """Dialog for adding a new download with destination & filename controls."""

    def __init__(
        self,
        parent: Gtk.Window,
        client: HydraGUIClient,
        settings: Optional[Dict[str, Any]],
        on_created: Callable[[Optional[str]], None],
        initial_url: str = "",
    ) -> None:
        super().__init__(transient_for=parent, modal=True)
        self.client = client
        self.settings = settings
        self.on_created = on_created

        self.set_title("New Download")
        self.set_default_size(560, 480)
        self.set_resizable(True)

        self._fallback_dir = str(resolve_downloads_dir(None))
        self._filename_touched = False
        self._folder_touched = False
        self._syncing_auto_fields = False
        self._submitting = False

        toolbar = Adw.ToolbarView()
        header = Adw.HeaderBar()
        header.set_show_end_title_buttons(True)
        toolbar.add_top_bar(header)

        cancel_btn = Gtk.Button(label="Cancel")
        cancel_btn.connect("clicked", lambda *_: self.close())
        header.pack_start(cancel_btn)

        self._submit_btn = Gtk.Button(label="Download")
        self._submit_btn.add_css_class("suggested-action")
        self._submit_btn.connect("clicked", self._on_submit)
        header.pack_end(self._submit_btn)

        scroll = Gtk.ScrolledWindow()
        scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        scroll.set_vexpand(True)

        body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=14)
        body.set_margin_top(16)
        body.set_margin_bottom(18)
        body.set_margin_start(18)
        body.set_margin_end(18)
        scroll.set_child(body)
        toolbar.set_content(scroll)
        self.set_content(toolbar)

        # Inline error banner
        self._error_label = Gtk.Label(xalign=0.0, wrap=True)
        self._error_label.add_css_class("error-banner-card")
        self._error_label.set_visible(False)
        body.append(self._error_label)

        # URL input
        url_lbl = Gtk.Label(label="Download URL", xalign=0.0)
        url_lbl.add_css_class("metric-strong")
        body.append(url_lbl)

        url_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        self._url_entry = Gtk.Entry()
        self._url_entry.set_placeholder_text("https://example.com/archive.zip")
        self._url_entry.set_hexpand(True)
        self._url_entry.connect("changed", self._on_url_changed)
        self._url_entry.connect("activate", self._on_submit)

        paste_btn = Gtk.Button.new_from_icon_name("edit-paste-symbolic")
        paste_btn.set_tooltip_text("Paste URL from clipboard")
        paste_btn.connect("clicked", self._on_paste_url)

        url_row.append(self._url_entry)
        url_row.append(paste_btn)
        body.append(url_row)

        # Filename input
        name_lbl = Gtk.Label(label="Save As (Filename)", xalign=0.0)
        name_lbl.add_css_class("metric-strong")
        body.append(name_lbl)

        self._filename_entry = Gtk.Entry()
        self._filename_entry.set_placeholder_text("downloaded_file.bin")
        self._filename_entry.connect("changed", self._on_filename_changed)
        self._filename_entry.connect("activate", self._on_submit)
        body.append(self._filename_entry)

        # Destination folder input + Browse button
        dir_lbl = Gtk.Label(label="Destination Folder", xalign=0.0)
        dir_lbl.add_css_class("metric-strong")
        body.append(dir_lbl)

        dir_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        default_dir = resolve_category_destination("downloaded_file.bin", settings, self._fallback_dir)
        self._dir_entry = Gtk.Entry()
        self._dir_entry.set_text(default_dir)
        self._dir_entry.set_hexpand(True)
        self._dir_entry.connect("changed", self._on_dir_changed)

        browse_btn = Gtk.Button(label="Browse...")
        browse_btn.connect("clicked", self._on_browse_folder)

        dir_row.append(self._dir_entry)
        dir_row.append(browse_btn)
        body.append(dir_row)

        # Full path preview + Free disk space
        self._preview_label = Gtk.Label(xalign=0.0, wrap=True)
        self._preview_label.add_css_class("job-subtitle-label")
        body.append(self._preview_label)

        self._space_label = Gtk.Label(label="Free disk space: checking...", xalign=0.0)
        self._space_label.add_css_class("telemetry-label")
        body.append(self._space_label)

        # Optional controls (speed cap, checksum, schedule)
        expander = Gtk.Expander(label="Advanced Options (Speed Limit, Checksum, Schedule)")
        adv_box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=10)
        adv_box.set_margin_top(10)

        speed_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        speed_lbl = Gtk.Label(label="Speed Limit (KB/s, 0 = inherit global/rule limit)", xalign=0.0)
        speed_lbl.set_hexpand(True)
        self._speed_spin = Gtk.SpinButton.new_with_range(0, 1024 * 1024, 128)
        self._speed_spin.set_value(0)
        speed_row.append(speed_lbl)
        speed_row.append(self._speed_spin)
        adv_box.append(speed_row)

        ck_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        self._algo_dropdown = Gtk.DropDown.new_from_strings(["No Checksum", "sha256", "md5", "crc32"])
        self._algo_dropdown.set_selected(0)
        self._checksum_entry = Gtk.Entry()
        self._checksum_entry.set_placeholder_text("Expected checksum hex (optional)")
        self._checksum_entry.set_hexpand(True)
        ck_row.append(self._algo_dropdown)
        ck_row.append(self._checksum_entry)
        adv_box.append(ck_row)

        sched_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        sched_lbl = Gtk.Label(label="Schedule Start", xalign=0.0)
        self._sched_entry = Gtk.Entry()
        self._sched_entry.set_placeholder_text("RFC3339 or YYYY-MM-DDTHH:MM (blank = start now)")
        self._sched_entry.set_hexpand(True)
        sched_row.append(sched_lbl)
        sched_row.append(self._sched_entry)

        sched_cfg = settings.get("schedule") if isinstance(settings, dict) else None
        default_time_str = str((sched_cfg or {}).get("default_time") or "").strip() or "23:00"
        if parse_clock_minutes(default_time_str) is not None:
            preset_btn = Gtk.Button(label=f"Next {default_time_str}")
            preset_btn.set_tooltip_text(f"Schedule for next {default_time_str} local time")
            preset_btn.connect("clicked", lambda *_: self._apply_schedule_preset(default_time_str))
            sched_row.append(preset_btn)

        adv_box.append(sched_row)

        self._start_paused_chk = Gtk.CheckButton(label="Add to queue paused (start manually later)")
        adv_box.append(self._start_paused_chk)

        hdr_lbl = Gtk.Label(label="Custom HTTP Headers (optional, one 'Name: Value' per line)", xalign=0.0)
        hdr_lbl.add_css_class("job-subtitle-label")
        adv_box.append(hdr_lbl)

        hdr_frame = Gtk.Frame()
        hdr_scroll = Gtk.ScrolledWindow()
        hdr_scroll.set_min_content_height(64)
        hdr_scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        self._headers_view = Gtk.TextView()
        self._headers_view.set_wrap_mode(Gtk.WrapMode.WORD_CHAR)
        hdr_scroll.set_child(self._headers_view)
        hdr_frame.set_child(hdr_scroll)
        adv_box.append(hdr_frame)

        expander.set_child(adv_box)
        body.append(expander)

        if initial_url.strip():
            self._url_entry.set_text(initial_url.strip())

        self._update_preview_and_space()

    def _apply_schedule_preset(self, hhmm: str) -> None:
        mins = parse_clock_minutes(hhmm)
        if mins is None:
            return
        now = datetime.now().astimezone()
        target = now.replace(hour=mins // 60, minute=mins % 60, second=0, microsecond=0)
        if target <= now:
            target += timedelta(days=1)
        self._sched_entry.set_text(target.isoformat(timespec="seconds"))

    def _on_paste_url(self, *_args: Any) -> None:
        clipboard = self.get_clipboard()

        def _done(_cb: Gdk.Clipboard, res: Gio.AsyncResult) -> None:
            try:
                text = clipboard.read_text_finish(res)
                if text:
                    self._url_entry.set_text(text.strip())
            except GLib.Error:
                pass

        clipboard.read_text_async(None, _done)

    def _on_url_changed(self, entry: Gtk.Entry) -> None:
        if not self._filename_touched:
            suggested = suggest_filename_from_url(entry.get_text())
            self._syncing_auto_fields = True
            self._filename_entry.set_text(suggested)
            if not self._folder_touched:
                cat_dir = resolve_category_destination(suggested, self.settings, self._fallback_dir)
                self._dir_entry.set_text(cat_dir)
            self._syncing_auto_fields = False
        self._update_preview_and_space()

    def _on_filename_changed(self, entry: Gtk.Entry) -> None:
        if not self._syncing_auto_fields:
            self._filename_touched = bool(entry.get_text().strip())
            if not self._folder_touched:
                cat_dir = resolve_category_destination(
                    entry.get_text(),
                    self.settings,
                    self._fallback_dir,
                )
                self._syncing_auto_fields = True
                self._dir_entry.set_text(cat_dir)
                self._syncing_auto_fields = False
        self._update_preview_and_space()

    def _on_dir_changed(self, entry: Gtk.Entry) -> None:
        if not self._syncing_auto_fields:
            self._folder_touched = True
        self._update_preview_and_space()

    def _on_browse_folder(self, *_args: Any) -> None:
        _open_folder_picker(self, self._dir_entry.get_text(), self._set_chosen_folder)

    def _set_chosen_folder(self, path: str) -> None:
        self._folder_touched = True
        self._dir_entry.set_text(path)

    def _update_preview_and_space(self) -> None:
        folder = expand_path(self._dir_entry.get_text().strip()) or self._fallback_dir
        name = self._filename_entry.get_text().strip() or "downloaded_file.bin"
        full = os.path.join(folder, name)
        self._preview_label.set_text(f"Full Save Path: {full}")
        _probe_disk_space_async(self.client, folder, self._space_label.set_text)

    def _show_error(self, msg: str) -> None:
        if msg:
            self._error_label.set_text(msg)
            self._error_label.set_visible(True)
        else:
            self._error_label.set_visible(False)

    def _on_submit(self, *_args: Any) -> None:
        if self._submitting:
            return

        url = self._url_entry.get_text().strip()
        filename = self._filename_entry.get_text().strip()
        folder = expand_path(self._dir_entry.get_text().strip())

        if err := validate_download_url(url):
            self._show_error(err)
            return
        if err := validate_filename(filename):
            self._show_error(err)
            return
        if not folder:
            self._show_error("Destination folder cannot be empty.")
            return

        self._show_error("")
        full_path = os.path.join(folder, filename)
        speed_limit_kbps = int(self._speed_spin.get_value())
        max_speed_bytes = speed_limit_kbps * 1024 if speed_limit_kbps > 0 else 0

        algos = ["", "sha256", "md5", "crc32"]
        algo_idx = self._algo_dropdown.get_selected()
        algo = algos[algo_idx] if 0 <= algo_idx < len(algos) else ""
        checksum = self._checksum_entry.get_text().strip()
        sched = self._sched_entry.get_text().strip()
        start_paused = self._start_paused_chk.get_active()

        buf = self._headers_view.get_buffer()
        headers_text = buf.get_text(buf.get_start_iter(), buf.get_end_iter(), False)
        headers, hdr_err = parse_headers_text(headers_text)
        if hdr_err:
            self._show_error(hdr_err)
            return

        self._submitting = True
        self._submit_btn.set_sensitive(False)
        self._submit_btn.set_label("Starting...")

        def worker() -> None:
            ok, job_id, err_msg = self.client.create_download(
                url=url,
                save_path=full_path,
                filename=filename,
                headers=headers,
                max_speed_bytes=max_speed_bytes,
                expected_checksum=checksum,
                checksum_algo=algo if checksum else "",
                scheduled_at=sched,
                start_paused=start_paused,
            )

            def _done() -> bool:
                self._submitting = False
                self._submit_btn.set_sensitive(True)
                self._submit_btn.set_label("Download")
                if ok:
                    self.on_created(job_id)
                    self.close()
                else:
                    self._show_error(err_msg or "Failed to submit download to Hydra daemon.")
                return False

            GLib.idle_add(_done)

        threading.Thread(target=worker, daemon=True).start()


class EditDestinationDialog(Adw.Window):
    """Dialog for editing destination folder, filename, speed cap, and schedule on an existing job."""

    def __init__(
        self,
        parent: Gtk.Window,
        client: HydraGUIClient,
        job: Dict[str, Any],
        settings: Optional[Dict[str, Any]],
        on_saved: Callable[[str], None],
    ) -> None:
        super().__init__(transient_for=parent, modal=True)
        self.client = client
        self.job_id = str(job.get("id") or "")
        self.on_saved = on_saved

        chunks = job.get("chunks") or []
        raw_save = str(job.get("save_path") or "").strip()
        self._has_written_chunks = bool(chunks) and raw_save not in {"", "PENDING", "DEFAULT"}
        self._locked_save_path = raw_save

        self.set_title("Download Options & Destination")
        self.set_default_size(540, 440)

        self._submitting = False
        fallback_dir = str(resolve_downloads_dir(None))
        current_name = display_name_for_job(job)
        if raw_save and raw_save not in {"PENDING", "DEFAULT"}:
            initial_dir = os.path.dirname(raw_save) or fallback_dir
        else:
            initial_dir = resolve_category_destination(current_name, settings, fallback_dir)

        toolbar = Adw.ToolbarView()
        header = Adw.HeaderBar()
        toolbar.add_top_bar(header)

        cancel_btn = Gtk.Button(label="Cancel")
        cancel_btn.connect("clicked", lambda *_: self.close())
        header.pack_start(cancel_btn)

        status = str(job.get("status") or "").upper()
        submit_text = "Save & Start" if status == "PENDING_PATH" else "Save Changes"
        self._save_btn = Gtk.Button(label=submit_text)
        self._save_btn.add_css_class("suggested-action")
        self._save_btn.connect("clicked", self._on_save)
        header.pack_end(self._save_btn)

        body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
        body.set_margin_top(16)
        body.set_margin_bottom(18)
        body.set_margin_start(18)
        body.set_margin_end(18)
        toolbar.set_content(body)
        self.set_content(toolbar)

        self._error_label = Gtk.Label(xalign=0.0, wrap=True)
        self._error_label.add_css_class("error-banner-card")
        self._error_label.set_visible(False)
        body.append(self._error_label)

        info_lbl = Gtk.Label(
            label=f"Source: {job.get('url') or self.job_id}",
            xalign=0.0,
            wrap=True,
        )
        info_lbl.add_css_class("job-subtitle-label")
        body.append(info_lbl)

        name_lbl = Gtk.Label(label="Filename", xalign=0.0)
        name_lbl.add_css_class("metric-strong")
        body.append(name_lbl)

        self._filename_entry = Gtk.Entry()
        self._filename_entry.set_text(current_name)
        self._filename_entry.set_sensitive(not self._has_written_chunks)
        self._filename_entry.connect("changed", lambda *_: self._refresh_preview())
        self._filename_entry.connect("activate", self._on_save)
        body.append(self._filename_entry)

        dir_lbl = Gtk.Label(label="Destination Folder", xalign=0.0)
        dir_lbl.add_css_class("metric-strong")
        body.append(dir_lbl)

        dir_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        self._dir_entry = Gtk.Entry()
        self._dir_entry.set_text(initial_dir)
        self._dir_entry.set_hexpand(True)
        self._dir_entry.set_sensitive(not self._has_written_chunks)
        self._dir_entry.connect("changed", lambda *_: self._refresh_preview())

        browse_btn = Gtk.Button(label="Browse...")
        browse_btn.set_sensitive(not self._has_written_chunks)
        browse_btn.connect(
            "clicked",
            lambda *_: _open_folder_picker(self, self._dir_entry.get_text(), self._dir_entry.set_text),
        )
        dir_row.append(self._dir_entry)
        dir_row.append(browse_btn)
        body.append(dir_row)

        if self._has_written_chunks:
            lock_note = Gtk.Label(
                label="Partial chunk data already exists on disk; filename and destination folder are locked.",
                xalign=0.0,
                wrap=True,
            )
            lock_note.add_css_class("job-subtitle-label")
            body.append(lock_note)

        self._preview_label = Gtk.Label(xalign=0.0, wrap=True)
        self._preview_label.add_css_class("job-subtitle-label")
        body.append(self._preview_label)

        self._space_label = Gtk.Label(label="Free disk space: checking...", xalign=0.0)
        self._space_label.add_css_class("telemetry-label")
        body.append(self._space_label)

        # Speed cap & schedule overrides
        speed_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        speed_lbl = Gtk.Label(label="Speed Limit (KB/s, 0 = inherit global/rule limit)", xalign=0.0)
        speed_lbl.set_hexpand(True)
        self._speed_spin = Gtk.SpinButton.new_with_range(0, 1024 * 1024, 128)
        self._speed_spin.set_value(float(int(int(job.get("max_speed_bytes") or 0) / 1024)))
        speed_row.append(speed_lbl)
        speed_row.append(self._speed_spin)
        body.append(speed_row)

        sched_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        sched_lbl = Gtk.Label(label="Schedule Start", xalign=0.0)
        self._sched_entry = Gtk.Entry()
        self._sched_entry.set_placeholder_text("RFC3339 or YYYY-MM-DDTHH:MM (blank = queue now)")
        raw_sched = str(job.get("scheduled_at") or "").strip()
        if raw_sched and not raw_sched.startswith("0001-01-01"):
            self._sched_entry.set_text(raw_sched)
        self._sched_entry.set_hexpand(True)
        clear_sched_btn = Gtk.Button(label="Now")
        clear_sched_btn.set_tooltip_text("Clear scheduled start time to queue immediately")
        clear_sched_btn.connect("clicked", lambda *_: self._sched_entry.set_text(""))
        sched_row.append(sched_lbl)
        sched_row.append(self._sched_entry)
        sched_row.append(clear_sched_btn)
        body.append(sched_row)

        self._refresh_preview()

    def _refresh_preview(self) -> None:
        if self._has_written_chunks:
            full = self._locked_save_path
            folder = os.path.dirname(full)
        else:
            folder = expand_path(self._dir_entry.get_text().strip())
            name = self._filename_entry.get_text().strip()
            full = os.path.join(folder, name)
        self._preview_label.set_text(f"Target File: {full}")
        _probe_disk_space_async(self.client, folder, self._space_label.set_text)

    def _on_save(self, *_args: Any) -> None:
        if self._submitting:
            return
        if self._has_written_chunks:
            full_path = self._locked_save_path
            filename = os.path.basename(full_path)
        else:
            filename = self._filename_entry.get_text().strip()
            folder = expand_path(self._dir_entry.get_text().strip())
            if err := validate_filename(filename):
                self._error_label.set_text(err)
                self._error_label.set_visible(True)
                return
            if not folder:
                self._error_label.set_text("Destination folder cannot be empty.")
                self._error_label.set_visible(True)
                return
            full_path = os.path.join(folder, filename)

        speed_kbps = int(self._speed_spin.get_value())
        max_speed_bytes = speed_kbps * 1024 if speed_kbps > 0 else 0
        sched = self._sched_entry.get_text().strip()

        self._error_label.set_visible(False)
        self._submitting = True
        self._save_btn.set_sensitive(False)

        def worker() -> None:
            ok, err_msg = self.client.update_job_destination(
                job_id=self.job_id,
                save_path=full_path,
                filename=filename,
                scheduled_at=sched,
                clear_schedule=(sched == ""),
                max_speed_bytes=max_speed_bytes if max_speed_bytes > 0 else None,
                reset_speed_limit=(max_speed_bytes == 0),
            )

            def _done() -> bool:
                self._submitting = False
                self._save_btn.set_sensitive(True)
                if ok:
                    self.on_saved(self.job_id)
                    self.close()
                else:
                    self._error_label.set_text(err_msg or "Could not update destination.")
                    self._error_label.set_visible(True)
                return False

            GLib.idle_add(_done)

        threading.Thread(target=worker, daemon=True).start()


class BatchDownloadDialog(Adw.Window):
    """Dialog for submitting a batch of download URLs (pasted or loaded from a text file)."""

    def __init__(
        self,
        parent: Gtk.Window,
        client: HydraGUIClient,
        settings: Optional[Dict[str, Any]],
        on_created: Callable[[int], None],
    ) -> None:
        super().__init__(transient_for=parent, modal=True)
        self.client = client
        self.settings = settings
        self.on_created = on_created
        self._submitting = False
        self._fallback_dir = str(resolve_downloads_dir(None))

        self.set_title("Batch Download")
        self.set_default_size(580, 540)
        self.set_resizable(True)

        toolbar = Adw.ToolbarView()
        header = Adw.HeaderBar()
        toolbar.add_top_bar(header)

        cancel_btn = Gtk.Button(label="Cancel")
        cancel_btn.connect("clicked", lambda *_: self.close())
        header.pack_start(cancel_btn)

        self._submit_btn = Gtk.Button(label="Queue Batch")
        self._submit_btn.add_css_class("suggested-action")
        self._submit_btn.connect("clicked", self._on_submit)
        header.pack_end(self._submit_btn)

        scroll = Gtk.ScrolledWindow()
        scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        scroll.set_vexpand(True)

        body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
        body.set_margin_top(16)
        body.set_margin_bottom(18)
        body.set_margin_start(18)
        body.set_margin_end(18)
        scroll.set_child(body)
        toolbar.set_content(scroll)
        self.set_content(toolbar)

        self._error_label = Gtk.Label(xalign=0.0, wrap=True)
        self._error_label.add_css_class("error-banner-card")
        self._error_label.set_visible(False)
        body.append(self._error_label)

        urls_hdr = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        urls_lbl = Gtk.Label(label="Download URLs (one HTTP/HTTPS URL per line)", xalign=0.0)
        urls_lbl.add_css_class("metric-strong")
        urls_lbl.set_hexpand(True)

        import_btn = Gtk.Button(label="Import File...")
        import_btn.set_tooltip_text("Load URLs from a text file")
        import_btn.connect("clicked", self._on_import_file)

        urls_hdr.append(urls_lbl)
        urls_hdr.append(import_btn)
        body.append(urls_hdr)

        urls_frame = Gtk.Frame()
        urls_scroll = Gtk.ScrolledWindow()
        urls_scroll.set_min_content_height(150)
        urls_scroll.set_policy(Gtk.PolicyType.NEVER, Gtk.PolicyType.AUTOMATIC)
        self._urls_view = Gtk.TextView()
        self._urls_view.set_wrap_mode(Gtk.WrapMode.WORD_CHAR)
        self._urls_view.get_buffer().connect("changed", lambda *_: self._refresh_counts())
        urls_scroll.set_child(self._urls_view)
        urls_frame.set_child(urls_scroll)
        body.append(urls_frame)

        self._url_count_label = Gtk.Label(label="0 valid URL(s)", xalign=0.0)
        self._url_count_label.add_css_class("job-subtitle-label")
        body.append(self._url_count_label)

        self._auto_rules_chk = Gtk.CheckButton(
            label="Use global file-type download rules automatically (e.g. Video -> ~/Videos)"
        )
        self._auto_rules_chk.set_active(True)
        self._auto_rules_chk.connect("toggled", self._on_auto_rules_toggled)
        body.append(self._auto_rules_chk)

        dir_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        default_dir = resolve_category_destination("downloaded_file.bin", settings, self._fallback_dir)
        self._dir_entry = Gtk.Entry()
        self._dir_entry.set_text(default_dir)
        self._dir_entry.set_hexpand(True)
        self._dir_entry.set_sensitive(False)

        self._browse_btn = Gtk.Button(label="Browse...")
        self._browse_btn.set_sensitive(False)
        self._browse_btn.connect(
            "clicked",
            lambda *_: _open_folder_picker(self, self._dir_entry.get_text(), self._dir_entry.set_text),
        )
        dir_row.append(self._dir_entry)
        dir_row.append(self._browse_btn)
        body.append(dir_row)

        sched_row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        sched_lbl = Gtk.Label(label="Schedule Start", xalign=0.0)
        self._sched_entry = Gtk.Entry()
        self._sched_entry.set_placeholder_text("RFC3339 or YYYY-MM-DDTHH:MM (blank = queue now)")
        self._sched_entry.set_hexpand(True)
        sched_row.append(sched_lbl)
        sched_row.append(self._sched_entry)
        body.append(sched_row)

        self._start_paused_chk = Gtk.CheckButton(label="Add all batch items paused")
        body.append(self._start_paused_chk)

    def _on_auto_rules_toggled(self, chk: Gtk.CheckButton) -> None:
        custom = not chk.get_active()
        self._dir_entry.set_sensitive(custom)
        self._browse_btn.set_sensitive(custom)

    def _on_import_file(self, *_args: Any) -> None:
        dlg = Gtk.FileDialog()
        dlg.set_title("Select URL List File")

        def _done(_d: Gtk.FileDialog, res: Gio.AsyncResult) -> None:
            try:
                f = dlg.open_finish(res)
                if f is None:
                    return
                path = f.get_path()
                if not path:
                    return
                text = Path(path).read_text(encoding="utf-8", errors="replace")
                buf = self._urls_view.get_buffer()
                existing = buf.get_text(buf.get_start_iter(), buf.get_end_iter(), False).strip()
                combined = f"{existing}\n{text}" if existing else text
                buf.set_text(combined.strip())
            except (GLib.Error, OSError) as exc:
                self._error_label.set_text(f"Could not read file: {exc}")
                self._error_label.set_visible(True)

        dlg.open(self, None, _done)

    def _refresh_counts(self) -> None:
        buf = self._urls_view.get_buffer()
        raw = buf.get_text(buf.get_start_iter(), buf.get_end_iter(), False)
        valid, invalid = parse_batch_urls_text(raw)
        if invalid:
            self._url_count_label.set_text(f"{len(valid)} valid URL(s), {len(invalid)} invalid line(s) skipped")
        else:
            self._url_count_label.set_text(f"{len(valid)} valid URL(s)")

    def _on_submit(self, *_args: Any) -> None:
        if self._submitting:
            return
        buf = self._urls_view.get_buffer()
        raw = buf.get_text(buf.get_start_iter(), buf.get_end_iter(), False)
        valid, invalid = parse_batch_urls_text(raw)
        if not valid:
            msg = "Enter at least one valid http:// or https:// URL."
            if invalid:
                msg += f" First invalid entry: {invalid[0]}"
            self._error_label.set_text(msg)
            self._error_label.set_visible(True)
            return

        if self._auto_rules_chk.get_active():
            save_path = "DEFAULT"
        else:
            save_path = expand_path(self._dir_entry.get_text().strip())
            if not save_path:
                self._error_label.set_text("Destination folder cannot be empty.")
                self._error_label.set_visible(True)
                return

        self._error_label.set_visible(False)
        sched = self._sched_entry.get_text().strip()
        start_paused = self._start_paused_chk.get_active()

        self._submitting = True
        self._submit_btn.set_sensitive(False)
        self._submit_btn.set_label("Queueing...")

        def worker() -> None:
            ok, payload, err_msg = self.client.create_batch_download(
                urls=valid,
                save_path=save_path,
                scheduled_at=sched,
                start_paused=start_paused,
            )

            def _done() -> bool:
                self._submitting = False
                self._submit_btn.set_sensitive(True)
                self._submit_btn.set_label("Queue Batch")
                if ok:
                    count = int((payload or {}).get("total_queued") or len(valid))
                    self.on_created(count)
                    self.close()
                else:
                    self._error_label.set_text(err_msg or "Failed to queue batch downloads.")
                    self._error_label.set_visible(True)
                return False

            GLib.idle_add(_done)

        threading.Thread(target=worker, daemon=True).start()


class UpdateURLDialog(Adw.Window):
    """Dialog for refreshing an expired download link and immediately retrying."""

    def __init__(
        self,
        parent: Gtk.Window,
        client: HydraGUIClient,
        job: Dict[str, Any],
        on_retried: Callable[[str], None],
    ) -> None:
        super().__init__(transient_for=parent, modal=True)
        self.client = client
        self.job_id = str(job.get("id") or "")
        self.on_retried = on_retried

        self.set_title("Update Download URL & Retry")
        self.set_default_size(520, 220)
        self._submitting = False

        toolbar = Adw.ToolbarView()
        header = Adw.HeaderBar()
        toolbar.add_top_bar(header)

        cancel_btn = Gtk.Button(label="Cancel")
        cancel_btn.connect("clicked", lambda *_: self.close())
        header.pack_start(cancel_btn)

        self._retry_btn = Gtk.Button(label="Update & Retry")
        self._retry_btn.add_css_class("suggested-action")
        self._retry_btn.connect("clicked", self._on_submit)
        header.pack_end(self._retry_btn)

        body = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=12)
        body.set_margin_top(16)
        body.set_margin_bottom(18)
        body.set_margin_start(18)
        body.set_margin_end(18)
        toolbar.set_content(body)
        self.set_content(toolbar)

        self._err_label = Gtk.Label(xalign=0.0, wrap=True)
        self._err_label.add_css_class("error-banner-card")
        self._err_label.set_visible(False)
        body.append(self._err_label)

        title = Gtk.Label(label=f"New URL for {display_name_for_job(job)}", xalign=0.0)
        title.add_css_class("metric-strong")
        body.append(title)

        row = Gtk.Box(orientation=Gtk.Orientation.HORIZONTAL, spacing=8)
        self._url_entry = Gtk.Entry()
        self._url_entry.set_text(str(job.get("url") or ""))
        self._url_entry.set_hexpand(True)
        self._url_entry.connect("activate", self._on_submit)

        paste_btn = Gtk.Button.new_from_icon_name("edit-paste-symbolic")
        paste_btn.set_tooltip_text("Paste URL from clipboard")
        paste_btn.connect("clicked", self._on_paste)
        row.append(self._url_entry)
        row.append(paste_btn)
        body.append(row)

    def _on_paste(self, *_args: Any) -> None:
        clipboard = self.get_clipboard()

        def _done(_cb: Gdk.Clipboard, res: Gio.AsyncResult) -> None:
            try:
                text = clipboard.read_text_finish(res)
                if text:
                    self._url_entry.set_text(text.strip())
            except GLib.Error:
                pass

        clipboard.read_text_async(None, _done)

    def _on_submit(self, *_args: Any) -> None:
        if self._submitting:
            return
        new_url = self._url_entry.get_text().strip()
        if err := validate_download_url(new_url):
            self._err_label.set_text(err)
            self._err_label.set_visible(True)
            return

        self._err_label.set_visible(False)
        self._submitting = True
        self._retry_btn.set_sensitive(False)

        def worker() -> None:
            ok, err_msg = self.client.update_job_url(self.job_id, new_url)
            if ok:
                ok, err_msg = self.client.resume_job(self.job_id)

            def _done() -> bool:
                self._submitting = False
                self._retry_btn.set_sensitive(True)
                if ok:
                    self.on_retried(self.job_id)
                    self.close()
                else:
                    self._err_label.set_text(err_msg or "Failed to update URL.")
                    self._err_label.set_visible(True)
                return False

            GLib.idle_add(_done)

        threading.Thread(target=worker, daemon=True).start()


def confirm_job_removal_dialog(
    parent: Gtk.Window,
    job: Dict[str, Any],
    mode: str,
    on_confirm: Callable[[], None],
    on_confirm_keep_file: Optional[Callable[[], None]] = None,
) -> None:
    """Present a native Adw.AlertDialog before cancelling or deleting a download."""
    name = display_name_for_job(job)
    save_path = str(job.get("save_path") or "").strip()
    status = str(job.get("status") or "").upper()
    offer_keep_file = (
        on_confirm_keep_file is not None
        and mode != "cancel"
        and status == "COMPLETED"
        and save_path not in {"", "PENDING", "DEFAULT"}
    )

    dlg = Adw.AlertDialog()
    if mode == "cancel":
        dlg.set_heading("Cancel Download?")
        dlg.set_body(
            f"Cancel “{name}”?\n\nThis stops any active transfer and deletes incomplete partial files from disk."
        )
        confirm_id = "cancel_transfer"
        confirm_label = "Cancel Download"
    else:
        dlg.set_heading("Remove or Delete Download?")
        path_note = f" ({save_path})" if save_path and save_path not in {"PENDING", "DEFAULT"} else ""
        if offer_keep_file:
            dlg.set_body(
                f"Choose what to do with “{name}”:\n\n"
                f"Remove from List keeps the downloaded file{path_note}, while Delete File removes both the list entry and the file on disk."
            )
        else:
            dlg.set_body(
                f"Delete “{name}”?\n\nThis removes the entry from Hydra and deletes the file on disk{path_note}."
            )
        confirm_id = "delete_job"
        confirm_label = "Delete File & Entry" if offer_keep_file else "Delete"

    dlg.add_response("keep", "Keep")
    if offer_keep_file:
        dlg.add_response("remove_keep_file", "Remove from List (Keep File)")
        dlg.set_response_appearance("remove_keep_file", Adw.ResponseAppearance.SUGGESTED)
    dlg.add_response(confirm_id, confirm_label)
    dlg.set_response_appearance(confirm_id, Adw.ResponseAppearance.DESTRUCTIVE)
    dlg.set_default_response("keep")
    dlg.set_close_response("keep")

    def _on_response(_d: Adw.AlertDialog, response: str) -> None:
        if response == confirm_id:
            on_confirm()
        elif response == "remove_keep_file" and on_confirm_keep_file is not None:
            on_confirm_keep_file()

    dlg.connect("response", _on_response)
    dlg.present(parent)

