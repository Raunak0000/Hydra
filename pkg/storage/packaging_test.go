package storage

import (
	"os"
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
		"/usr/lib/systemd/user/hydra.service",
		"/usr/share/applications/hydra.desktop",
		"/etc/xdg/autostart/hydra-daemon.desktop",
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

func TestLoginAutostartDefinitions(t *testing.T) {
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
}

