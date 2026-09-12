package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type DirectoryBrowser struct {
	currentPath string
	entries     []os.DirEntry
	selected    int
	scroll      int
	showHidden  bool
	err         error

	width  int
	height int

	lastClickTime  time.Time
	lastClickIndex int
}

type directoryBrowserLoadedMsg struct {
	path    string
	entries []os.DirEntry
	err     error
}

type directoryBrowserSelectedMsg struct {
	path string
}

type directoryBrowserCancelledMsg struct{}

type directoryBrowserStyles struct {
	title       lipgloss.Style
	path        lipgloss.Style
	selected    lipgloss.Style
	directory   lipgloss.Style
	file        lipgloss.Style
	parent      lipgloss.Style
	muted       lipgloss.Style
	errorStyle  lipgloss.Style
	footer      lipgloss.Style
	highlighted lipgloss.Style
	button      lipgloss.Style
}

var directoryStyles = directoryBrowserStyles{
	title: lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("205")),

	path: lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")),

	selected: lipgloss.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("230")).
		Bold(true),

	directory: lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")),

	file: lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")),

	parent: lipgloss.NewStyle().
		Foreground(lipgloss.Color("214")),

	muted: lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")),

	errorStyle: lipgloss.NewStyle().
		Foreground(lipgloss.Color("196")),

	footer: lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")),

	highlighted: lipgloss.NewStyle().
		Background(lipgloss.Color("237")),

	button: lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true),
}

func NewDirectoryBrowser(startPath string) DirectoryBrowser {
	startPath = expandHome(startPath)

	absolutePath, err := filepath.Abs(startPath)
	if err != nil {
		absolutePath = startPath
	}

	return DirectoryBrowser{
		currentPath:    filepath.Clean(absolutePath),
		selected:       0,
		scroll:         0,
		showHidden:     false,
		lastClickIndex: -1,
	}
}

func (b DirectoryBrowser) Init() tea.Cmd {
	return b.loadCurrentDirectory()
}

func (b DirectoryBrowser) loadCurrentDirectory() tea.Cmd {
	path := b.currentPath

	return func() tea.Msg {
		entries, err := readDirectory(path)

		return directoryBrowserLoadedMsg{
			path:    path,
			entries: entries,
			err:     err,
		}
	}
}

func readDirectory(path string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		left := entries[i]
		right := entries[j]

		// Directories appear before files.
		if left.IsDir() != right.IsDir() {
			return left.IsDir()
		}

		// Case-insensitive alphabetical ordering.
		leftName := strings.ToLower(left.Name())
		rightName := strings.ToLower(right.Name())

		if leftName == rightName {
			return left.Name() < right.Name()
		}

		return leftName < rightName
	})

	return entries, nil
}

func (b DirectoryBrowser) Update(msg tea.Msg) (DirectoryBrowser, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.width = msg.Width
		b.height = msg.Height
		b.clampScroll()

	case directoryBrowserLoadedMsg:
		// Ignore stale directory-loading responses.
		if msg.path != b.currentPath {
			return b, nil
		}

		b.entries = msg.entries
		b.err = msg.err
		b.selected = 0
		b.scroll = 0

		return b, nil

	case tea.KeyMsg:
		return b.updateKey(msg)

	case tea.MouseMsg:
		return b.updateMouse(msg)
	}

	return b, nil
}

func (b DirectoryBrowser) updateKey(msg tea.KeyMsg) (DirectoryBrowser, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		b.moveSelection(-1)

	case "down", "j":
		b.moveSelection(1)

	case "pgup", "ctrl+u":
		b.moveSelection(-b.visibleRows())

	case "pgdown", "ctrl+d":
		b.moveSelection(b.visibleRows())

	case "home", "g":
		b.selected = 0
		b.scroll = 0

	case "end", "G":
		entries := b.visibleEntries()

		if len(entries) > 0 {
			b.selected = len(entries) - 1
			b.clampScroll()
		}

	case "enter", "right", "l":
		return b.openSelected()

	case "backspace", "left", "h":
		return b.goToParent()

	case ".":
		b.showHidden = !b.showHidden
		b.selected = 0
		b.scroll = 0
		b.refreshVisibleEntries()

	case "s":
		return b.selectCurrentDirectory()

	case "esc", "q":
		return b, func() tea.Msg {
			return directoryBrowserCancelledMsg{}
		}
	}

	return b, nil
}

func (b DirectoryBrowser) updateMouse(msg tea.MouseMsg) (DirectoryBrowser, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		b.moveSelection(-1)
		return b, nil

	case tea.MouseButtonWheelDown:
		b.moveSelection(1)
		return b, nil
	}

	if msg.Button != tea.MouseButtonLeft ||
		msg.Action != tea.MouseActionPress {
		return b, nil
	}

	// Handle footer buttons first.
	if action := b.footerAction(msg.X, msg.Y); action != "" {
		switch action {
		case "select":
			return b.selectCurrentDirectory()

		case "back":
			return b.goToParent()

		case "cancel":
			return b, func() tea.Msg {
				return directoryBrowserCancelledMsg{}
			}
		}
	}

	index := b.indexFromMouseY(msg.Y)

	entries := b.visibleEntries()

	if index < 0 || index >= len(entries) {
		return b, nil
	}

	now := time.Now()

	// Double-click within 400 milliseconds.
	if b.lastClickIndex == index &&
		now.Sub(b.lastClickTime) <= 400*time.Millisecond {

		b.selected = index
		b.lastClickIndex = -1
		b.lastClickTime = time.Time{}

		return b.openSelected()
	}

	b.selected = index
	b.lastClickIndex = index
	b.lastClickTime = now

	b.clampScroll()

	return b, nil
}

func (b DirectoryBrowser) openSelected() (DirectoryBrowser, tea.Cmd) {
	entries := b.visibleEntries()

	if b.selected < 0 || b.selected >= len(entries) {
		return b, nil
	}

	entry := entries[b.selected]

	// Synthetic parent entry.
	if entry.Name() == ".." {
		return b.goToParent()
	}

	// Files cannot be opened as directories.
	if !entry.IsDir() {
		return b, nil
	}

	nextPath := filepath.Join(b.currentPath, entry.Name())

	// Validate the target and follow symlinks safely.
	info, err := os.Stat(nextPath)
	if err != nil {
		b.err = fmt.Errorf("cannot open directory: %w", err)
		return b, nil
	}

	if !info.IsDir() {
		b.err = fmt.Errorf("selected entry is not a directory")
		return b, nil
	}

	// Canonicalize the resulting path. This also catches broken
	// symlink chains and symlink loops.
	resolvedPath, err := filepath.EvalSymlinks(nextPath)
	if err != nil {
		b.err = fmt.Errorf("cannot resolve directory: %w", err)
		return b, nil
	}

	absolutePath, err := filepath.Abs(resolvedPath)
	if err != nil {
		b.err = fmt.Errorf("cannot resolve directory path: %w", err)
		return b, nil
	}

	b.currentPath = filepath.Clean(absolutePath)
	b.selected = 0
	b.scroll = 0
	b.err = nil

	return b, b.loadCurrentDirectory()
}

func (b DirectoryBrowser) goToParent() (DirectoryBrowser, tea.Cmd) {
	parent := filepath.Dir(b.currentPath)

	// Already at filesystem root.
	if parent == b.currentPath {
		return b, nil
	}

	b.currentPath = filepath.Clean(parent)
	b.selected = 0
	b.scroll = 0
	b.err = nil

	return b, b.loadCurrentDirectory()
}

func (b DirectoryBrowser) selectCurrentDirectory() (DirectoryBrowser, tea.Cmd) {
	selectedPath := filepath.Clean(b.currentPath)

	return b, func() tea.Msg {
		return directoryBrowserSelectedMsg{
			path: selectedPath,
		}
	}
}

func (b *DirectoryBrowser) moveSelection(delta int) {
	entries := b.visibleEntries()

	if len(entries) == 0 {
		return
	}

	b.selected += delta

	if b.selected < 0 {
		b.selected = 0
	}

	if b.selected >= len(entries) {
		b.selected = len(entries) - 1
	}

	b.clampScroll()
}

func (b *DirectoryBrowser) clampScroll() {
	visibleRows := b.visibleRows()
	entries := b.visibleEntries()

	if len(entries) == 0 {
		b.selected = 0
		b.scroll = 0
		return
	}

	if b.selected < 0 {
		b.selected = 0
	}

	if b.selected >= len(entries) {
		b.selected = len(entries) - 1
	}

	maxScroll := max(0, len(entries)-visibleRows)

	if b.scroll > maxScroll {
		b.scroll = maxScroll
	}

	if b.scroll < 0 {
		b.scroll = 0
	}

	if b.selected < b.scroll {
		b.scroll = b.selected
	}

	if b.selected >= b.scroll+visibleRows {
		b.scroll = b.selected - visibleRows + 1
	}
}

func (b DirectoryBrowser) visibleRows() int {
	// Reserve space for:
	// title, path, spacing, selected directory, buttons, footer.
	return max(1, b.height-8)
}

func (b DirectoryBrowser) visibleEntries() []os.DirEntry {
	entries := make([]os.DirEntry, 0, len(b.entries)+1)

	// Always show the parent entry unless already at filesystem root.
	if filepath.Dir(b.currentPath) != b.currentPath {
		entries = append(entries, parentEntry{})
	}

	for _, entry := range b.entries {
		if !b.showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		entries = append(entries, entry)
	}

	return entries
}

func (b *DirectoryBrowser) refreshVisibleEntries() {
	entries := b.visibleEntries()

	if len(entries) == 0 {
		b.selected = 0
		b.scroll = 0
		return
	}

	if b.selected >= len(entries) {
		b.selected = len(entries) - 1
	}

	b.clampScroll()
}

func (b DirectoryBrowser) entryStartY() int {
	y := 4

	if b.err != nil {
		y += 2
	}

	return y
}

func (b DirectoryBrowser) renderedEntryCount() int {
	entries := b.visibleEntries()

	if len(entries) == 0 {
		return 1
	}

	return min(
		len(entries),
		b.scroll+b.visibleRows(),
	) - b.scroll
}

func (b DirectoryBrowser) footerY() int {
	return b.entryStartY() + b.renderedEntryCount() + 3
}

func (b DirectoryBrowser) footerAction(x, y int) string {
	if y != b.footerY() {
		return ""
	}

	// [Select]  [Back]  [Cancel]
	switch {
	case x >= 0 && x < 10:
		return "select"

	case x >= 11 && x < 18:
		return "back"

	case x >= 19 && x < 28:
		return "cancel"

	default:
		return ""
	}
}

func (b DirectoryBrowser) indexFromMouseY(y int) int {
	index := y - b.entryStartY() + b.scroll

	if index < 0 {
		return -1
	}

	return index
}

func (b DirectoryBrowser) View() string {
	var lines []string

	width := b.width

	if width <= 0 {
		width = 80
	}

	contentWidth := max(1, width-2)

	lines = append(
		lines,
		directoryStyles.title.Render("Select download directory"),
		"",
		directoryStyles.path.Render(
			truncate("Path: "+b.currentPath, contentWidth),
		),
		"",
	)

	if b.err != nil {
		lines = append(
			lines,
			directoryStyles.errorStyle.Render(
				truncate("Error: "+b.err.Error(), contentWidth),
			),
		)

		lines = append(lines, "")
	}

	entries := b.visibleEntries()

	if len(entries) == 0 {
		lines = append(
			lines,
			directoryStyles.muted.Render("Directory is empty."),
		)
	} else {
		end := min(
			len(entries),
			b.scroll+b.visibleRows(),
		)

		for index := b.scroll; index < end; index++ {
			entry := entries[index]

			name := entry.Name()
			prefix := "[FILE]"

			if name == ".." {
				prefix = "  "
				name = "../"
			} else if entry.IsDir() {
				prefix = "[DIR] "
				name += "/"
			}

			line := prefix + " " + name
			line = truncate(line, contentWidth)

			switch {
			case index == b.selected:
				lines = append(
					lines,
					directoryStyles.selected.Render(line),
				)

			case entry.Name() == "..":
				lines = append(
					lines,
					directoryStyles.parent.Render(line),
				)

			case entry.IsDir():
				lines = append(
					lines,
					directoryStyles.directory.Render(line),
				)

			default:
				lines = append(
					lines,
					directoryStyles.file.Render(line),
				)
			}
		}
	}

	lines = append(lines, "")

	lines = append(
		lines,
		directoryStyles.muted.Render(
			truncate(
				fmt.Sprintf("Selected directory: %s", b.currentPath),
				contentWidth,
			),
		),
		"",
	)

	// Mouse-clickable footer buttons.
	buttons := "[Select]  [Back]  [Cancel]"

	lines = append(
		lines,
		directoryStyles.button.Render(
			truncate(buttons, contentWidth),
		),
	)

	lines = append(
		lines,
		directoryStyles.footer.Render(
			truncate(
				"↑/↓ select  Enter open  Backspace parent  . hidden  S select  Esc cancel",
				contentWidth,
			),
		),
	)

	return strings.Join(lines, "\n")
}

type parentEntry struct{}

func (parentEntry) Name() string {
	return ".."
}

func (parentEntry) IsDir() bool {
	return true
}

func (parentEntry) Type() os.FileMode {
	return os.ModeDir
}

func (parentEntry) Info() (os.FileInfo, error) {
	return nil, fmt.Errorf("parent entry has no file information")
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}

	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(
				home,
				strings.TrimPrefix(path, "~/"),
			)
		}
	}

	return path
}
