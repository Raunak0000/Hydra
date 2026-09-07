package tui

import (
	"fmt"
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

type keyMap struct {
	up      key.Binding
	down    key.Binding
	refresh key.Binding
	quit    key.Binding
	help    key.Binding
}

var keys = keyMap{
	up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	refresh: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "refresh"),
	),
	quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
}

type Model struct {
	client      *DaemonClient
	jobs        []models.UIJob
	selected    int
	width       int
	height      int
	loading     bool
	showHelp    bool
	err         error
	lastUpdated time.Time
}

func NewModel(client *DaemonClient) Model {
	return Model{client: client, loading: true}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(fetchJobs(m.client), scheduleRefresh())
}

func fetchJobs(client *DaemonClient) tea.Cmd {
	return func() tea.Msg {
		jobs, err := client.GetJobs()
		return jobsLoadedMsg{jobs: jobs, err: err}
	}
}

func scheduleRefresh() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return refreshMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.quit):
			return m, tea.Quit
		case key.Matches(msg, keys.refresh):
			m.loading = true
			m.err = nil
			return m, fetchJobs(m.client)
		case key.Matches(msg, keys.help):
			m.showHelp = !m.showHelp
		case key.Matches(msg, keys.up):
			if m.selected > 0 {
				m.selected--
			}
		case key.Matches(msg, keys.down):
			if m.selected < len(m.jobs)-1 {
				m.selected++
			}
		}

	case jobsLoadedMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.jobs = msg.jobs
			if m.selected >= len(m.jobs) {
				m.selected = max(0, len(m.jobs)-1)
			}
			m.lastUpdated = time.Now()
		}

	case refreshMsg:
		return m, tea.Batch(fetchJobs(m.client), scheduleRefresh())
	}

	return m, nil
}

func (m Model) View() string {
	title := titleStyle.Render("HYDRA DOWNLOADER")
	summary := summaryStyle.Render(fmt.Sprintf("%d jobs", len(m.jobs)))
	header := lipgloss.JoinHorizontal(lipgloss.Center, title, "  ", summary)

	if m.showHelp {
		return lipgloss.JoinVertical(lipgloss.Left, header, "", helpView())
	}

	body := ""
	switch {
	case m.loading && len(m.jobs) == 0:
		body = mutedStyle.Render("Connecting to Hydra daemon...")
	case m.err != nil && len(m.jobs) == 0:
		body = errorStyle.Render("Daemon error: " + m.err.Error())
	case len(m.jobs) == 0:
		body = mutedStyle.Render("No downloads yet.")
	default:
		body = m.jobsView()
	}

	footer := footerStyle.Render("↑/↓ select  r refresh  ? help  q quit")
	if !m.lastUpdated.IsZero() {
		footer = footerStyle.Render(fmt.Sprintf("Updated %s  •  ↑/↓ select  r refresh  ? help  q quit", m.lastUpdated.Format("15:04:05")))
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", footer)
}

func (m Model) jobsView() string {
	nameWidth := 28
	if m.width > 0 && m.width < 100 {
		nameWidth = max(16, m.width-65)
	}
	lines := []string{headerStyle.Render(fmt.Sprintf("%-*s %10s %14s %10s  %s", nameWidth, "NAME", "PROGRESS", "SPEED", "ETA", "STATUS"))}
	for index, job := range m.jobs {
		name := truncate(job.FileName, nameWidth)
		progress := fmt.Sprintf("%6.1f%%", job.Progress)
		line := fmt.Sprintf("%-*s %10s %14s %10s  %s", nameWidth, name, progress, job.Speed, job.ETA, job.Status)
		if index == m.selected {
			lines = append(lines, selectedStyle.Render(line))
		} else {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func helpView() string {
	return strings.Join([]string{
		headerStyle.Render("KEYBOARD CONTROLS"),
		"",
		"↑ / k       Select previous job",
		"↓ / j       Select next job",
		"r           Refresh jobs",
		"?           Toggle this help",
		"q / Ctrl+C   Quit",
	}, "\n")
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

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7C5CFC"))
	summaryStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B8FA3"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#8B8FA3"))
	selectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#3A315F"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B8FA3"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F15B5B"))
	footerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B8FA3"))
)
