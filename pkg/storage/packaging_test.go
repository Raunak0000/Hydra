package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagingConfiguration(t *testing.T) {
	// Root of the repository is two levels up from pkg/storage
	repoRoot := filepath.Join("..", "..")

	nfpmPath := filepath.Join(repoRoot, "nfpm.yaml")
	data, err := os.ReadFile(nfpmPath)
	if err != nil {
		t.Fatalf("failed to read nfpm.yaml: %v", err)
	}

	content := string(data)
	requiredEntries := []string{
		"name: \"hydra\"",
		"arch: \"amd64\"",
		"/usr/bin/hydra-daemon",
		"/usr/bin/hydra",
		"/usr/bin/hydra-tui",
		"/usr/bin/hydra-tray",
		"/usr/lib/systemd/user/hydra.service",
		"/usr/share/applications/hydra.desktop",
		"/etc/xdg/autostart/hydra-daemon.desktop",
		"/etc/xdg/autostart/hydra-tray.desktop",
		"/usr/share/icons/hicolor/scalable/apps/hydra.svg",
		"postinstall: ./packaging/scripts/postinst",
		"preremove: ./packaging/scripts/prerm",
	}

	for _, entry := range requiredEntries {
		if !strings.Contains(content, entry) {
			t.Errorf("nfpm.yaml missing expected entry: %s", entry)
		}
	}
}

func TestPackagingFilesExist(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	requiredFiles := []string{
		"install.sh",
		"packaging/desktop/hydra.desktop",
		"packaging/autostart/hydra-daemon.desktop",
		"packaging/autostart/hydra-tray.desktop",
		"packaging/tray/hydra-tray",
		"packaging/systemd/hydra.service",
		"packaging/icons/hydra.svg",
		"packaging/scripts/postinst",
		"packaging/scripts/prerm",
		"scripts/validate-package.sh",
	}

	for _, f := range requiredFiles {
		path := filepath.Join(repoRoot, f)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected packaging file missing: %s", f)
		}
	}
}

func TestLoginAutostartAndDesktopLauncherDefinitions(t *testing.T) {
	repoRoot := filepath.Join("..", "..")

	serviceBytes, err := os.ReadFile(filepath.Join(repoRoot, "packaging/systemd/hydra.service"))
	if err != nil {
		t.Fatalf("read hydra.service: %v", err)
	}
	serviceText := string(serviceBytes)
	for _, want := range []string{"ExecStart=/usr/bin/hydra-daemon", "WantedBy=default.target", "RestartPreventExitStatus=2"} {
		if !strings.Contains(serviceText, want) {
			t.Errorf("hydra.service missing %q", want)
		}
	}

	autostartBytes, err := os.ReadFile(filepath.Join(repoRoot, "packaging/autostart/hydra-daemon.desktop"))
	if err != nil {
		t.Fatalf("read hydra-daemon.desktop: %v", err)
	}
	autostartText := string(autostartBytes)
	for _, want := range []string{"Exec=/usr/bin/hydra-daemon --autostart", "X-GNOME-Autostart-enabled=true", "NoDisplay=true"} {
		if !strings.Contains(autostartText, want) {
			t.Errorf("hydra-daemon.desktop missing %q", want)
		}
	}

	trayAutoBytes, err := os.ReadFile(filepath.Join(repoRoot, "packaging/autostart/hydra-tray.desktop"))
	if err != nil {
		t.Fatalf("read hydra-tray.desktop: %v", err)
	}
	trayAutoText := string(trayAutoBytes)
	for _, want := range []string{"Exec=/usr/bin/hydra-tray --autostart", "X-GNOME-Autostart-enabled=true", "NoDisplay=true"} {
		if !strings.Contains(trayAutoText, want) {
			t.Errorf("hydra-tray.desktop missing %q", want)
		}
	}

	launcherBytes, err := os.ReadFile(filepath.Join(repoRoot, "packaging/desktop/hydra.desktop"))
	if err != nil {
		t.Fatalf("read hydra.desktop: %v", err)
	}
	launcherText := string(launcherBytes)
	for _, want := range []string{
		"Exec=/usr/bin/hydra-tui --ensure-daemon",
		"Terminal=true",
		"Actions=TrayController;OpenDownloads;PauseAll;ResumeAll;",
		"[Desktop Action TrayController]",
		"Exec=/usr/bin/hydra-tray",
	} {
		if !strings.Contains(launcherText, want) {
			t.Errorf("hydra.desktop missing %q", want)
		}
	}

	if validator, err := exec.LookPath("desktop-file-validate"); err == nil {
		cmd := exec.Command(
			validator,
			filepath.Join(repoRoot, "packaging/desktop/hydra.desktop"),
			filepath.Join(repoRoot, "packaging/autostart/hydra-daemon.desktop"),
			filepath.Join(repoRoot, "packaging/autostart/hydra-tray.desktop"),
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("desktop-file-validate failed: %v\n%s", err, string(out))
		}
	}
}
