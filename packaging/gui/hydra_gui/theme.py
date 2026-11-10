"""Modern Libadwaita + GTK 4 CSS styling for Hydra Graphical Download Manager."""

from __future__ import annotations

HYDRA_CSS = """
.hydra-sidebar {
    background-color: alpha(@view_bg_color, 0.96);
    border-right: 1px solid alpha(@borders, 0.7);
}

.sidebar-section-label {
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.06em;
    opacity: 0.62;
    margin-top: 12px;
    margin-bottom: 4px;
    margin-left: 12px;
    margin-right: 12px;
}

.sidebar-filter-row {
    border-radius: 8px;
    margin: 2px 6px;
    padding: 6px 10px;
}

.count-badge {
    font-size: 11px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    padding: 2px 8px;
    border-radius: 999px;
    background-color: alpha(@window_fg_color, 0.08);
    color: alpha(@window_fg_color, 0.85);
}

.header-status-pill {
    padding: 4px 12px;
    border-radius: 999px;
    font-size: 12px;
    font-weight: 600;
    font-variant-numeric: tabular-nums;
    border: 1px solid alpha(@borders, 0.55);
}

.header-status-online {
    background-color: alpha(#22c55e, 0.14);
    color: #22c55e;
    border-color: alpha(#22c55e, 0.35);
}

.header-status-offline {
    background-color: alpha(#ef4444, 0.14);
    color: #ef4444;
    border-color: alpha(#ef4444, 0.35);
}

.summary-toolbar {
    padding: 8px 14px;
    border-bottom: 1px solid alpha(@borders, 0.6);
    background-color: alpha(@headerbar_bg_color, 0.5);
}

.download-list-header {
    padding: 8px 18px;
    border-bottom: 1px solid alpha(@borders, 0.55);
    font-size: 11px;
    font-weight: 700;
    letter-spacing: 0.05em;
    opacity: 0.65;
}

.download-listview row {
    padding: 0;
    margin: 3px 10px;
    border-radius: 10px;
    border: 1px solid transparent;
}

.download-listview row:hover {
    background-color: alpha(@window_fg_color, 0.04);
}

.download-listview row:selected {
    background-color: alpha(@accent_bg_color, 0.15);
    border-color: alpha(@accent_bg_color, 0.45);
}

.download-row-box {
    padding: 10px 12px;
}

.job-title-label {
    font-weight: 600;
    font-size: 13.5px;
}

.job-subtitle-label {
    font-size: 11.5px;
    opacity: 0.68;
}

.tabular-metric {
    font-variant-numeric: tabular-nums;
    font-size: 12.5px;
}

.metric-strong {
    font-weight: 700;
    font-variant-numeric: tabular-nums;
    font-size: 12.5px;
}

.status-pill {
    font-size: 11px;
    font-weight: 700;
    padding: 3px 9px;
    border-radius: 999px;
    letter-spacing: 0.02em;
}

.status-downloading {
    background-color: alpha(#3b82f6, 0.18);
    color: #3b82f6;
}

.status-queued {
    background-color: alpha(#6366f1, 0.18);
    color: #6366f1;
}

.status-scheduled {
    background-color: alpha(#8b5cf6, 0.18);
    color: #8b5cf6;
}

.status-pending {
    background-color: alpha(#f97316, 0.18);
    color: #f97316;
}

.status-paused {
    background-color: alpha(#f59e0b, 0.18);
    color: #d97706;
}

.status-completed {
    background-color: alpha(#22c55e, 0.18);
    color: #16a34a;
}

.status-failed {
    background-color: alpha(#ef4444, 0.18);
    color: #ef4444;
}

progressbar.compact trough {
    min-height: 7px;
    border-radius: 999px;
    background-color: alpha(@window_fg_color, 0.10);
}

progressbar.compact progress {
    min-height: 7px;
    border-radius: 999px;
}

progressbar.progress-active progress {
    background-color: #3b82f6;
}

progressbar.progress-queued progress {
    background-color: #6366f1;
}

progressbar.progress-paused progress {
    background-color: #f59e0b;
}

progressbar.progress-completed progress {
    background-color: #22c55e;
}

progressbar.progress-failed progress {
    background-color: #ef4444;
}

.inspector-panel {
    background-color: alpha(@card_bg_color, 0.72);
    border-top: 1px solid alpha(@borders, 0.8);
}

.inspector-header-strip {
    padding: 10px 16px;
    border-bottom: 1px solid alpha(@borders, 0.55);
}

.telemetry-tile {
    background-color: alpha(@window_fg_color, 0.045);
    border: 1px solid alpha(@borders, 0.45);
    border-radius: 10px;
    padding: 10px 12px;
}

.telemetry-label {
    font-size: 11px;
    font-weight: 600;
    opacity: 0.65;
}

.telemetry-value {
    font-size: 16px;
    font-weight: 700;
    font-variant-numeric: tabular-nums;
}

.error-banner-card {
    background-color: alpha(#ef4444, 0.12);
    border: 1px solid alpha(#ef4444, 0.42);
    border-radius: 10px;
    padding: 10px 14px;
    color: #ef4444;
}

.chunk-row-card {
    background-color: alpha(@window_fg_color, 0.03);
    border: 1px solid alpha(@borders, 0.35);
    border-radius: 8px;
    padding: 6px 10px;
}

.legend-dot {
    font-size: 11px;
    opacity: 0.78;
}

.offline-banner {
    background-color: alpha(#f59e0b, 0.14);
    border-bottom: 1px solid alpha(#f59e0b, 0.4);
    padding: 8px 16px;
}

.action-strip {
    padding: 6px 14px;
    border-bottom: 1px solid alpha(@borders, 0.55);
    background-color: alpha(@window_bg_color, 0.72);
}

.action-strip button {
    font-size: 12px;
    padding: 4px 10px;
}
"""

