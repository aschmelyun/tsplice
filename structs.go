package main

import (
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
)

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

type videoCompilationDoneMsg struct {
	outputFile string
}

type TranscriptItem struct {
	StartTime string
	EndTime   string
	Text      string
}

type model struct {
	spinner         spinner.Model
	loading         bool
	loadingMsg      string
	list            list.Model
	quitting        bool
	inputFile       string
	errorMsg        string
	transcriptItems []TranscriptItem
	statuses        []string
}

type item struct {
	title     string
	timestamp string
	selected  bool
}

type itemDelegate struct{}
