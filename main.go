package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Messages
type audioExtractedMsg struct {
	audioFile string
}

type transcriptionDoneMsg struct {
	vttContent      string
	transcriptItems []TranscriptItem
}

type errorMsg struct {
	err error
}

type TranscriptItem struct {
	StartTime string
	EndTime   string
	Text      string
}

// Model represents the application state
type model struct {
	width           int
	height          int
	spinner         spinner.Model
	loading         bool
	loadingMsg      string
	list            list.Model
	quitting        bool
	inputFile       string
	errorMsg        string
	transcriptItems []TranscriptItem
}

// List item for transcripts
type item struct {
	title     string
	timestamp string
	selected  bool
}

func (i item) FilterValue() string { return i.title }

// Item delegate for rendering list items
type itemDelegate struct{}

func (d itemDelegate) Height() int                             { return 2 }
func (d itemDelegate) Spacing() int                            { return 0 }
func (d itemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(item)
	if !ok {
		return
	}

	timestampStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).PaddingLeft(2)
	itemStyle := lipgloss.NewStyle().PaddingLeft(2)
	selectedItemStyle := lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Color("14"))

	checkbox := "☐"
	if i.selected {
		checkbox = "◼"
	}

	timestampLine := timestampStyle.Render(i.timestamp)
	str := fmt.Sprintf("%s %s", checkbox, i.title)

	fn := itemStyle.Render
	if index == m.Index() {
		fn = func(s ...string) string {
			return selectedItemStyle.Render("> " + strings.Join(s, " "))
		}
	}

	fmt.Fprintf(w, "%s\n%s", timestampLine, fn(str))
}

// Init initializes the model and returns the initial command
func (m model) Init() tea.Cmd {
	if m.loading {
		// Start the spinner and begin audio extraction
		return tea.Batch(
			m.spinner.Tick,
			extractAudioCmd(m.inputFile),
		)
	}
	// If not loading, just return nil (no commands to run)
	return nil
}

// extractAudioCmd extracts audio from video file
func extractAudioCmd(inputFile string) tea.Cmd {
	return func() tea.Msg {
		audioFile, err := extractAudio(inputFile)
		if err != nil {
			return errorMsg{err: err}
		}
		return audioExtractedMsg{audioFile: audioFile}
	}
}

// transcribeAudioCmd sends audio to OpenAI Whisper API
func transcribeAudioCmd(audioFile string) tea.Cmd {
	return func() tea.Msg {
		vttContent, err := transcribeWithOpenAI(audioFile)
		if err != nil {
			return errorMsg{err: err}
		}

		transcriptItems, err := parseVTT(vttContent)
		if err != nil {
			return errorMsg{err: err}
		}

		// Save VTT file
		basename := strings.TrimSuffix(filepath.Base(audioFile), filepath.Ext(audioFile))
		vttFile := basename + ".vtt"
		if err := os.WriteFile(vttFile, []byte(vttContent), 0644); err != nil {
			return errorMsg{err: err}
		}

		// Clean up audio file
		os.Remove(audioFile)

		return transcriptionDoneMsg{vttContent: vttContent, transcriptItems: transcriptItems}
	}
}

// extractAudio extracts audio from video file using ffmpeg
func extractAudio(inputFile string) (string, error) {
	basename := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))
	audioFile := basename + ".mp3"

	cmd := exec.Command("ffmpeg", "-y", "-i", inputFile, audioFile)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract audio: %w", err)
	}

	return audioFile, nil
}

// transcribeWithOpenAI sends audio to OpenAI Whisper API
func transcribeWithOpenAI(audioFile string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY environment variable is not set")
	}

	file, err := os.Open(audioFile)
	if err != nil {
		return "", fmt.Errorf("failed to open audio file: %w", err)
	}
	defer file.Close()

	var b bytes.Buffer
	writer := multipart.NewWriter(&b)

	part, err := writer.CreateFormFile("file", filepath.Base(audioFile))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}

	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file: %w", err)
	}

	writer.WriteField("model", "whisper-1")
	writer.WriteField("response_format", "vtt")

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close writer: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", &b)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	return string(body), nil
}

// parseVTT parses VTT content into transcript items
func parseVTT(vttContent string) ([]TranscriptItem, error) {
	lines := strings.Split(vttContent, "\n")
	var transcriptItems []TranscriptItem

	timeStampRegex := regexp.MustCompile(`^(\d{2}:\d{2}:\d{2}\.\d{3}) --> (\d{2}:\d{2}:\d{2}\.\d{3})`)
	var currentStartTime, currentEndTime string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if matches := timeStampRegex.FindStringSubmatch(line); matches != nil {
			currentStartTime = matches[1]
			currentEndTime = matches[2]
			continue
		}

		if strings.HasPrefix(line, "WEBVTT") || line == "" {
			continue
		}

		if line != "" && currentStartTime != "" {
			transcriptItems = append(transcriptItems, TranscriptItem{
				StartTime: currentStartTime,
				EndTime:   currentEndTime,
				Text:      line,
			})
			currentStartTime = ""
			currentEndTime = ""
		}
	}

	return transcriptItems, nil
}

// formatTimestamp converts HH:MM:SS.mmm to MM:SS.XX format
func formatTimestamp(timestamp string) string {
	parts := strings.Split(timestamp, ":")
	if len(parts) != 3 {
		return timestamp
	}

	secParts := strings.Split(parts[2], ".")
	if len(secParts) != 2 {
		return timestamp
	}

	milliseconds := secParts[1]
	if len(milliseconds) >= 3 {
		hundredths := milliseconds[:2]
		if len(milliseconds) > 2 && milliseconds[2] >= '5' {
			val := 0
			fmt.Sscanf(hundredths, "%d", &val)
			val++
			if val >= 100 {
				val = 99
			}
			hundredths = fmt.Sprintf("%02d", val)
		}
		return fmt.Sprintf("%s:%s.%s", parts[1], secParts[0], hundredths)
	} else {
		paddedMs := fmt.Sprintf("%-3s", milliseconds)
		hundredths := paddedMs[:2]
		return fmt.Sprintf("%s:%s.%s", parts[1], secParts[0], hundredths)
	}
}

// previewVideo plays video segment using mpv
func previewVideo(inputFile, startTime, endTime string) {
	cmd := exec.Command("mpv", "--start="+startTime, "--end="+endTime, inputFile)
	cmd.Run()
}

// getEndTime calculates end time for video preview
func getEndTime(items []list.Item, currentIndex int) string {
	if currentIndex+1 < len(items) {
		if nextItem, ok := items[currentIndex+1].(item); ok {
			return strings.Split(nextItem.timestamp, " - ")[0]
		}
	}
	if currentItem, ok := items[currentIndex].(item); ok {
		startTime := strings.Split(currentItem.timestamp, " - ")[0]
		return addSecondsToTimestamp(startTime, 10)
	}
	return "00:00:10.000"
}

// addSecondsToTimestamp adds seconds to a timestamp
func addSecondsToTimestamp(timestamp string, seconds int) string {
	parts := strings.Split(timestamp, ":")
	if len(parts) != 3 {
		return timestamp
	}

	secParts := strings.Split(parts[2], ".")
	if len(secParts) != 2 {
		return timestamp
	}

	currentSec := 0
	fmt.Sscanf(secParts[0], "%d", &currentSec)
	newSec := currentSec + seconds

	if newSec >= 60 {
		newSec = 59
	}

	return fmt.Sprintf("%s:%s:%02d.%s", parts[0], parts[1], newSec, secParts[1])
}

// Update handles messages and updates the model
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "enter", " ":
			if !m.loading && len(m.list.Items()) > 0 {
				selectedIndex := m.list.Index()
				if selectedIndex >= 0 && selectedIndex < len(m.list.Items()) {
					items := m.list.Items()
					if i, ok := items[selectedIndex].(item); ok {
						i.selected = !i.selected
						items[selectedIndex] = i
						m.list.SetItems(items)
					}
				}
			}
			return m, nil

		case "p":
			if !m.loading && len(m.list.Items()) > 0 {
				selectedIndex := m.list.Index()
				if selectedIndex >= 0 && selectedIndex < len(m.list.Items()) {
					items := m.list.Items()
					if i, ok := items[selectedIndex].(item); ok {
						startTime := strings.Split(i.timestamp, " - ")[0]
						go previewVideo(m.inputFile, startTime, getEndTime(items, selectedIndex))
					}
				}
			}
			return m, nil
		}

		// If not loading, pass to list
		if !m.loading {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.loading {
			m.list.SetWidth(msg.Width)
			m.list.SetHeight(msg.Height - 4) // Leave space for title and footer
		}
		return m, nil

	case audioExtractedMsg:
		m.loadingMsg = "Transcribing with OpenAI Whisper..."
		return m, transcribeAudioCmd(msg.audioFile)

	case transcriptionDoneMsg:
		m.loading = false
		m.transcriptItems = msg.transcriptItems

		// Convert transcript items to list items
		items := make([]list.Item, len(msg.transcriptItems))
		for i, transcriptItem := range msg.transcriptItems {
			items[i] = item{
				title:     transcriptItem.Text,
				timestamp: formatTimestamp(transcriptItem.StartTime) + " - " + formatTimestamp(transcriptItem.EndTime),
				selected:  false,
			}
		}

		// Create and configure the list
		l := list.New(items, itemDelegate{}, m.width, m.height-4)
		l.SetShowTitle(false)
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(true)
		l.SetShowHelp(true)
		l.SetShowPagination(false)
		
		// Add custom key bindings for help
		l.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{
				key.NewBinding(
					key.WithKeys("p"),
					key.WithHelp("p", "preview"),
				),
			}
		}
		
		m.list = l

		return m, nil

	case errorMsg:
		m.loading = false
		m.errorMsg = msg.err.Error()
		return m, nil

	case spinner.TickMsg:
		if m.loading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	return m, nil
}

// View renders the UI
func (m model) View() string {
	if m.quitting {
		return ""
	}

	// Create styles
	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("86")).
		Align(lipgloss.Center).
		Width(m.width)

	loadingStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Align(lipgloss.Center).
		Width(m.width)

	errorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("196")).
		Align(lipgloss.Center).
		Width(m.width)

	// Title
	title := titleStyle.Render("tsplice")

	// Content area
	if m.errorMsg != "" {
		// Show error
		errorText := fmt.Sprintf("Error: %s", m.errorMsg)
		return title + "\n\n" + errorStyle.Render(errorText) + "\n\nPress 'q' to quit"
	} else if m.loading {
		// Show spinner while loading
		loadingMsg := m.loadingMsg
		if loadingMsg == "" {
			loadingMsg = "Extracting audio with ffmpeg..."
		}
		loadingText := fmt.Sprintf("%s %s", m.spinner.View(), loadingMsg)
		return title + "\n\n" + loadingStyle.Render(loadingText)
	} else {
		// Show transcript list
		if len(m.transcriptItems) == 0 {
			return title + "\n\n" + "No transcript items found"
		}

		// Add header with total time info
		var header string
		if len(m.transcriptItems) > 0 {
			firstStart := formatTimestamp(m.transcriptItems[0].StartTime)
			lastEnd := formatTimestamp(m.transcriptItems[len(m.transcriptItems)-1].EndTime)
			header = fmt.Sprintf("  Start: %s | End: %s\n", firstStart, lastEnd)
		}

		return title + "\n" + header + m.list.View()
	}
}

func main() {
	// Validate command line arguments
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: tsplice <input-file>\n")
		os.Exit(1)
	}

	inputFile := os.Args[1]
	if _, err := os.Stat(inputFile); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: File '%s' does not exist\n", inputFile)
		os.Exit(1)
	}

	// Check if VTT file already exists
	basename := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))
	vttFile := basename + ".vtt"

	// Initialize spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	// Create initial model
	initialModel := model{
		spinner:    s,
		loading:    true,
		loadingMsg: "Extracting audio with ffmpeg...",
		inputFile:  inputFile,
	}

	// Check if transcript already exists
	if _, err := os.Stat(vttFile); err == nil {
		// Load existing transcript
		vttBytes, err := os.ReadFile(vttFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading existing VTT file: %v\n", err)
			os.Exit(1)
		}

		transcriptItems, err := parseVTT(string(vttBytes))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing VTT: %v\n", err)
			os.Exit(1)
		}

		// Convert to list items
		items := make([]list.Item, len(transcriptItems))
		for i, transcriptItem := range transcriptItems {
			items[i] = item{
				title:     transcriptItem.Text,
				timestamp: formatTimestamp(transcriptItem.StartTime) + " - " + formatTimestamp(transcriptItem.EndTime),
				selected:  false,
			}
		}

		// Create list
		l := list.New(items, itemDelegate{}, 80, 20)
		l.SetShowTitle(false)
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(true)
		l.SetShowHelp(true)
		l.SetShowPagination(false)
		
		// Add custom key bindings for help
		l.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{
				key.NewBinding(
					key.WithKeys("p"),
					key.WithHelp("p", "preview"),
				),
			}
		}

		initialModel.loading = false
		initialModel.list = l
		initialModel.transcriptItems = transcriptItems
	}

	// Create and run the program
	p := tea.NewProgram(
		initialModel,
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v", err)
	}
}
