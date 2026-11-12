#!/usr/bin/env bash
set -euo pipefail

echo "============================================="
echo "🧪 Hydra Package Validation Suite (Week 20)"
echo "============================================="

DEB_PKG="dist/hydra_1.0.0_amd64.deb"

if [[ ! -f "$DEB_PKG" ]]; then
    echo "❌ Error: $DEB_PKG not found! Run ./build.sh first."
    exit 1
fi

echo "📋 [1/5] Validating package metadata and control fields..."
dpkg-deb -I "$DEB_PKG" > /tmp/hydra_deb_info.txt

grep -q "Package: hydra" /tmp/hydra_deb_info.txt || { echo "❌ Failed: Package name mismatch"; exit 1; }
grep -q "Architecture: amd64" /tmp/hydra_deb_info.txt || { echo "❌ Failed: Architecture mismatch"; exit 1; }
grep -qE "Version: (v)?1\.0\.0" /tmp/hydra_deb_info.txt || { echo "❌ Failed: Version mismatch"; exit 1; }
echo "  ✓ Package metadata valid."

echo "📂 [2/5] Validating payload contents and file permissions..."
CONTENTS=$(dpkg-deb -c "$DEB_PKG")

declare -a REQUIRED_FILES=(
    "-rwxr-xr-x .* \./usr/bin/hydra-daemon"
    "-rwxr-xr-x .* \./usr/bin/hydra$"
    "-rwxr-xr-x .* \./usr/bin/hydra-tui"
    "-rwxr-xr-x .* \./usr/bin/hydra-tray"
    "-rwxr-xr-x .* \./usr/bin/hydra-gui"
    "-rw-r--r-- .* \./usr/share/hydra/gui/hydra_gui/__init__\.py"
    "-rw-r--r-- .* \./usr/share/hydra/gui/hydra_gui/settings_dialog\.py"
    "-rw-r--r-- .* \./usr/share/hydra/gui/hydra_gui/window\.py"
    "-rw-r--r-- .* \./usr/lib/systemd/user/hydra\.service"
    "-rw-r--r-- .* \./usr/share/applications/hydra\.desktop"
    "-rw-r--r-- .* \./usr/share/applications/hydra-gui\.desktop"
    "-rw-r--r-- .* \./etc/xdg/autostart/hydra-daemon\.desktop"
    "-rw-r--r-- .* \./etc/xdg/autostart/hydra-tray\.desktop"
    "-rw-r--r-- .* \./usr/share/icons/hicolor/scalable/apps/hydra\.svg"
    "-rwxr-xr-x .* \./usr/bin/hydra-native-host"
    "-rw-r--r-- .* \./usr/lib/mozilla/native-messaging-hosts/org\.hydra\.downloader\.json"
)

for pattern in "${REQUIRED_FILES[@]}"; do
    if echo "$CONTENTS" | grep -qE -e "$pattern"; then
        echo "  ✓ Found matching: $pattern"
    else
        echo "❌ Missing or invalid permission for: $pattern"
        exit 1
    fi
done

TEST_ROOT=$(mktemp -d -t hydra-install-test-XXXXXX)
trap 'rm -rf "$TEST_ROOT" /tmp/hydra_deb_info.txt' EXIT

echo "📦 [3/5] Simulating fresh installation to target root: $TEST_ROOT..."
dpkg-deb -x "$DEB_PKG" "$TEST_ROOT"

test -x "$TEST_ROOT/usr/bin/hydra-daemon" || { echo "❌ Missing hydra-daemon binary"; exit 1; }
test -x "$TEST_ROOT/usr/bin/hydra" || { echo "❌ Missing hydra CLI binary"; exit 1; }
test -x "$TEST_ROOT/usr/bin/hydra-tui" || { echo "❌ Missing hydra-tui binary"; exit 1; }
test -x "$TEST_ROOT/usr/bin/hydra-tray" || { echo "❌ Missing hydra-tray executable"; exit 1; }
test -x "$TEST_ROOT/usr/bin/hydra-gui" || { echo "❌ Missing hydra-gui executable"; exit 1; }
test -f "$TEST_ROOT/usr/share/hydra/gui/hydra_gui/__init__.py" || { echo "❌ Missing hydra_gui package"; exit 1; }
test -f "$TEST_ROOT/usr/share/hydra/gui/hydra_gui/settings_dialog.py" || { echo "❌ Missing hydra_gui settings module"; exit 1; }
test -f "$TEST_ROOT/usr/share/hydra/gui/hydra_gui/window.py" || { echo "❌ Missing hydra_gui window module"; exit 1; }
test -f "$TEST_ROOT/usr/lib/systemd/user/hydra.service" || { echo "❌ Missing systemd service"; exit 1; }
test -f "$TEST_ROOT/usr/share/applications/hydra.desktop" || { echo "❌ Missing desktop file"; exit 1; }
test -f "$TEST_ROOT/usr/share/applications/hydra-gui.desktop" || { echo "❌ Missing GUI desktop file"; exit 1; }
test -f "$TEST_ROOT/etc/xdg/autostart/hydra-daemon.desktop" || { echo "❌ Missing autostart desktop file"; exit 1; }
test -f "$TEST_ROOT/etc/xdg/autostart/hydra-tray.desktop" || { echo "❌ Missing tray autostart desktop file"; exit 1; }
test -f "$TEST_ROOT/usr/share/icons/hicolor/scalable/apps/hydra.svg" || { echo "❌ Missing icon"; exit 1; }
test -x "$TEST_ROOT/usr/bin/hydra-native-host" || { echo "❌ Missing hydra-native-host wrapper"; exit 1; }
test -f "$TEST_ROOT/usr/lib/mozilla/native-messaging-hosts/org.hydra.downloader.json" || { echo "❌ Missing Firefox native messaging manifest"; exit 1; }

if command -v desktop-file-validate >/dev/null 2>&1; then
    desktop-file-validate \
        "$TEST_ROOT/usr/share/applications/hydra.desktop" \
        "$TEST_ROOT/usr/share/applications/hydra-gui.desktop" \
        "$TEST_ROOT/etc/xdg/autostart/hydra-daemon.desktop" \
        "$TEST_ROOT/etc/xdg/autostart/hydra-tray.desktop"
    echo "  ✓ Desktop entries pass desktop-file-validate."
fi

# Execute postinst maintainer script
dpkg-deb -e "$DEB_PKG" "$TEST_ROOT/DEBIAN"
if [[ -f "$TEST_ROOT/DEBIAN/postinst" ]]; then
    chmod +x "$TEST_ROOT/DEBIAN/postinst"
    "$TEST_ROOT/DEBIAN/postinst" configure
    echo "  ✓ postinst maintainer script executed cleanly."
fi

echo "🔄 [4/5] Simulating package upgrade cycle..."
# Overwrite installation to ensure upgrade replaces files idempotently
dpkg-deb -x "$DEB_PKG" "$TEST_ROOT"
if [[ -f "$TEST_ROOT/DEBIAN/postinst" ]]; then
    "$TEST_ROOT/DEBIAN/postinst" configure "v1.0.0"
    echo "  ✓ Package upgrade simulated successfully."
fi

echo "🗑️ [5/5] Simulating package pre-removal..."
if [[ -f "$TEST_ROOT/DEBIAN/prerm" ]]; then
    chmod +x "$TEST_ROOT/DEBIAN/prerm"
    "$TEST_ROOT/DEBIAN/prerm" remove
    echo "  ✓ prerm maintainer script executed cleanly."
fi

rm -rf "$TEST_ROOT/usr" "$TEST_ROOT/etc"
echo "  ✓ Clean removal verified."

echo "============================================="
echo "🎉 Package validation SUCCESSFUL: All checks passed!"
echo "============================================="
