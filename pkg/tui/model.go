package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type jobsLoadedMsg struct {
	jobs []models.UIJob
	err  error
}

type refreshMsg time.Time

type actionResultMsg struct {
	action string
	err    error
}

type downloadCreatedMsg struct {
	jobID string
	err   error
}

type keyMap struct {
	up, down, pageUp, pageDown     key.Binding
	refresh, pause, resume, delete key.Binding
	newDownload                    key.Binding
	confirm, cancel, quit, help    key.Binding
	openDirectory                  key.Binding
}

var keys = keyMap{
	up:            key.NewBinding(key.WithKeys("up", "k")),
	down:          key.NewBinding(key.WithKeys("down", "j")),
	pageUp:        key.NewBinding(key.WithKeys("pgup", "ctrl+u")),
	pageDown:      key.NewBinding(key.WithKeys("pgdown", "ctrl+d")),
	refresh:       key.NewBinding(key.WithKeys("r")),
	pause:         key.NewBinding(key.WithKeys("p")),
	resume:        key.NewBinding(key.WithKeys("e")),
	delete:        key.NewBinding(key.WithKeys("d")),
	newDownload:   key.NewBinding(key.WithKeys("n")),
	confirm:       key.NewBinding(key.WithKeys("y", "enter")),
	cancel:        key.NewBinding(key.WithKeys("esc")),
	quit:          key.NewBinding(key.WithKeys("q", "ctrl+c")),
	help:          key.NewBinding(key.WithKeys("?")),
	openDirectory: key.NewBinding(key.WithKeys("b")),
}

type Model struct {
	client *DaemonClient

	jobs []models.UIJob

	selected, scroll, width, height int

	loading, showHelp, confirming, working bool

	err         error
	lastUpdated time.Time

	// Phase 4 directory browser state.
	directoryBrowser *DirectoryBrowser
	showDirectory    bool

	// Directory selected by the user.
	selectedDirectory string

	// Phase 5 new download form state.
	downloadForm    *DownloadForm
	showNewDownload bool
	pendingJobID    string
}

func NewModel(client *DaemonClient) Model {
	return Model{
		client:  client,
		loading: true,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		fetchJobs(m.client),
		scheduleRefresh(),
	)
}

func fetchJobs(client *DaemonClient) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return jobsLoadedMsg{
				err: fmt.Errorf("daemon client is nil"),
			}
		}

		jobs, err := client.GetJobs()

		return jobsLoadedMsg{
			jobs: jobs,
			err:  err,
		}
	}
}

func scheduleRefresh() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return refreshMsg(t)
	})
}

func runAction(client *DaemonClient, jobID, action string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return actionResultMsg{
				action: action,
				err:    fmt.Errorf("daemon client is nil"),
			}
		}

		var err error

		switch action {
		case "pause":
			err = client.PauseJob(jobID)

		case "resume":
			err = client.ResumeJob(jobID)

		case "delete":
			err = client.DeleteJob(jobID)

		default:
			err = fmt.Errorf("unknown action: %s", action)
		}

		return actionResultMsg{
			action: action,
			err:    err,
		}
	}
}

// createDownload sends the Phase 5 download request to the daemon.
func createDownload(client *DaemonClient, request CreateDownloadRequest) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return downloadCreatedMsg{
				err: fmt.Errorf("daemon client is nil"),
			}
		}

		jobID, err := client.CreateDownload(request)

		return downloadCreatedMsg{
			jobID: jobID,
			err:   err,
		}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {

	// Automatic refresh must be handled before the directory browser
	// or download form so the dashboard continues updating in the
	// background.
	switch msg := msg.(type) {

	case refreshMsg:
		return m, tea.Batch(
			fetchJobs(m.client),
			scheduleRefresh(),
		)

	case jobsLoadedMsg:
		m.loading = false
		m.err = msg.err

		if msg.err == nil {
			oldSelectedID := ""

			if m.hasSelection() {
				oldSelectedID = m.jobs[m.selected].ID
			}

			m.jobs = msg.jobs
			m.lastUpdated = time.Now()

			// Try to keep the same job selected after refresh.
			if oldSelectedID != "" {
				for i, job := range m.jobs {
					if job.ID == oldSelectedID {
						m.selected = i
						break
					}
				}
			}

			m.clampSelection()
			m.clampScroll()
		}

		// Keep interactive screens open while the dashboard refreshes.
		if m.showDirectory || m.showNewDownload {
			return m, nil
		}
	}

	// Directory browser is active.
	if m.showDirectory {
		return m.updateDirectoryBrowser(msg)
	}

	// New download form is active.
	if m.showNewDownload {
		return m.updateDownloadForm(msg)
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampScroll()

	case tea.KeyMsg:

		if m.confirming {
			if key.Matches(msg, keys.confirm) && m.hasSelection() {
				m.confirming = false
				m.working = true

				return m, runAction(
					m.client,
					m.jobs[m.selected].ID,
					"delete",
				)
			}

			if key.Matches(msg, keys.cancel) {
				m.confirming = false
			}

			return m, nil
		}

		switch {

		case key.Matches(msg, keys.quit):
			return m, tea.Quit

		case key.Matches(msg, keys.newDownload):
			return m, m.startDownloadForm()

		case key.Matches(msg, keys.refresh):
			m.loading = true
			m.err = nil

			return m, fetchJobs(m.client)

		case key.Matches(msg, keys.help):
			m.showHelp = !m.showHelp

		case key.Matches(msg, keys.openDirectory):
			return m, m.startDirectoryBrowser()

		case key.Matches(msg, keys.up):
			m.moveSelection(-1)

		case key.Matches(msg, keys.down):
			m.moveSelection(1)

		case key.Matches(msg, keys.pageUp):
			m.moveSelection(-m.visibleRows())

		case key.Matches(msg, keys.pageDown):
			m.moveSelection(m.visibleRows())

		case key.Matches(msg, keys.pause):
			return m, m.actionCommand("pause")

		case key.Matches(msg, keys.resume):
			return m, m.actionCommand("resume")

		case key.Matches(msg, keys.delete):
			m.confirming = m.hasSelection()
		}

	case tea.MouseMsg:

		if msg.Button == tea.MouseButtonWheelUp {
			m.moveSelection(-1)
			return m, nil
		}

		if msg.Button == tea.MouseButtonWheelDown {
			m.moveSelection(1)
			return m, nil
		}

		if msg.Button == tea.MouseButtonLeft &&
			msg.Action == tea.MouseActionPress &&
			msg.Y >= m.tableTop() {

			if action := m.footerAction(msg.X, msg.Y); action != "" {

				if action == "delete" {
					m.confirming = m.hasSelection()
					return m, nil
				}

				return m, m.actionCommand(action)
			}

			index := m.scroll + msg.Y - m.tableTop()

			if index >= 0 &&
				index < len(m.jobs) &&
				index < m.scroll+m.visibleRows() {

				m.selected = index
				m.clampScroll()
			}
		}

	case actionResultMsg:
		m.working = false
		m.err = msg.err

		if msg.err == nil {
			return m, fetchJobs(m.client)
		}
	}

	return m, nil
}

// startDownloadForm opens the Phase 5 new download form.
func (m *Model) startDownloadForm() tea.Cmd {
	directory := m.selectedDirectory

	if directory == "" {
		directory = defaultDownloadDirectory()
	}

	form := NewDownloadForm(directory)

	m.downloadForm = &form
	m.showNewDownload = true
	m.err = nil
	m.working = false

	return nil
}

// updateDownloadForm handles Phase 5 form messages and input.
func (m *Model) updateDownloadForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.downloadForm == nil {
		m.showNewDownload = false
		return m, nil
	}

	switch msg := msg.(type) {

	case openDownloadDirectoryMsg:
		return m, m.startDownloadDirectoryBrowser()

	case downloadCancelledMsg:
		m.showNewDownload = false
		m.downloadForm = nil
		m.pendingJobID = ""
		m.err = nil

		return m, nil

	case submitDownloadMsg:
		m.working = true
		m.err = nil

		return m, createDownload(m.client, msg.request)

	case downloadCreatedMsg:
		m.working = false

		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}

		m.pendingJobID = msg.jobID
		m.showNewDownload = false
		m.downloadForm = nil
		m.err = nil

		return m, fetchJobs(m.client)

	case directoryBrowserSelectedMsg:
		m.selectedDirectory = filepath.Clean(msg.path)

		if m.downloadForm != nil {
			m.downloadForm.SetDirectory(m.selectedDirectory)

			// Return to the filename stage after selecting
			// a directory.
			m.downloadForm.stage = downloadStageFilename
			m.downloadForm.focusFilename()
		}

		m.showDirectory = false
		m.directoryBrowser = nil

		return m, nil

	case directoryBrowserCancelledMsg:
		m.showDirectory = false
		m.directoryBrowser = nil

		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		updatedForm, command := m.downloadForm.Update(msg)
		m.downloadForm = &updatedForm

		return m, command
	}

	updatedForm, command := m.downloadForm.Update(msg)
	m.downloadForm = &updatedForm

	return m, command
}

// startDownloadDirectoryBrowser opens the filesystem browser
// from the root so drives/mounts such as /media/raunak/DATA1
// can be selected without hardcoding them.
func (m *Model) startDownloadDirectoryBrowser() tea.Cmd {
	startPath := string(filepath.Separator)

	browser := NewDirectoryBrowser(startPath)

	browser.width = m.width
	browser.height = m.height

	m.directoryBrowser = &browser
	m.showDirectory = true

	return browser.Init()
}

func (m Model) updateDirectoryBrowser(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.directoryBrowser == nil {
		m.showDirectory = false
		return m, nil
	}

	switch msg := msg.(type) {

	case tea.KeyMsg:
		updatedBrowser, command := m.directoryBrowser.Update(msg)
		m.directoryBrowser = &updatedBrowser

		return m, command

	case tea.MouseMsg:
		updatedBrowser, command := m.directoryBrowser.Update(msg)
		m.directoryBrowser = &updatedBrowser

		return m, command

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		updatedBrowser, command := m.directoryBrowser.Update(msg)
		m.directoryBrowser = &updatedBrowser

		return m, command

	case directoryBrowserLoadedMsg:
		updatedBrowser, command := m.directoryBrowser.Update(msg)
		m.directoryBrowser = &updatedBrowser

		return m, command

	case directoryBrowserSelectedMsg:
		m.selectedDirectory = filepath.Clean(msg.path)

		m.showDirectory = false
		m.directoryBrowser = nil
		m.err = nil

		// If the directory browser was opened from the Phase 5
		// download form, return to the filename stage and update
		// the form with the selected directory.
		if m.downloadForm != nil && m.showNewDownload {
			m.downloadForm.SetDirectory(m.selectedDirectory)
			m.downloadForm.stage = downloadStageFilename

			return m, m.downloadForm.focusFilename()
		}

		return m, nil

	case directoryBrowserCancelledMsg:
		m.showDirectory = false
		m.directoryBrowser = nil

		// Return to the download form if the browser was opened
		// from the Phase 5 form.
		if m.downloadForm != nil && m.showNewDownload {
			return m, m.downloadForm.focusFilename()
		}

		return m, nil
	}

	updatedBrowser, command := m.directoryBrowser.Update(msg)
	m.directoryBrowser = &updatedBrowser

	return m, command
}

func (m *Model) startDirectoryBrowser() tea.Cmd {
	startPath := string(filepath.Separator)

	browser := NewDirectoryBrowser(startPath)

	browser.width = m.width
	browser.height = m.height

	m.directoryBrowser = &browser
	m.showDirectory = true
	m.err = nil

	return browser.Init()
}

func (m Model) actionCommand(action string) tea.Cmd {
	if !m.hasSelection() || m.working {
		return nil
	}

	m.working = true

	return runAction(
		m.client,
		m.jobs[m.selected].ID,
		action,
	)
}

func (m *Model) moveSelection(delta int) {
	if len(m.jobs) == 0 {
		return
	}

	m.selected += delta

	m.clampSelection()
	m.clampScroll()
}

func (m *Model) clampSelection() {
	if m.selected < 0 {
		m.selected = 0
	}

	if m.selected >= len(m.jobs) {
		m.selected = max(0, len(m.jobs)-1)
	}
}

func (m *Model) clampScroll() {
	maxScroll := max(
		0,
		len(m.jobs)-m.visibleRows(),
	)

	if m.scroll > maxScroll {
		m.scroll = maxScroll
	}

	if m.selected < m.scroll {
		m.scroll = m.selected
	}

	if m.selected >= m.scroll+m.visibleRows() {
		m.scroll = m.selected - m.visibleRows() + 1
	}
}

func (m Model) hasSelection() bool {
	return m.selected >= 0 && m.selected < len(m.jobs)
}

func (m Model) visibleRows() int {
	return max(1, m.height-7)
}

func (m Model) tableTop() int {
	return 3
}

func (m Model) footerAction(x, y int) string {
	renderedRows := min(
		len(m.jobs)-m.scroll,
		m.visibleRows(),
	)

	footerY := m.tableTop() + renderedRows + 1

	if y != footerY || m.showHelp || m.working {
		return ""
	}

	switch {
	case x >= 0 && x < 12:
		return "pause"

	case x >= 12 && x < 25:
		return "resume"

	case x >= 25 && x < 36:
		return "delete"

	default:
		return ""
	}
}

func (m Model) View() string {
	// Render the directory browser first.
	if m.showDirectory && m.directoryBrowser != nil {
		return m.directoryBrowser.View()
	}

	// Render the Phase 5 new download form.
	if m.showNewDownload && m.downloadForm != nil {
		return m.downloadForm.View()
	}

	header := lipgloss.JoinHorizontal(
		lipgloss.Center,
		titleStyle.Render("HYDRA DOWNLOADER"),
		"  ",
		summaryStyle.Render(
			fmt.Sprintf("%d jobs", len(m.jobs)),
		),
	)

	if m.showHelp {
		return lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			"",
			helpView(),
		)
	}

	body := ""

	switch {

	case m.loading && len(m.jobs) == 0:
		body = mutedStyle.Render(
			"Connecting to Hydra daemon...",
		)

	case m.err != nil && len(m.jobs) == 0:
		body = errorStyle.Render(
			"Daemon error: " + m.err.Error(),
		)

	case len(m.jobs) == 0:
		body = mutedStyle.Render(
			"No downloads yet.",
		)

	default:
		body = m.jobsView()
	}

	footer := "↑/↓ select  [N] new  [P] pause  [E] resume  [D] delete  [B] browse  [R] refresh  [?] help  [Q] quit"

	if m.confirming {
		footer = "Delete selected job?  [Y/Enter] confirm  [Esc] cancel"
	} else if m.working {
		footer = "Working..."
	} else if !m.lastUpdated.IsZero() {
		footer = fmt.Sprintf(
			"Updated %s  •  %s",
			m.lastUpdated.Format("15:04:05"),
			footer,
		)
	}

	if m.selectedDirectory != "" {
		footer = fmt.Sprintf(
			"Selected directory: %s  •  %s",
			m.selectedDirectory,
			footer,
		)
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		body,
		"",
		footerStyle.Render(footer),
	)
}

func (m Model) jobsView() string {
	nameWidth := 28

	if m.width > 0 && m.width < 100 {
		nameWidth = max(16, m.width-65)
	}

	lines := []string{
		headerStyle.Render(
			fmt.Sprintf(
				"%-*s %10s %14s %10s  %s",
				nameWidth,
				"NAME",
				"PROGRESS",
				"SPEED",
				"ETA",
				"STATUS",
			),
		),
	}

	end := min(
		len(m.jobs),
		m.scroll+m.visibleRows(),
	)

	for index := m.scroll; index < end; index++ {
		job := m.jobs[index]

		line := fmt.Sprintf(
			"%-*s %9.1f%% %14s %10s  %s",
			nameWidth,
			truncate(job.FileName, nameWidth),
			job.Progress,
			job.Speed,
			job.ETA,
			job.Status,
		)

		if index == m.selected {
			lines = append(
				lines,
				selectedStyle.Render(line),
			)
		} else {
			lines = append(lines, line)
		}
	}

	return strings.Join(lines, "\n")
}

func helpView() string {
	return strings.Join(
		[]string{
			headerStyle.Render("KEYBOARD AND MOUSE CONTROLS"),
			"",
			"↑/k, ↓/j       Select a job",
			"PageUp/PageDown Scroll",
			"Mouse wheel    Scroll and select",
			"Mouse click    Select a row",
			"n               Create a new download",
			"p               Pause selected job",
			"e               Resume selected job",
			"d               Delete selected job",
			"b               Open directory browser",
			"r               Refresh jobs",
			"?               Toggle help",
			"q/Ctrl+C        Quit",
		},
		"\n",
	)
}

func truncate(value string, width int) string {
	if width < 4 || len(value) <= width {
		return value
	}

	return value[:width-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}

	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}

func defaultDownloadDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}

	return filepath.Join(home, "Downloads")
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7C5CFC"))

	summaryStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B8FA3"))

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#8B8FA3"))

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#3A315F"))

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B8FA3"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F15B5B"))

	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8B8FA3"))
)
