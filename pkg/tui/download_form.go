package tui

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type downloadFormStage int

const (
	downloadStageURL downloadFormStage = iota
	downloadStageFilename
	downloadStageOptions
	downloadStageConfirm
)

type DownloadForm struct {
	stage     downloadFormStage
	urlInput  textinput.Model
	nameInput textinput.Model

	headersInput  textinput.Model
	checksumInput textinput.Model
	algoInput     textinput.Model
	speedInput    textinput.Model
	scheduleInput textinput.Model
	batchInput    textinput.Model

	directory string
	err       error
}

type openDownloadDirectoryMsg struct{}

type downloadCancelledMsg struct{}

type submitDownloadMsg struct {
	request CreateDownloadRequest
}

func newDownloadInput(placeholder string, width int) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.CharLimit = 4096
	input.Width = width
	return input
}

func NewDownloadForm(directory string) DownloadForm {
	urlInput := newDownloadInput("https://example.com/file.zip", 80)
	urlInput.Focus()

	nameInput := newDownloadInput("filename.zip", 60)

	headersInput := newDownloadInput("Authorization: Bearer token, Referer: https://example.com", 80)
	checksumInput := newDownloadInput("expected checksum (optional)", 80)
	algoInput := newDownloadInput("sha256", 20)
	speedInput := newDownloadInput("e.g. 1MB/s, 500KB/s, 0 for unlimited", 40)
	scheduleInput := newDownloadInput("RFC3339, e.g. 2026-09-15T20:00:00+05:30", 60)
	batchInput := newDownloadInput("batch-id (optional)", 40)

	return DownloadForm{
		stage:         downloadStageURL,
		urlInput:      urlInput,
		nameInput:     nameInput,
		headersInput:  headersInput,
		checksumInput: checksumInput,
		algoInput:     algoInput,
		speedInput:    speedInput,
		scheduleInput: scheduleInput,
		batchInput:    batchInput,
		directory:     filepath.Clean(directory),
	}
}

func (f *DownloadForm) SetDirectory(directory string) {
	f.directory = filepath.Clean(directory)
}

func (f DownloadForm) Directory() string {
	return f.directory
}

func (f DownloadForm) Filename() string {
	return strings.TrimSpace(f.nameInput.Value())
}

func (f DownloadForm) URL() string {
	return strings.TrimSpace(f.urlInput.Value())
}

func (f *DownloadForm) focusURL() tea.Cmd {
	f.nameInput.Blur()
	return f.urlInput.Focus()
}

func (f *DownloadForm) focusFilename() tea.Cmd {
	f.urlInput.Blur()
	return f.nameInput.Focus()
}

func (f *DownloadForm) focusOptions() tea.Cmd {
	f.nameInput.Blur()
	return f.headersInput.Focus()
}

func (f *DownloadForm) acceptURL() tea.Cmd {
	value := strings.TrimSpace(f.urlInput.Value())

	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		f.err = fmt.Errorf("invalid URL")
		return nil
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		f.err = fmt.Errorf("unsupported URL scheme: %s", parsed.Scheme)
		return nil
	}

	f.err = nil

	filename := filepath.Base(parsed.Path)

	if filename == "." || filename == "/" || filename == "" {
		filename = "download.bin"
	}

	if decoded, err := url.PathUnescape(filename); err == nil {
		filename = decoded
	}

	if filename == "." || filename == ".." || filename == "" {
		filename = "download.bin"
	}

	f.nameInput.SetValue(filename)

	return func() tea.Msg {
		return openDownloadDirectoryMsg{}
	}
}

func (f *DownloadForm) acceptFilename() bool {
	filename := strings.TrimSpace(f.nameInput.Value())

	if filename == "" {
		f.err = fmt.Errorf("filename cannot be empty")
		return false
	}

	if filename == "." || filename == ".." {
		f.err = fmt.Errorf("invalid filename")
		return false
	}

	if strings.ContainsRune(filename, '\x00') {
		f.err = fmt.Errorf("filename contains an invalid character")
		return false
	}

	if filepath.Base(filename) != filename {
		f.err = fmt.Errorf("filename must not contain directory separators")
		return false
	}

	f.err = nil
	return true
}

func (f DownloadForm) SavePath() string {
	return filepath.Join(f.directory, f.Filename())
}

func (f DownloadForm) Headers() map[string]string {
	value := strings.TrimSpace(f.headersInput.Value())

	if value == "" {
		return nil
	}

	headers := make(map[string]string)

	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)

		if part == "" {
			continue
		}

		key, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		if key == "" || value == "" {
			continue
		}

		headers[key] = value
	}

	if len(headers) == 0 {
		return nil
	}

	return headers
}

func (f DownloadForm) MaxSpeedBytes() int64 {
	value := strings.TrimSpace(strings.ToUpper(f.speedInput.Value()))

	if value == "" || value == "0" {
		return 0
	}

	multiplier := int64(1)

	switch {
	case strings.HasSuffix(value, "KB/S"):
		multiplier = 1024
		value = strings.TrimSpace(strings.TrimSuffix(value, "KB/S"))

	case strings.HasSuffix(value, "MB/S"):
		multiplier = 1024 * 1024
		value = strings.TrimSpace(strings.TrimSuffix(value, "MB/S"))

	case strings.HasSuffix(value, "GB/S"):
		multiplier = 1024 * 1024 * 1024
		value = strings.TrimSpace(strings.TrimSuffix(value, "GB/S"))

	case strings.HasSuffix(value, "K"):
		multiplier = 1024
		value = strings.TrimSpace(strings.TrimSuffix(value, "K"))

	case strings.HasSuffix(value, "M"):
		multiplier = 1024 * 1024
		value = strings.TrimSpace(strings.TrimSuffix(value, "M"))

	case strings.HasSuffix(value, "G"):
		multiplier = 1024 * 1024 * 1024
		value = strings.TrimSpace(strings.TrimSuffix(value, "G"))
	}

	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return 0
	}

	return int64(number * float64(multiplier))
}

func (f DownloadForm) ExpectedChecksum() string {
	return strings.TrimSpace(f.checksumInput.Value())
}

func (f DownloadForm) ChecksumAlgo() string {
	return strings.ToLower(strings.TrimSpace(f.algoInput.Value()))
}

func (f DownloadForm) ScheduledAt() string {
	return strings.TrimSpace(f.scheduleInput.Value())
}

func (f DownloadForm) BatchID() string {
	return strings.TrimSpace(f.batchInput.Value())
}

func (f DownloadForm) validateOptions() bool {
	f.err = nil

	checksum := f.ExpectedChecksum()
	algo := f.ChecksumAlgo()

	if checksum != "" {
		switch algo {
		case "sha256", "md5", "crc32":
		default:
			f.err = fmt.Errorf(
				"unsupported checksum algorithm: %s (use sha256, md5, or crc32)",
				algo,
			)
			return false
		}
	}

	if schedule := f.ScheduledAt(); schedule != "" {
		if _, err := parseSchedule(schedule); err != nil {
			f.err = err
			return false
		}
	}

	// Empty speed limit and "0" both mean unlimited.
	if speed := strings.TrimSpace(f.speedInput.Value()); speed != "" && speed != "0" {
		if f.MaxSpeedBytes() <= 0 {
			f.err = fmt.Errorf("invalid speed limit: %s", speed)
			return false
		}
	}

	return true
}

func parseSchedule(value string) (string, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		return "", nil
	}

	// Basic RFC3339 validation.
	// The daemon performs the authoritative timestamp parsing.
	if !isRFC3339(value) {
		return "", fmt.Errorf("invalid schedule: use RFC3339 format")
	}

	return value, nil
}

func isRFC3339(value string) bool {
	// Avoid accepting arbitrary strings as a scheduled timestamp.
	//
	// The daemon is responsible for parsing the timestamp into time.Time.
	// Here we only ensure that it resembles an RFC3339 timestamp.
	if len(value) < 20 {
		return false
	}

	return strings.Contains(value, "T") &&
		(strings.HasSuffix(value, "Z") ||
			strings.Contains(value[len(value)-6:], "+") ||
			strings.Contains(value[len(value)-6:], "-"))
}

func (f DownloadForm) Request() CreateDownloadRequest {
	return CreateDownloadRequest{
		URL:              f.URL(),
		SavePath:         f.SavePath(),
		Filename:         f.Filename(),
		Headers:          f.Headers(),
		MaxSpeedBytes:    f.MaxSpeedBytes(),
		ExpectedChecksum: f.ExpectedChecksum(),
		ChecksumAlgo:     f.ChecksumAlgo(),
		ScheduledAt:      f.ScheduledAt(),
		BatchID:          f.BatchID(),
	}
}

func (f DownloadForm) activeOptionInput() *textinput.Model {
	switch f.optionIndex() {
	case 0:
		return &f.headersInput
	case 1:
		return &f.checksumInput
	case 2:
		return &f.algoInput
	case 3:
		return &f.speedInput
	case 4:
		return &f.scheduleInput
	case 5:
		return &f.batchInput
	default:
		return nil
	}
}

func (f DownloadForm) optionIndex() int {
	inputs := []*textinput.Model{
		&f.headersInput,
		&f.checksumInput,
		&f.algoInput,
		&f.speedInput,
		&f.scheduleInput,
		&f.batchInput,
	}

	for i, input := range inputs {
		if input.Focused() {
			return i
		}
	}

	return 0
}

func (f *DownloadForm) moveOption(delta int) tea.Cmd {
	inputs := []*textinput.Model{
		&f.headersInput,
		&f.checksumInput,
		&f.algoInput,
		&f.speedInput,
		&f.scheduleInput,
		&f.batchInput,
	}

	current := f.optionIndex()
	next := current + delta

	if next < 0 {
		next = len(inputs) - 1
	}

	if next >= len(inputs) {
		next = 0
	}

	for _, input := range inputs {
		input.Blur()
	}

	return inputs[next].Focus()
}

func (f *DownloadForm) acceptOptions() bool {
	if !f.validateOptions() {
		return false
	}

	f.err = nil
	f.stage = downloadStageConfirm

	return true
}

func (f DownloadForm) Update(msg tea.Msg) (DownloadForm, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.String() == "esc" {
			return f, func() tea.Msg {
				return downloadCancelledMsg{}
			}
		}

		switch f.stage {
		case downloadStageURL:
			if keyMsg.Type == tea.KeyEnter {
				cmd := f.acceptURL()

				if cmd != nil {
					return f, cmd
				}

				return f, nil
			}

		case downloadStageFilename:
			if keyMsg.Type == tea.KeyEnter {
				if !f.acceptFilename() {
					return f, nil
				}

				f.stage = downloadStageOptions
				f.err = nil

				return f, f.focusOptions()
			}

		case downloadStageOptions:
			switch keyMsg.Type {
			case tea.KeyTab, tea.KeyDown:
				return f, f.moveOption(1)

			case tea.KeyShiftTab, tea.KeyUp:
				return f, f.moveOption(-1)

			case tea.KeyEnter:
				if f.optionIndex() == 5 {
					if !f.acceptOptions() {
						return f, nil
					}

					return f, nil
				}

				return f, f.moveOption(1)
			}

		case downloadStageConfirm:
			switch keyMsg.String() {
			case "y", "enter":
				request := f.Request()

				return f, func() tea.Msg {
					return submitDownloadMsg{
						request: request,
					}
				}

			case "n":
				return f, func() tea.Msg {
					return downloadCancelledMsg{}
				}
			}
		}
	}

	switch f.stage {
	case downloadStageURL:
		var cmd tea.Cmd
		f.urlInput, cmd = f.urlInput.Update(msg)
		return f, cmd

	case downloadStageFilename:
		var cmd tea.Cmd
		f.nameInput, cmd = f.nameInput.Update(msg)
		return f, cmd

	case downloadStageOptions:
		var cmd tea.Cmd

		switch f.optionIndex() {
		case 0:
			f.headersInput, cmd = f.headersInput.Update(msg)

		case 1:
			f.checksumInput, cmd = f.checksumInput.Update(msg)

		case 2:
			f.algoInput, cmd = f.algoInput.Update(msg)

		case 3:
			f.speedInput, cmd = f.speedInput.Update(msg)

		case 4:
			f.scheduleInput, cmd = f.scheduleInput.Update(msg)

		case 5:
			f.batchInput, cmd = f.batchInput.Update(msg)
		}

		return f, cmd
	}

	return f, nil
}

func (f DownloadForm) View() string {
	header := titleStyle.Render("NEW DOWNLOAD")

	var body string

	switch f.stage {
	case downloadStageURL:
		body = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			"",
			headerStyle.Render("URL"),
			f.urlInput.View(),
			"",
			mutedStyle.Render("Enter to continue • Esc to cancel"),
		)

	case downloadStageFilename:
		body = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			"",
			headerStyle.Render("Download directory"),
			f.directory,
			"",
			headerStyle.Render("Filename"),
			f.nameInput.View(),
			"",
			mutedStyle.Render("Enter to continue • Esc to cancel"),
		)

	case downloadStageOptions:
		body = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			"",
			headerStyle.Render("Download options"),
			"",
			"Headers:",
			f.headersInput.View(),
			"",
			"Checksum:",
			f.checksumInput.View(),
			"",
			"Checksum algorithm:",
			f.algoInput.View(),
			"",
			"Speed limit:",
			f.speedInput.View(),
			"",
			"Schedule:",
			f.scheduleInput.View(),
			"",
			"Batch ID:",
			f.batchInput.View(),
			"",
			mutedStyle.Render("Tab/↓ next • Shift+Tab/↑ previous • Enter continue • Esc cancel"),
		)

	case downloadStageConfirm:
		headers := f.Headers()
		headersText := "none"

		if len(headers) > 0 {
			parts := make([]string, 0, len(headers))

			for key, value := range headers {
				parts = append(parts, key+": "+value)
			}

			headersText = strings.Join(parts, ", ")
		}

		speedText := "unlimited"

		if speed := f.MaxSpeedBytes(); speed > 0 {
			speedText = fmt.Sprintf("%d bytes/s", speed)
		}

		checksumText := "none"

		if checksum := f.ExpectedChecksum(); checksum != "" {
			checksumText = fmt.Sprintf(
				"%s (%s)",
				checksum,
				f.ChecksumAlgo(),
			)
		}

		scheduleText := "immediately"

		if schedule := f.ScheduledAt(); schedule != "" {
			scheduleText = schedule
		}

		batchText := "none"

		if batch := f.BatchID(); batch != "" {
			batchText = batch
		}

		body = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			"",
			headerStyle.Render("Confirm download"),
			"",
			fmt.Sprintf("URL:         %s", f.URL()),
			fmt.Sprintf("Directory:   %s", f.directory),
			fmt.Sprintf("Filename:    %s", f.Filename()),
			"",
			headerStyle.Render("Options"),
			fmt.Sprintf("Headers:     %s", headersText),
			fmt.Sprintf("Checksum:    %s", checksumText),
			fmt.Sprintf("Speed limit: %s", speedText),
			fmt.Sprintf("Schedule:    %s", scheduleText),
			fmt.Sprintf("Batch ID:     %s", batchText),
			"",
			headerStyle.Render("Final path"),
			f.SavePath(),
			"",
			mutedStyle.Render("[Y/Enter] confirm  [N/Esc] cancel"),
		)
	}

	if f.err != nil {
		body = lipgloss.JoinVertical(
			lipgloss.Left,
			body,
			"",
			errorStyle.Render("Error: "+f.err.Error()),
		)
	}

	return lipgloss.NewStyle().
		Padding(1, 2).
		Render(body)
}
