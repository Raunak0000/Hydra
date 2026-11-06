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
		"packaging/desktop/hydra.desktop",
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

