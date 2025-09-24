package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

func (i item) FilterValue() string { return i.title }

func (d itemDelegate) Height() int                             { return 2 }
func (d itemDelegate) Spacing() int                            { return 0 }
func (d itemDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (d itemDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(item)
	if !ok {
		return
	}

	checkbox := "☐"
	if i.selected {
		checkbox = "◼"
	}

	timestampLine := TimestampStyle.Render(i.timestamp)
	str := fmt.Sprintf("%s %s", checkbox, i.title)

	fn := ItemStyle.Render
	if index == m.Index() {
		fn = func(s ...string) string {
			return SelectedItemStyle.Render("> " + strings.Join(s, " "))
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

func extractAudioCmd(inputFile string) tea.Cmd {
	return func() tea.Msg {
		audioFile, err := extractAudio(inputFile)
		if err != nil {
			return errorMsg{err: err}
		}
		return audioExtractedMsg{audioFile: audioFile}
	}
}

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

		basename := strings.TrimSuffix(filepath.Base(audioFile), filepath.Ext(audioFile))
		vttFile := basename + ".vtt"
		if err := os.WriteFile(vttFile, []byte(vttContent), 0644); err != nil {
			return errorMsg{err: err}
		}

		os.Remove(audioFile)

		return transcriptionDoneMsg{vttContent: vttContent, transcriptItems: transcriptItems}
	}
}

func extractAudio(inputFile string) (string, error) {
	basename := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))
	audioFile := basename + ".mp3"

	cmd := exec.Command("ffmpeg", "-y", "-i", inputFile, audioFile)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to extract audio: %w", err)
	}

	return audioFile, nil
}

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

func previewVideo(inputFile, startTime, endTime string) {
	cmd := exec.Command("mpv", "--start="+startTime, "--end="+endTime, inputFile)
	cmd.Run()
}

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

func compileVideoCmd(inputFile string, items []list.Item) tea.Cmd {
	return func() tea.Msg {
		outputFile, err := compileVideoSegments(inputFile, items)
		if err != nil {
			return errorMsg{err: err}
		}
		return videoCompilationDoneMsg{outputFile: outputFile}
	}
}

func compileVideoSegments(inputFile string, items []list.Item) (string, error) {
	// Collect selected segments
	var segments []struct {
		start, end float64
	}

	for _, listItem := range items {
		if i, ok := listItem.(item); ok && i.selected {
			timestamps := strings.Split(i.timestamp, " - ")
			if len(timestamps) == 2 {
				// Convert MM:SS.XX back to HH:MM:SS.mmm format for ffmpeg
				start, err := parseTimeToSeconds(timestamps[0])
				if err != nil {
					return "", fmt.Errorf("could not parse start time '%s': %w", timestamps[0], err)
				}

				end, err := parseTimeToSeconds(timestamps[1])
				if err != nil {
					return "", fmt.Errorf("could not parse end time '%s': %w", timestamps[1], err)
				}

				segments = append(segments, struct {
					start, end float64
				}{start: start, end: end})
			}
		}
	}

	if len(segments) == 0 {
		return "", fmt.Errorf("no segments selected")
	}

	// Generate output filename
	basename := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))
	outputFile := fmt.Sprintf("%s_compiled.mp4", basename)

	// Use the same directory as input file
	outputFile = filepath.Join(filepath.Dir(inputFile), outputFile)

	// Build ffmpeg filter_complex command for multiple segments
	var filterParts []string

	for _, segment := range segments {
		filterParts = append(filterParts, fmt.Sprintf("between(t,%.3f,%.3f)", segment.start, segment.end))
	}

	selectFilter := strings.Join(filterParts, "+")

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i",
		inputFile,
		"-vf",
		fmt.Sprintf("select='%s',setpts=N/FRAME_RATE/TB", selectFilter),
		"-af",
		fmt.Sprintf("aselect='%s',asetpts=N/SR/TB", selectFilter),
		outputFile,
	)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to compile video segments: %w", err)
	}

	return outputFile, nil
}

func parseTimeToSeconds(timeStr string) (float64, error) {
	var hours, minutes int
	var seconds float64

	_, err := fmt.Sscanf(timeStr, "%d:%d:%f", &hours, &minutes, &seconds)
	if err != nil {
		return 0, err
	}

	totalSeconds := float64(hours*3600) + float64(minutes*60) + seconds
	return totalSeconds, nil
}

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

		case "c":
			if !m.loading && len(m.list.Items()) > 0 {
				// Check if any items are selected
				items := m.list.Items()
				hasSelected := false
				for _, listItem := range items {
					if i, ok := listItem.(item); ok && i.selected {
						hasSelected = true
						break
					}
				}
				if hasSelected {
					m.loading = true
					m.loadingMsg = "Compiling video segments with ffmpeg..."
					return m, tea.Batch(
						m.spinner.Tick,
						compileVideoCmd(m.inputFile, items),
					)
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

	case audioExtractedMsg:
		m.statuses = append(m.statuses, "Audio extracted from ffmpeg")
		m.loadingMsg = "Transcribing with OpenAI Whisper..."
		return m, transcribeAudioCmd(msg.audioFile)

	case transcriptionDoneMsg:
		m.statuses = append(m.statuses, "Transcription finished and saved locally")
		m.loading = false
		m.transcriptItems = msg.transcriptItems

		// Convert transcript items to list items
		items := make([]list.Item, len(msg.transcriptItems))
		for i, transcriptItem := range msg.transcriptItems {
			items[i] = item{
				title:     transcriptItem.Text,
				timestamp: transcriptItem.StartTime + " - " + transcriptItem.EndTime,
				selected:  false,
			}
		}

		// Create and configure the list
		l := list.New(items, itemDelegate{}, 32, 12)
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
				key.NewBinding(
					key.WithKeys("c"),
					key.WithHelp("c", "compile"),
				),
			}
		}

		m.list = l

		return m, nil

	case videoCompilationDoneMsg:
		m.statuses = append(m.statuses, "Video compiled successfully!")
		m.loading = false
		m.errorMsg = fmt.Sprintf("✓ Video compiled successfully: %s", msg.outputFile)
		return m, nil

	case errorMsg:
		m.statuses = append(m.statuses, msg.err.Error())
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

func (m model) View() string {
	if m.quitting {
		return styleOutput(m.statuses)
	}

	// Content area
	if m.errorMsg != "" {
		return styleOutput(m.statuses) + "\nPress 'q' to quit"
	} else if m.loading {
		loadingText := fmt.Sprintf("%s %s", m.spinner.View(), m.loadingMsg)
		if len(m.statuses) > 0 {
			return styleOutput(m.statuses) + loadingText
		}
		return loadingText
	} else {
		// Show transcript list
		if len(m.transcriptItems) == 0 {
			return styleOutput(m.statuses) + "No transcript items found"
		}

		// Add header with total time info
		var header string
		if len(m.transcriptItems) > 0 {
			firstStart := m.transcriptItems[0].StartTime
			lastEnd := m.transcriptItems[len(m.transcriptItems)-1].EndTime
			header = fmt.Sprintf("  Start: %s | End: %s\n", firstStart, lastEnd)
		}

		return styleOutput(m.statuses) + header + m.list.View()
	}
}

func styleOutput(statuses []string) string {
	var styledStatuses []string
	for i, status := range statuses {
		bullet := "├"
		if i == len(statuses)-1 {
			bullet = "└"
		}
		styledStatuses = append(styledStatuses, BulletStyle.Render(bullet)+TextStyle.Render(status))
	}
	return strings.Join(styledStatuses, "\n") + "\n"
}

func checkDependency(command string) bool {
	_, err := exec.LookPath(command)
	return err == nil
}

func main() {
	fmt.Println(BulletStyle.Render("┌") + TitleStyle.Render("tsplice"))

	var lang string
	var prompt string
	var help bool

	flag.StringVar(&lang, "lang", "auto", "Language for transcription (e.g. en, es, fr)")
	flag.StringVar(&prompt, "prompt", "", "Optional prompt used to create a more accurate transcription")
	flag.BoolVar(&help, "help", false, "Show usage info")
	flag.Usage = func() {
		fmt.Println(BulletStyle.Render("├") + TextStyle.Render("Usage: tsplice [options] <input-file>"))
		fmt.Println(BulletStyle.Render("│"))
		fmt.Println(BulletStyle.Render("├") + TextStyle.Render("Options:"))
		fmt.Println(BulletStyle.Render("├────") + TextStyle.Render("--lang") + DimTextStyle.Render("    language for transcription (e.g. en, es, fr)"))
		fmt.Println(BulletStyle.Render("├────") + TextStyle.Render("--prompt") + DimTextStyle.Render("  optional prompt used to create a more accurate transcription"))
		fmt.Println(BulletStyle.Render("│"))
		fmt.Println(BulletStyle.Render("├") + TextStyle.Render("Requirements:"))

		dependencies := []string{"ffmpeg", "mpv"}
		for _, dependency := range dependencies {
			status := "✔ installed"
			if !checkDependency(dependency) {
				status = "✗ missing"
			}
			spaces := strings.Repeat(" ", 10-len(dependency))
			fmt.Println(BulletStyle.Render("├────") + TextStyle.Render(dependency) + DimTextStyle.Render(spaces+status))
		}

		fmt.Println(BulletStyle.Render("│"))
		fmt.Println(BulletStyle.Render("└") + TextStyle.Render("Supported formats:") + DimTextStyle.Render(" .mp4, .avi, .mov, .mkv, .m4v"))
	}

	flag.Parse()

	if help {
		flag.Usage()
		os.Exit(0)
	}

	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(0)
	}

	// Validate the file exists
	inputFile := args[0]
	if inputFile == "help" {
		flag.Usage()
		os.Exit(0)
	}

	if _, err := os.Stat(inputFile); os.IsNotExist(err) {
		fmt.Printf(BulletStyle.Render("└")+TextStyle.Render("Error: file '%s' does not exist.")+"\n", inputFile)
		os.Exit(1)
	}

	// Validate the input file is a video file
	validExtensions := []string{".mp4", ".avi", ".mov", ".mkv", ".m4v"}
	fileExt := strings.ToLower(filepath.Ext(inputFile))

	if !slices.Contains(validExtensions, fileExt) {
		fmt.Printf(BulletStyle.Render("└")+TextStyle.Render("Error: file '%s' is not a valid video file.")+"\n", inputFile)
		os.Exit(1)
	}

	// Check if OPENAI_API_KEY env variable is set, and if not, prompt for it
	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Print(BulletStyle.Render("├") + TextStyle.Render("OPENAI_API_KEY not found. Please enter your API key: "))

		var apiKey string
		fmt.Scanln(&apiKey)

		if apiKey == "" {
			fmt.Println(BulletStyle.Render("└") + TextStyle.Render("API key is required to proceed."))
			os.Exit(1)
		}

		os.Setenv("OPENAI_API_KEY", apiKey)
		fmt.Println(BulletStyle.Render("├") + TextStyle.Render("API key set for this session."))
	}

	// Check if VTT file already exists
	basename := strings.TrimSuffix(filepath.Base(inputFile), filepath.Ext(inputFile))
	vttFile := basename + ".vtt"

	// Initialize spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = SpinnerStyle

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
			fmt.Fprintf(os.Stderr, BulletStyle.Render("└")+TextStyle.Render("There was a problem reading the existing VTT file: %v")+"\n", err)
			os.Exit(1)
		}

		transcriptItems, err := parseVTT(string(vttBytes))
		if err != nil {
			fmt.Fprintf(os.Stderr, BulletStyle.Render("└")+TextStyle.Render("There was a problem parsing the existing VTT file: %v")+"\n", err)
			os.Exit(1)
		}

		// Convert to list items
		items := make([]list.Item, len(transcriptItems))
		for i, transcriptItem := range transcriptItems {
			items[i] = item{
				title:     transcriptItem.Text,
				timestamp: transcriptItem.StartTime + " - " + transcriptItem.EndTime,
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
				key.NewBinding(
					key.WithKeys("c"),
					key.WithHelp("c", "compile"),
				),
			}
		}

		initialModel.loading = false
		initialModel.list = l
		initialModel.transcriptItems = transcriptItems
		initialModel.statuses = append(initialModel.statuses, "Transcript already exists locally")
	}

	// Create and run the program
	p := tea.NewProgram(
		initialModel,
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running program: %v", err)
	}
}
