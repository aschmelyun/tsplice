package main

import "github.com/charmbracelet/lipgloss"

var (
	TitleStyle        = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	BulletStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).PaddingRight(1)
	TextStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	SpinnerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	TimestampStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).PaddingLeft(2)
	ItemStyle         = lipgloss.NewStyle().PaddingLeft(2)
	SelectedItemStyle = lipgloss.NewStyle().PaddingLeft(0).Foreground(lipgloss.Color("3"))
	ErrorStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	SuccessStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)
