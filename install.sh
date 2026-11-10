#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

MODE="auto"
PKG_FORMAT=""
UNINSTALL=0
SKIP_BUILD=0
NO_START=0
NO_TRAY=0
PREFIX=""
BIN_SRC="$SCRIPT_DIR/bin"
XDG_CONFIG_BASE="${XDG_CONFIG_HOME:-$HOME/.config}"
XDG_DATA_BASE="${XDG_DATA_HOME:-$HOME/.local/share}"

usage() {
    cat <<'EOF'
Hydra Installer — One-command installation for Linux

Usage: ./install.sh [OPTIONS]

Modes:
  --user                  Install for current user under ~/.local (default for non-root)
  --system                Install system-wide under /usr (default for root / sudo)
  --package               Build and install native Linux package (.deb or .rpm)
  --deb                   Build and install Debian (.deb) package
  --rpm                   Build and install RPM (.rpm) package
  --uninstall             Uninstall Hydra binaries, launcher, and login autostart

Options:
  --prefix DIR            Override install prefix (default: ~/.local or /usr)
  --bin-src DIR           Source directory for compiled binaries (default: ./bin)
  --xdg-config-home DIR   Override XDG config home (default: $XDG_CONFIG_HOME or ~/.config)
  --xdg-data-home DIR     Override XDG data home (default: $XDG_DATA_HOME or ~/.local/share)
  --skip-build            Use existing binaries in ./bin without running go build
  --no-start              Do not interact with systemd or start daemon immediately
  --no-tray               Do not install login autostart entry for optional hydra-tray
  -h, --help              Show this help message
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --user)
            MODE="user"
            shift
            ;;
        --system)
            MODE="system"
            shift
            ;;
        --package)
            MODE="package"
            shift
            ;;
        --deb)
            MODE="package"
            PKG_FORMAT="deb"
            shift
            ;;
        --rpm)
            MODE="package"
            PKG_FORMAT="rpm"
            shift
            ;;
        --uninstall)
            UNINSTALL=1
            shift
            ;;
        --prefix)
            PREFIX="$2"
            shift 2
            ;;
        --bin-src)
            BIN_SRC="$2"
            shift 2
            ;;
        --xdg-config-home)
            XDG_CONFIG_BASE="$2"
            shift 2
            ;;
        --xdg-data-home)
            XDG_DATA_BASE="$2"
            shift 2
            ;;
        --skip-build)
            SKIP_BUILD=1
            shift
            ;;
        --no-start)
            NO_START=1
            shift
            ;;
        --no-tray)
            NO_TRAY=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "Error: Unknown option: $1" >&2
            usage
            exit 1
            ;;
    esac
done

if [[ "$MODE" == "auto" ]]; then
    if [[ "$(id -u)" -eq 0 ]]; then
        MODE="system"
    else
        MODE="user"
    fi
fi

generate_token_hex() {
    if command -v openssl >/dev/null 2>&1; then
        openssl rand -hex 32
    else
        od -An -tx1 -N32 /dev/urandom | tr -d ' \n'
    fi
}

ensure_user_api_token() {
    local cfg_dir="$XDG_CONFIG_BASE/hydra"
    local token_file="$cfg_dir/api.token"
    if [[ ! -s "$token_file" ]]; then
        mkdir -p "$cfg_dir"
        chmod 755 "$cfg_dir"
        local tok
        tok="$(generate_token_hex)"
        umask 077
        printf '%s\n' "$tok" > "$token_file"
        chmod 600 "$token_file"
        echo "  ✓ Provisioned per-user API token at $token_file"
    else
        chmod 600 "$token_file"
        echo "  ✓ Using existing API token at $token_file"
    fi
}

build_binaries() {
    if [[ "$SKIP_BUILD" -eq 1 ]]; then
        for b in "$BIN_SRC/hydra-daemon" "$BIN_SRC/hydra" "$BIN_SRC/hydra-tui"; do
            if [[ ! -x "$b" ]]; then
                echo "Error: --skip-build specified but $b is missing." >&2
                exit 1
            fi
        done
        echo "  ✓ Using pre-built binaries in $BIN_SRC"
        return
    fi

    if ! command -v go >/dev/null 2>&1; then
        echo "Error: Go toolchain not found in PATH. Install Go or pass --skip-build." >&2
        exit 1
    fi

    echo "🔨 Building Hydra binaries (-ldflags='-s -w')..."
    mkdir -p "$BIN_SRC"
    go build -ldflags="-s -w" -o "$BIN_SRC/hydra-daemon" main.go
    go build -ldflags="-s -w" -o "$BIN_SRC/hydra" ./cmd/hydra-cli
    go build -ldflags="-s -w" -o "$BIN_SRC/hydra-tui" ./cmd/hydra-tui
}

run_as_root() {
    if [[ "$(id -u)" -eq 0 ]]; then
        "$@"
    elif command -v sudo >/dev/null 2>&1; then
        sudo "$@"
    else
        echo "Error: Root privileges required to run: $*" >&2
        exit 1
    fi
}

install_package_mode() {
    if [[ -z "$PKG_FORMAT" ]]; then
        if command -v dpkg >/dev/null 2>&1 && { [[ -f /etc/debian_version ]] || command -v apt-get >/dev/null 2>&1; }; then
            PKG_FORMAT="deb"
        elif command -v rpm >/dev/null 2>&1; then
            PKG_FORMAT="rpm"
        else
            echo "Error: Unable to detect .deb or .rpm package manager; pass --deb, --rpm, or --user." >&2
            exit 1
        fi
    fi

    if [[ "$UNINSTALL" -eq 1 ]]; then
        echo "🗑️  Removing installed Hydra package ($PKG_FORMAT)..."
        if [[ "$PKG_FORMAT" == "deb" ]]; then
            if command -v apt-get >/dev/null 2>&1; then
                run_as_root apt-get remove -y hydra
            else
                run_as_root dpkg -r hydra
            fi
        else
            if command -v dnf >/dev/null 2>&1; then
                run_as_root dnf remove -y hydra
            elif command -v zypper >/dev/null 2>&1; then
                run_as_root zypper --non-interactive remove hydra
            else
                run_as_root rpm -e hydra
            fi
        fi
        echo "✅ Hydra package removed."
        return
    fi

    if [[ "$SKIP_BUILD" -eq 0 ]]; then
        ./build.sh
    fi

    local pkg_file=""
    if [[ "$PKG_FORMAT" == "deb" ]]; then
        pkg_file="$(ls -1t dist/hydra_*_amd64.deb 2>/dev/null | head -n1 || true)"
        if [[ -z "$pkg_file" ]]; then
            echo "Error: No .deb package found in dist/. Run ./build.sh first." >&2
            exit 1
        fi
        echo "📦 Installing $pkg_file..."
        if command -v apt-get >/dev/null 2>&1; then
            run_as_root apt-get install -y "./$pkg_file"
        else
            run_as_root dpkg -i "./$pkg_file"
        fi
    else
        pkg_file="$(ls -1t dist/hydra-*.x86_64.rpm 2>/dev/null | head -n1 || true)"
        if [[ -z "$pkg_file" ]]; then
            echo "Error: No .rpm package found in dist/. Run ./build.sh first." >&2
            exit 1
        fi
        echo "📦 Installing $pkg_file..."
        if command -v dnf >/dev/null 2>&1; then
            run_as_root dnf install -y "./$pkg_file"
        elif command -v zypper >/dev/null 2>&1; then
            run_as_root zypper --non-interactive install --allow-unsigned-rpm "./$pkg_file"
        else
            run_as_root rpm -Uvh "./$pkg_file"
        fi
    fi

    if [[ "$(id -u)" -ne 0 ]]; then
        ensure_user_api_token
        if [[ "$NO_START" -eq 0 ]] && command -v systemctl >/dev/null 2>&1; then
            systemctl --user daemon-reload >/dev/null 2>&1 || true
            systemctl --user enable hydra.service >/dev/null 2>&1 || true
            if systemctl --user restart hydra.service >/dev/null 2>&1; then
                echo "  ✓ Hydra background engine started (systemd --user)"
            fi
        fi
    fi

    echo "✅ Hydra package installation complete."
}

configure_paths_for_mode() {
    if [[ "$MODE" == "system" ]]; then
        local sys_prefix="${PREFIX:-/usr}"
        BIN_DIR="$sys_prefix/bin"
        APP_DIR="$sys_prefix/share/applications"
        ICON_DIR="$sys_prefix/share/icons/hicolor/scalable/apps"
        SYSTEMD_USER_DIR="$sys_prefix/lib/systemd/user"
        if [[ -z "$PREFIX" || "$PREFIX" == "/usr" ]]; then
            AUTOSTART_DIR="/etc/xdg/autostart"
        else
            AUTOSTART_DIR="$sys_prefix/etc/xdg/autostart"
        fi
    else
        local usr_prefix="${PREFIX:-$HOME/.local}"
        BIN_DIR="$usr_prefix/bin"
        APP_DIR="$XDG_DATA_BASE/applications"
        ICON_DIR="$XDG_DATA_BASE/icons/hicolor/scalable/apps"
        SYSTEMD_USER_DIR="$XDG_CONFIG_BASE/systemd/user"
        AUTOSTART_DIR="$XDG_CONFIG_BASE/autostart"
    fi
}

install_files_mode() {
    configure_paths_for_mode

    if [[ "$UNINSTALL" -eq 1 ]]; then
        echo "🗑️  Uninstalling Hydra ($MODE mode)..."
        if [[ "$MODE" == "user" && "$NO_START" -eq 0 ]] && command -v systemctl >/dev/null 2>&1; then
            systemctl --user disable --now hydra.service >/dev/null 2>&1 || true
            systemctl --user daemon-reload >/dev/null 2>&1 || true
        elif [[ "$MODE" == "system" && "$NO_START" -eq 0 ]] && [[ "$(id -u)" -eq 0 ]] && command -v systemctl >/dev/null 2>&1; then
            systemctl --global disable hydra.service >/dev/null 2>&1 || true
        fi

        rm -f \
            "$BIN_DIR/hydra-daemon" \
            "$BIN_DIR/hydra" \
            "$BIN_DIR/hydra-tui" \
            "$BIN_DIR/hydra-tray" \
            "$SYSTEMD_USER_DIR/hydra.service" \
            "$AUTOSTART_DIR/hydra-daemon.desktop" \
            "$AUTOSTART_DIR/hydra-tray.desktop" \
            "$APP_DIR/hydra.desktop" \
            "$ICON_DIR/hydra.svg"

        if command -v update-desktop-database >/dev/null 2>&1; then
            update-desktop-database -q "$APP_DIR" >/dev/null 2>&1 || true
        fi

        echo "✅ Hydra uninstalled ($MODE). User database/config in $XDG_DATA_BASE/hydra and $XDG_CONFIG_BASE/hydra were preserved."
        return
    fi

    if [[ "$MODE" == "system" && "$(id -u)" -ne 0 && -z "$PREFIX" ]]; then
        echo "Error: --system installation into /usr requires root (run with sudo ./install.sh --system or omit for --user install)." >&2
        exit 1
    fi

    build_binaries

    echo "📥 Installing Hydra ($MODE mode)..."
    install -d -m 755 "$BIN_DIR" "$APP_DIR" "$ICON_DIR" "$SYSTEMD_USER_DIR" "$AUTOSTART_DIR"

    install -m 755 "$BIN_SRC/hydra-daemon" "$BIN_DIR/hydra-daemon"
    install -m 755 "$BIN_SRC/hydra" "$BIN_DIR/hydra"
    install -m 755 "$BIN_SRC/hydra-tui" "$BIN_DIR/hydra-tui"
    install -m 755 packaging/tray/hydra-tray "$BIN_DIR/hydra-tray"

    sed -e "s|/usr/bin/hydra-daemon|$BIN_DIR/hydra-daemon|g" \
        -e "s|/usr/bin/hydra-tui|$BIN_DIR/hydra-tui|g" \
        -e "s|/usr/bin/hydra-tray|$BIN_DIR/hydra-tray|g" \
        -e "s|/usr/bin/hydra |$BIN_DIR/hydra |g" \
        -e "s|/usr/bin/hydra$|$BIN_DIR/hydra|g" \
        packaging/desktop/hydra.desktop > "$APP_DIR/hydra.desktop"
    chmod 644 "$APP_DIR/hydra.desktop"
    install -m 644 packaging/icons/hydra.svg "$ICON_DIR/hydra.svg"

    sed "s|/usr/bin/hydra-daemon|$BIN_DIR/hydra-daemon|g" packaging/systemd/hydra.service \
        > "$SYSTEMD_USER_DIR/hydra.service"
    chmod 644 "$SYSTEMD_USER_DIR/hydra.service"

    sed "s|/usr/bin/hydra-daemon|$BIN_DIR/hydra-daemon|g" packaging/autostart/hydra-daemon.desktop \
        > "$AUTOSTART_DIR/hydra-daemon.desktop"
    chmod 644 "$AUTOSTART_DIR/hydra-daemon.desktop"

    if [[ "$NO_TRAY" -eq 0 ]]; then
        sed "s|/usr/bin/hydra-tray|$BIN_DIR/hydra-tray|g" packaging/autostart/hydra-tray.desktop \
            > "$AUTOSTART_DIR/hydra-tray.desktop"
        chmod 644 "$AUTOSTART_DIR/hydra-tray.desktop"
    fi

    if command -v update-desktop-database >/dev/null 2>&1; then
        update-desktop-database -q "$APP_DIR" >/dev/null 2>&1 || true
    fi
    if command -v gtk-update-icon-cache >/dev/null 2>&1; then
        gtk-update-icon-cache -q "$(dirname "$(dirname "$ICON_DIR")")" >/dev/null 2>&1 || true
    fi

    if [[ "$MODE" == "user" ]]; then
        ensure_user_api_token

        if [[ "$NO_START" -eq 0 ]]; then
            if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
                systemctl --user daemon-reload
                systemctl --user enable hydra.service >/dev/null 2>&1 || true
                systemctl --user restart hydra.service
                echo "  ✓ Systemd user service enabled and started (hydra.service)"
            else
                echo "  ℹ systemd --user session is unavailable in this shell; XDG autostart & systemd unit are installed for next login."
            fi
        fi

        case ":$PATH:" in
            *":$BIN_DIR:"*) ;;
            *)
                echo ""
                echo "  ⚠ Note: $BIN_DIR is not in your PATH. Add this to your shell profile:"
                echo "    export PATH=\"$BIN_DIR:\$PATH\""
                ;;
        esac
    else
        if [[ "$NO_START" -eq 0 && "$(id -u)" -eq 0 ]] && command -v systemctl >/dev/null 2>&1; then
            systemctl daemon-reload >/dev/null 2>&1 || true
            systemctl --global enable hydra.service >/dev/null 2>&1 || true
            echo "  ✓ Systemd user service enabled globally for user logins (hydra.service)"
        fi
    fi

    echo ""
    echo "✅ Hydra installation complete!"
    echo "   Binaries:   $BIN_DIR/{hydra-daemon,hydra,hydra-tui,hydra-tray}"
    echo "   Systemd:    $SYSTEMD_USER_DIR/hydra.service"
    echo "   Autostart:  $AUTOSTART_DIR/hydra-daemon.desktop"
    if [[ "$NO_TRAY" -eq 0 ]]; then
        echo "   Tray Auto:  $AUTOSTART_DIR/hydra-tray.desktop"
    fi
    echo "   Launcher:   $APP_DIR/hydra.desktop"
    echo ""
    echo "   Quick commands:"
    echo "     hydra status    # Check background download engine"
    echo "     hydra-tui       # Launch terminal UI"
    echo "     hydra-tray      # Launch optional system-tray controller"
}

if [[ "$MODE" == "package" ]]; then
    install_package_mode
else
    install_files_mode
fi
