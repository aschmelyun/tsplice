package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const VERSION = "1.0.0"

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

	fmt.Fprintf(w, "%s\n%s\n", timestampLine, fn(str))
}

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
		m.statuses = append(m.statuses, "Audio extracted from ffmpeg.")
		m.loadingMsg = "Transcribing with OpenAI Whisper..."
		return m, transcribeAudioCmd(msg.audioFile)

	case transcriptionDoneMsg:
		m.statuses = append(m.statuses, "Transcription finished and saved locally.")
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
		l := list.New(items, itemDelegate{}, 64, 16)
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
		m.statuses = append(m.statuses, "Video compiled successfully.")
		m.statuses = append(m.statuses, "Saved output to "+msg.outputFile)
		m.loading = false
		m.quitting = true
		return m, tea.Quit

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
		loadingText := fmt.Sprintf("%s%s", m.spinner.View(), m.loadingMsg)
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

func main() {
	fmt.Println(BulletStyle.Render("┌") + TitleStyle.Render("tsplice"))

	var lang string
	var prompt string
	var help bool
	var version bool

	flag.StringVar(&lang, "lang", "auto", "Language for transcription (e.g. en, es, fr)")
	flag.StringVar(&prompt, "prompt", "", "Optional prompt used to create a more accurate transcription")
	flag.BoolVar(&help, "help", false, "Show usage info")
	flag.BoolVar(&version, "version", false, "Show version info")
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

	if version {
		fmt.Println(BulletStyle.Render("└") + TextStyle.Render(VERSION))
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
	username := getSystemUser()

	apiKey, err := keyring.Get("tsplice", username)
	if err != nil {
		if !strings.Contains(err.Error(), "secret not found") {
			fmt.Println("Error reading API key:", err)
			return
		}
	}

	if apiKey != "" {
		os.Setenv("OPENAI_API_KEY", apiKey)
		fmt.Println(BulletStyle.Render("├") + TextStyle.Render("API key set for this session."))
	}

	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Print(BulletStyle.Render("├") + TextStyle.Render("OPENAI_API_KEY not found, enter one: "))

		byteApiKey, err := term.ReadPassword(int(syscall.Stdin))
		if err != nil {
			fmt.Println("Error reading API key:", err)
			return
		}

		fmt.Println()
		apiKey := strings.TrimSpace(string(byteApiKey))

		if apiKey == "" {
			fmt.Println(BulletStyle.Render("└") + TextStyle.Render("An OpenAI API key is required to proceed."))
			os.Exit(1)
		}

		err = keyring.Set("tsplice", username, apiKey)
		if err != nil {
			fmt.Println("Error saving API key:", err)
			return
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
		l := list.New(items, itemDelegate{}, 64, 16)
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
