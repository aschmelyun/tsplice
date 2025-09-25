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

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

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

func getSystemUser() string {
	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME") // Windows fallback
	}
	if username == "" {
		username = "anon" // Default fallback
	}

	return username
}
