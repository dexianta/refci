package tui

import "github.com/charmbracelet/lipgloss"

var (
	appStyle = lipgloss.NewStyle().
			Padding(1, 2)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("31")).
			Padding(0, 1)

	sectionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("111"))

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	footerBarStyle = lipgloss.NewStyle().
			BorderTop(true).
			BorderForeground(lipgloss.Color("238")).
			Foreground(lipgloss.Color("245")).
			Padding(0, 1)

	keycapStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("24")).
			Padding(0, 1)

	selectedItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("229")).
				Background(lipgloss.Color("60")).
				Bold(true)

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("114"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	regionStyle = lipgloss.NewStyle().
			Padding(0, 1).
			BorderStyle(lipgloss.NormalBorder()).
			BorderTop(true).
			BorderLeft(false).
			BorderRight(false).
			BorderBottom(false).
			BorderForeground(lipgloss.Color("240"))

	regionFocusedStyle = regionStyle.
				BorderForeground(lipgloss.Color("45"))
)
