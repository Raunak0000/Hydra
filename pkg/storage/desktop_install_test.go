package storage

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstanceLock_SingleInstanceAndRelease(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "runtime", "hydra.lock")

	lock1, err := AcquireInstanceLockAt(lockPath)
	if err != nil {
		t.Fatalf("first AcquireInstanceLockAt failed: %v", err)
	}
	if lock1.Path() != lockPath {
		t.Fatalf("lock Path() = %q, want %q", lock1.Path(), lockPath)
	}

	// Verify PID recorded inside lock file.
	pidBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if strings.TrimSpace(string(pidBytes)) != fmt.Sprintf("%d", os.Getpid()) {
		t.Fatalf("lock PID = %q, want %d", strings.TrimSpace(string(pidBytes)), os.Getpid())
	}

	// Second concurrent acquire on same path must fail with ErrDaemonAlreadyRunning.
	lock2, err := AcquireInstanceLockAt(lockPath)
	if !errors.Is(err, ErrDaemonAlreadyRunning) {
		if lock2 != nil {
			_ = lock2.Release()
		}
		t.Fatalf("expected ErrDaemonAlreadyRunning, got %v", err)
	}

	// Releasing lock1 cleans up path and allows re-acquisition.
	if err := lock1.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file still exists after Release: err=%v", err)
	}

	lock3, err := AcquireInstanceLockAt(lockPath)
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	_ = lock3.Release()
}

func TestEnsureAPIToken_CreatesReusesAndRespectsEnv(t *testing.T) {
	originalToken := hydraAPIToken
	t.Cleanup(func() {
		hydraAPIToken = originalToken
	})

	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HYDRA_API_TOKEN", "")
	hydraAPIToken = ""

	token1, err := EnsureAPIToken()
	if err != nil {
		t.Fatalf("EnsureAPIToken failed: %v", err)
	}
	if len(token1) != 64 {
		t.Fatalf("expected 64-char hex token, got length %d (%q)", len(token1), token1)
	}
	if hydraAPIToken != token1 {
		t.Fatalf("active server token = %q, want %q", hydraAPIToken, token1)
	}

	tokenPath := filepath.Join(cfgHome, "hydra", "api.token")
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("token file mode = %o, want 0600", info.Mode().Perm())
	}

	// Second EnsureAPIToken call must load and reuse the existing persisted token.
	hydraAPIToken = ""
	token2, err := EnsureAPIToken()
	if err != nil {
		t.Fatalf("second EnsureAPIToken failed: %v", err)
	}
	if token2 != token1 {
		t.Fatalf("second token %q != initial token %q", token2, token1)
	}

	// Env override takes precedence over file.
	t.Setenv("HYDRA_API_TOKEN", "explicit-env-token")
	envTok, err := LoadAPIToken()
	if err != nil {
		t.Fatalf("LoadAPIToken with env failed: %v", err)
	}
	if envTok != "explicit-env-token" {
		t.Fatalf("LoadAPIToken() = %q, want explicit-env-token", envTok)
	}
}

func TestAPIToken_RejectsSymlink(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HYDRA_API_TOKEN", "")

	hydraCfgDir := filepath.Join(cfgHome, "hydra")
	if err := os.MkdirAll(hydraCfgDir, 0755); err != nil {
		t.Fatal(err)
	}

	realTarget := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(realTarget, []byte("hijacked\n"), 0600); err != nil {
		t.Fatal(err)
	}

	tokenLink := filepath.Join(hydraCfgDir, "api.token")
	if err := os.Symlink(realTarget, tokenLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := LoadAPIToken(); err == nil {
		t.Fatal("expected LoadAPIToken to reject symlink token file")
	}
}

func TestInstallerScript_UserInstallAndUninstall(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	installScript := filepath.Join(repoRoot, "install.sh")

	root := t.TempDir()
	binSrc := filepath.Join(root, "src-bin")
	prefix := filepath.Join(root, "local")
	cfgHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "share")
	nativeDir := filepath.Join(root, "mozilla-native-hosts")

	if err := os.MkdirAll(binSrc, 0755); err != nil {
		t.Fatal(err)
	}
	for _, binName := range []string{"hydra-daemon", "hydra", "hydra-tui"} {
		p := filepath.Join(binSrc, binName)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	installCmd := exec.Command(
		installScript,
		"--user",
		"--skip-build",
		"--no-start",
		"--bin-src", binSrc,
		"--prefix", prefix,
		"--xdg-config-home", cfgHome,
		"--xdg-data-home", dataHome,
		"--mozilla-native-dir", nativeDir,
	)
	out, err := installCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\noutput:\n%s", err, string(out))
	}

	expectedDaemon := filepath.Join(prefix, "bin", "hydra-daemon")
	expectedTUI := filepath.Join(prefix, "bin", "hydra-tui")
	expectedTray := filepath.Join(prefix, "bin", "hydra-tray")
	expectedGUI := filepath.Join(prefix, "bin", "hydra-gui")
	expectedCLI := filepath.Join(prefix, "bin", "hydra")
	expectedNativeHost := filepath.Join(prefix, "bin", "hydra-native-host")
	expectedNativeManifest := filepath.Join(nativeDir, "org.hydra.downloader.json")

	for _, binName := range []string{"hydra-daemon", "hydra", "hydra-tui", "hydra-tray", "hydra-gui", "hydra-native-host"} {
		info, err := os.Stat(filepath.Join(prefix, "bin", binName))
		if err != nil {
			t.Fatalf("installed binary %s missing: %v", binName, err)
		}
		if info.Mode().Perm()&0111 == 0 {
			t.Fatalf("installed binary %s not executable: %o", binName, info.Mode().Perm())
		}
	}

	nativeManifestData, err := os.ReadFile(expectedNativeManifest)
	if err != nil {
		t.Fatalf("read installed native messaging manifest: %v", err)
	}
	if !strings.Contains(string(nativeManifestData), expectedNativeHost) {
		t.Fatalf("native messaging manifest does not reference %q:\n%s", expectedNativeHost, string(nativeManifestData))
	}

	guiModulePath := filepath.Join(dataHome, "hydra", "gui", "hydra_gui", "window.py")
	if _, err := os.Stat(guiModulePath); err != nil {
		t.Fatalf("installed GUI module missing: %s (%v)", guiModulePath, err)
	}

	servicePath := filepath.Join(cfgHome, "systemd", "user", "hydra.service")
	serviceData, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatalf("read installed hydra.service: %v", err)
	}
	if !strings.Contains(string(serviceData), "ExecStart="+expectedDaemon) {
		t.Fatalf("hydra.service does not reference %q:\n%s", expectedDaemon, string(serviceData))
	}

	autostartPath := filepath.Join(cfgHome, "autostart", "hydra-daemon.desktop")
	autostartData, err := os.ReadFile(autostartPath)
	if err != nil {
		t.Fatalf("read installed autostart desktop entry: %v", err)
	}
	if !strings.Contains(string(autostartData), "Exec="+expectedDaemon+" --autostart") {
		t.Fatalf("autostart desktop entry does not reference %q:\n%s", expectedDaemon, string(autostartData))
	}

	trayAutostartPath := filepath.Join(cfgHome, "autostart", "hydra-tray.desktop")
	trayAutostartData, err := os.ReadFile(trayAutostartPath)
	if err != nil {
		t.Fatalf("read installed tray autostart desktop entry: %v", err)
	}
	if !strings.Contains(string(trayAutostartData), "Exec="+expectedTray+" --autostart") {
		t.Fatalf("tray autostart desktop entry does not reference %q:\n%s", expectedTray, string(trayAutostartData))
	}

	launcherPath := filepath.Join(dataHome, "applications", "hydra.desktop")
	launcherData, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatalf("read installed launcher desktop entry: %v", err)
	}
	launcherText := string(launcherData)
	for _, want := range []string{
		"Exec=" + expectedTUI + " --ensure-daemon",
		"Exec=" + expectedTray,
		"Exec=" + expectedCLI + " pause-all",
		"Exec=" + expectedCLI + " resume-all",
	} {
		if !strings.Contains(launcherText, want) {
			t.Fatalf("installed hydra.desktop missing %q:\n%s", want, launcherText)
		}
	}

	guiLauncherPath := filepath.Join(dataHome, "applications", "hydra-gui.desktop")
	guiLauncherData, err := os.ReadFile(guiLauncherPath)
	if err != nil {
		t.Fatalf("read installed GUI launcher desktop entry: %v", err)
	}
	if !strings.Contains(string(guiLauncherData), "Exec="+expectedGUI+" --ensure-daemon") {
		t.Fatalf("installed hydra-gui.desktop missing expected Exec line:\n%s", string(guiLauncherData))
	}

	for _, p := range []string{
		launcherPath,
		guiLauncherPath,
		filepath.Join(dataHome, "icons", "hicolor", "scalable", "apps", "hydra.svg"),
		filepath.Join(cfgHome, "hydra", "api.token"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected installed file missing: %s (%v)", p, err)
		}
	}

	tokenInfo, err := os.Stat(filepath.Join(cfgHome, "hydra", "api.token"))
	if err != nil {
		t.Fatal(err)
	}
	if tokenInfo.Mode().Perm() != 0600 {
		t.Fatalf("provisioned api.token mode = %o, want 0600", tokenInfo.Mode().Perm())
	}

	uninstallCmd := exec.Command(
		installScript,
		"--user",
		"--uninstall",
		"--no-start",
		"--prefix", prefix,
		"--xdg-config-home", cfgHome,
		"--xdg-data-home", dataHome,
		"--mozilla-native-dir", nativeDir,
	)
	out, err = uninstallCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --uninstall failed: %v\noutput:\n%s", err, string(out))
	}

	for _, removed := range []string{
		expectedDaemon,
		expectedCLI,
		expectedTUI,
		expectedTray,
		expectedGUI,
		expectedNativeHost,
		expectedNativeManifest,
		guiModulePath,
		servicePath,
		autostartPath,
		trayAutostartPath,
		launcherPath,
		guiLauncherPath,
		filepath.Join(dataHome, "icons", "hicolor", "scalable", "apps", "hydra.svg"),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected file %s to be removed on uninstall", removed)
		}
	}

	// Verify --no-tray skips tray autostart while installing binaries.
	noTrayCmd := exec.Command(
		installScript,
		"--user",
		"--skip-build",
		"--no-start",
		"--no-tray",
		"--bin-src", binSrc,
		"--prefix", prefix,
		"--xdg-config-home", cfgHome,
		"--xdg-data-home", dataHome,
		"--mozilla-native-dir", nativeDir,
	)
	out, err = noTrayCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --no-tray failed: %v\noutput:\n%s", err, string(out))
	}
	if _, err := os.Stat(trayAutostartPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to not exist with --no-tray", trayAutostartPath)
	}
}

func TestHydraTrayController_UnitAndHeadlessAutostart(t *testing.T) {
	pythonBin, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable; skipping tray controller tests")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	unitCmd := exec.Command(pythonBin, "-B", "-m", "unittest", "discover", "-s", filepath.Join(repoRoot, "packaging", "tray"), "-p", "test_*.py")
	if out, err := unitCmd.CombinedOutput(); err != nil {
		t.Fatalf("tray unit tests failed: %v\n%s", err, string(out))
	}
}

func TestHydraGUI_UnitAndCheck(t *testing.T) {
	pythonBin, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable; skipping GUI unit tests")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	unitCmd := exec.Command(pythonBin, "-B", "-m", "unittest", "discover", "-s", filepath.Join(repoRoot, "packaging", "gui"), "-p", "test_*.py")
	if out, err := unitCmd.CombinedOutput(); err != nil {
		t.Fatalf("gui unit tests failed: %v\n%s", err, string(out))
	}
}
