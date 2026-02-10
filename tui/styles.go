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

	logoStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("117"))

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

	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("24"))

	tabStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Background(lipgloss.Color("237"))

	cardStyle = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(1, 2).
			MarginRight(1)

	pageCardStyle = cardStyle.Width(0).Height(0).Border(lipgloss.ASCIIBorder(), true, false, false, false)

	selectedItemStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("229")).
				Background(lipgloss.Color("60")).
				Bold(true)

	inputStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("230")).
			Background(lipgloss.Color("238")).
			Padding(0, 1)

	placeholderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("114"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("249")).Width(22)
	valueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("230")).Background(lipgloss.Color("238")).Padding(0, 1)
	valueFocus = valueStyle.Copy().Background(lipgloss.Color("31")).Bold(true)

	itemStyle = lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(lipgloss.Color("250")).
			Background(lipgloss.Color("237")).
			MarginBottom(1) // vertical gap

	itemSelectedStyle = itemStyle.Copy().
				Foreground(lipgloss.Color("230")).
				Background(lipgloss.Color("24")).
				Bold(true)
)
