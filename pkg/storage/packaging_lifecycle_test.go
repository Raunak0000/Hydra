package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSecurityReview_InstallScriptUpgradeAndUninstallLifecycle(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	workRoot := t.TempDir()
	binSrc := filepath.Join(workRoot, "bin")
	prefix := filepath.Join(workRoot, "prefix")
	cfgHome := filepath.Join(workRoot, "config")
	dataHome := filepath.Join(workRoot, "data")
	mozillaDir := filepath.Join(workRoot, "mozilla")
	if err := os.MkdirAll(binSrc, 0755); err != nil {
		t.Fatal(err)
	}
	for _, binName := range []string{"hydra-daemon", "hydra", "hydra-tui"} {
		if err := os.WriteFile(filepath.Join(binSrc, binName), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	runInstall := func(extraArgs ...string) {
		t.Helper()
		args := []string{
			"./install.sh",
			"--user",
			"--skip-build",
			"--no-start",
			"--bin-src", binSrc,
			"--prefix", prefix,
			"--xdg-config-home", cfgHome,
			"--xdg-data-home", dataHome,
			"--mozilla-native-dir", mozillaDir,
		}
		args = append(args, extraArgs...)
		cmd := exec.Command("bash", args...)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install.sh %v failed: %v\n%s", extraArgs, err, string(out))
		}
	}

	// 1. Fresh user install provisions binaries, desktop icons, hicolor index, native host, and token.
	runInstall()

	tokenPath := filepath.Join(cfgHome, "hydra", "api.token")
	firstToken, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read provisioned token: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "icons", "hicolor", "index.theme")); err != nil {
		t.Fatalf("expected hicolor index.theme: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "icons", "hicolor", "scalable", "apps", "hydra.svg")); err != nil {
		t.Fatalf("expected scalable hydra.svg: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "pixmaps", "hydra.svg")); err != nil {
		t.Fatalf("expected pixmaps hydra.svg: %v", err)
	}

	// Seed a user SQLite DB sentinel to confirm upgrade & uninstall preserve user data.
	dbSentinelDir := filepath.Join(dataHome, "hydra")
	if err := os.MkdirAll(dbSentinelDir, 0755); err != nil {
		t.Fatal(err)
	}
	dbSentinel := filepath.Join(dbSentinelDir, "hydra.db")
	if err := os.WriteFile(dbSentinel, []byte("user-queue-data"), 0600); err != nil {
		t.Fatal(err)
	}

	// 2. Idempotent upgrade re-installs cleanly without rotating existing token or wiping user DB.
	runInstall()
	secondToken, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("read token after upgrade: %v", err)
	}
	if string(secondToken) != string(firstToken) {
		t.Fatal("upgrade overwrote existing user API token")
	}

	// 3. Uninstall removes installed executables, desktop files, icons, and manifests while keeping DB & token.
	runInstall("--uninstall")
	for _, removed := range []string{
		filepath.Join(prefix, "bin", "hydra-daemon"),
		filepath.Join(prefix, "bin", "hydra"),
		filepath.Join(prefix, "bin", "hydra-tui"),
		filepath.Join(prefix, "bin", "hydra-gui"),
		filepath.Join(prefix, "bin", "hydra-tray"),
		filepath.Join(dataHome, "applications", "hydra.desktop"),
		filepath.Join(dataHome, "applications", "hydra-gui.desktop"),
		filepath.Join(dataHome, "icons", "hicolor", "scalable", "apps", "hydra.svg"),
		filepath.Join(dataHome, "pixmaps", "hydra.svg"),
		filepath.Join(mozillaDir, "org.hydra.downloader.json"),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed after --uninstall", removed)
		}
	}
	if _, err := os.Stat(dbSentinel); err != nil {
		t.Fatalf("expected user DB preserved on --uninstall: %v", err)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("expected API token preserved on --uninstall: %v", err)
	}
}

