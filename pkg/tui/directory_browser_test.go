package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func createTestDirectory(t *testing.T) string {
	t.Helper()

	root := t.TempDir()

	if err := os.Mkdir(filepath.Join(root, "Documents"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.Mkdir(filepath.Join(root, "Projects"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(root, "file.txt"),
		[]byte("hello"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(root, ".hidden"),
		[]byte("hidden"),
		0644,
	); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestReadDirectorySortsDirectoriesBeforeFiles(t *testing.T) {
	root := createTestDirectory(t)

	entries, err := readDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	if !entries[0].IsDir() {
		t.Fatalf("first entry should be a directory")
	}

	if !entries[1].IsDir() {
		t.Fatalf("second entry should be a directory")
	}

	if entries[2].IsDir() {
		t.Fatalf("third entry should be a file")
	}
}

func TestDirectoryBrowserHidesHiddenFiles(t *testing.T) {
	root := createTestDirectory(t)

	browser := NewDirectoryBrowser(root)

	entries, err := readDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	browser.entries = entries

	visible := browser.visibleEntries()

	for _, entry := range visible {
		if entry.Name() == ".hidden" {
			t.Fatal("hidden file should not be visible")
		}
	}
}

func TestDirectoryBrowserShowsHiddenFiles(t *testing.T) {
	root := createTestDirectory(t)

	browser := NewDirectoryBrowser(root)
	browser.showHidden = true

	entries, err := readDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	browser.entries = entries

	visible := browser.visibleEntries()

	found := false

	for _, entry := range visible {
		if entry.Name() == ".hidden" {
			found = true
			break
		}
	}

	if !found {
		t.Fatal("hidden file should be visible")
	}
}

func TestDirectoryBrowserMovesToParent(t *testing.T) {
	root := createTestDirectory(t)
	child := filepath.Join(root, "Documents")

	browser := NewDirectoryBrowser(child)

	updated, command := browser.goToParent()

	if command == nil {
		t.Fatal("expected directory-loading command")
	}

	if updated.currentPath != root {
		t.Fatalf(
			"current path = %q, want %q",
			updated.currentPath,
			root,
		)
	}
}

func TestDirectoryBrowserOpensDirectory(t *testing.T) {
	root := createTestDirectory(t)

	browser := NewDirectoryBrowser(root)

	entries, err := readDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	browser.entries = entries

	// Documents is the first visible entry because directories are sorted
	// before files. The parent entry is inserted at index 0.
	browser.selected = 1

	updated, command := browser.openSelected()

	if command == nil {
		t.Fatal("expected directory-loading command")
	}

	if updated.currentPath != filepath.Join(root, "Documents") {
		t.Fatalf(
			"current path = %q, want Documents directory",
			updated.currentPath,
		)
	}
}

func TestDirectoryBrowserSelectsCurrentDirectory(t *testing.T) {
	root := createTestDirectory(t)

	browser := NewDirectoryBrowser(root)

	_, command := browser.selectCurrentDirectory()

	if command == nil {
		t.Fatal("expected selection command")
	}

	msg := command()

	selected, ok := msg.(directoryBrowserSelectedMsg)
	if !ok {
		t.Fatalf("got %T, want directoryBrowserSelectedMsg", msg)
	}

	if selected.path != root {
		t.Fatalf(
			"selected path = %q, want %q",
			selected.path,
			root,
		)
	}
}

func TestDirectoryBrowserCancel(t *testing.T) {
	root := createTestDirectory(t)
	browser := NewDirectoryBrowser(root)

	updated, command := browser.Update(tea.KeyMsg{
		Type: tea.KeyEsc,
	})

	if command == nil {
		t.Fatal("expected cancel command")
	}

	_, ok := command().(directoryBrowserCancelledMsg)
	if !ok {
		t.Fatal("expected directoryBrowserCancelledMsg")
	}

	if updated.currentPath != root {
		t.Fatal("browser path unexpectedly changed")
	}
}

func TestDirectoryBrowserSelectionClamps(t *testing.T) {
	root := createTestDirectory(t)

	browser := NewDirectoryBrowser(root)

	entries, err := readDirectory(root)
	if err != nil {
		t.Fatal(err)
	}

	browser.entries = entries
	browser.selected = 0

	browser.moveSelection(-100)

	if browser.selected != 0 {
		t.Fatalf("selected = %d, want 0", browser.selected)
	}

	browser.moveSelection(100)

	if browser.selected != len(browser.visibleEntries())-1 {
		t.Fatalf(
			"selected = %d, want %d",
			browser.selected,
			len(browser.visibleEntries())-1,
		)
	}
}

func TestDirectoryBrowserHandlesMissingDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")

	browser := NewDirectoryBrowser(root)

	command := browser.Init()
	msg := command()

	loaded, ok := msg.(directoryBrowserLoadedMsg)
	if !ok {
		t.Fatalf("got %T, want directoryBrowserLoadedMsg", msg)
	}

	if loaded.err == nil {
		t.Fatal("expected an error for missing directory")
	}
}
