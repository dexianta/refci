package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type topModel struct {
	tabs      []string
	activeTab int
	width     int
	height    int
	now       time.Time

	repoInput    string
	repos        []string
	selectedRepo int
	projectFocus projectFocus
	settings     settings

	statusMessage string
	statusIsError bool
}

type tickMsg time.Time

func newModel() topModel {
	return topModel{
		tabs: []string{
			"Projects",
			"Branches",
			"Logs",
			"Settings",
		},
		repos:        []string{},
		projectFocus: focusInput,
		now:          time.Now(),
		settings:     newSettingModel(),
	}
}

func Run() error {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

func (m topModel) Init() tea.Cmd {
	return tickCmd()
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()

		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "left":
			if m.activeTab > 0 {
				m.activeTab--
			}
			return m, nil
		case "right":
			if m.activeTab < len(m.tabs)-1 {
				m.activeTab++
			}
			return m, nil
		}

		switch m.activeTab {
		case 0:
			return m.updateProjects(msg)
		case 3:
			m.settings = m.settings.Update(msg)
			return m, nil
		}

		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tickMsg:
		m.now = time.Time(msg)
		return m, tickCmd()
	default:
		return m, nil
	}
}

type projectFocus int

const (
	focusInput projectFocus = iota
	focusRepoList
)

func (m topModel) updateProjects(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab", "shift+tab":
		if m.projectFocus == focusInput {
			m.projectFocus = focusRepoList
		} else {
			m.projectFocus = focusInput
		}
		return m, nil
	case "enter":
		if m.projectFocus == focusInput {
			m = m.addRepoFromInput()
		}
		return m, nil
	case "backspace":
		if m.projectFocus == focusInput && len(m.repoInput) > 0 {
			runes := []rune(m.repoInput)
			m.repoInput = string(runes[:len(runes)-1])
		}
		return m, nil
	case "up", "k":
		if m.projectFocus == focusRepoList && m.selectedRepo > 0 {
			m.selectedRepo--
		}
		return m, nil
	case "down", "j":
		if m.projectFocus == focusRepoList && m.selectedRepo < len(m.repos)-1 {
			m.selectedRepo++
		}
		return m, nil
	case "d":
		if m.projectFocus == focusRepoList {
			m = m.deleteSelectedRepo()
		}
		return m, nil
	case "esc":
		if m.projectFocus == focusInput {
			m.repoInput = ""
		}
		m.statusMessage = ""
		m.statusIsError = false
		return m, nil
	}

	if m.projectFocus == focusInput && len(msg.Runes) > 0 {
		m.repoInput += string(msg.Runes)
	}

	return m, nil
}

func (m topModel) addRepoFromInput() topModel {
	raw := strings.TrimSpace(m.repoInput)
	if raw == "" {
		m.statusMessage = "Please enter a repository URL or owner/repo."
		m.statusIsError = true
		return m
	}
	if !strings.Contains(raw, "/") {
		m.statusMessage = "Repo looks invalid. Use github.com/owner/repo or owner/repo."
		m.statusIsError = true
		return m
	}

	for _, existing := range m.repos {
		if strings.EqualFold(existing, raw) {
			m.statusMessage = "Repo already exists in the list."
			m.statusIsError = true
			return m
		}
	}

	m.repos = append(m.repos, raw)
	m.selectedRepo = len(m.repos) - 1
	m.repoInput = ""
	m.statusMessage = "Repository added."
	m.statusIsError = false
	return m
}

func (m topModel) deleteSelectedRepo() topModel {
	if len(m.repos) == 0 {
		m.statusMessage = "No repository selected."
		m.statusIsError = true
		return m
	}

	deleted := m.repos[m.selectedRepo]
	m.repos = append(m.repos[:m.selectedRepo], m.repos[m.selectedRepo+1:]...)
	if m.selectedRepo >= len(m.repos) && m.selectedRepo > 0 {
		m.selectedRepo--
	}
	m.statusMessage = "Deleted: " + deleted
	m.statusIsError = false
	return m
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

var globalFooter = footerBarStyle.Render(
	renderHint("left/right", "switch tab "),
	renderHint("ctrl+c", "quit"),
)

func (m topModel) topHelp() string {
	return footerBarStyle.Render(
		renderHint("tab", "switch section"),
	)
}

func (m topModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading..."
	}

	header := headerStyle.Render("nci  -  zero-overhead CI")
	subHeader := mutedStyle.Render(fmt.Sprintf("%s", m.now.Format("2006-01-02 15:04:05 Z07:00")))
	tabs := m.renderTabs()
	body := m.renderBody()

	var footer string
	switch m.activeTab {
	case 0:
		footer = m.topHelp()
	case 3:
		footer = m.settings.help()
	}

	footer = lipgloss.JoinVertical(lipgloss.Top, footer, "", globalFooter)

	return appStyle.Render(strings.Join([]string{
		header,
		subHeader,
		"",
		tabs,
		"",
		body,
		"",
		footer,
	}, "\n"))
}

func (m topModel) renderTabs() string {
	parts := make([]string, 0, len(m.tabs))
	for i, tab := range m.tabs {
		if i == m.activeTab {
			parts = append(parts, tabActiveStyle.Render(" "+tab+" "))
			continue
		}
		parts = append(parts, tabStyle.Render(" "+tab+" "))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func (m topModel) renderBody() string {
	switch m.tabs[m.activeTab] {
	case "Projects":
		return m.renderProjectsLanding()
	case "Branches":
		return "Branches\n\nThis section will show branch status and recent runs."
	case "Logs":
		return "Logs\n\nThis section will stream live job output."
	case "Settings":
		return m.settings.View()
	default:
		return ""
	}
}

func (m topModel) renderProjectsLanding() string {
	logo := logoStyle.Render(strings.Join([]string{
		" _   _  ____ ___ ",
		"| \\ | |/ ___|_ _|",
		"|  \\| | |    | | ",
		"| |\\  | |___ | | ",
		"|_| \\_|\\____|___|",
	}, "\n"))

	inputCard := m.renderRepoInputCard()
	repoListCard := m.renderRepoListCard()

	var sections string
	if m.width < 110 {
		sections = lipgloss.JoinVertical(lipgloss.Left, inputCard, repoListCard)
	} else {
		sections = lipgloss.JoinHorizontal(lipgloss.Top, inputCard, repoListCard)
	}

	return strings.Join([]string{
		logo,
		"",
		sections,
	}, "\n")
}

func (m topModel) renderRepoInputCard() string {
	var b strings.Builder
	b.WriteString(sectionTitleStyle.Render("1) Add Repo"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("Paste GitHub URL or owner/repo, then press enter."))
	b.WriteString("\n\n")

	inputText := m.repoInput
	if inputText == "" {
		inputText = placeholderStyle.Render("https://github.com/owner/repo")
	}
	cursor := ""
	if m.projectFocus == focusInput {
		cursor = "█"
	}
	b.WriteString(inputStyle.Render("> " + inputText + cursor))

	if m.statusMessage != "" {
		b.WriteString("\n\n")
		if m.statusIsError {
			b.WriteString(errorStyle.Render(m.statusMessage))
		} else {
			b.WriteString(successStyle.Render(m.statusMessage))
		}
	}

	style := cardStyle
	if m.projectFocus == focusInput {
		style = style.BorderForeground(lipgloss.Color("45"))
	}

	width := 62
	if m.width < 110 {
		width = max(44, m.width-8)
	}
	return style.Width(width).Render(b.String())
}

func (m topModel) renderRepoListCard() string {
	var b strings.Builder
	b.WriteString(sectionTitleStyle.Render("2) Existing Repos"))
	b.WriteString("\n")

	if len(m.repos) == 0 {
		b.WriteString(mutedStyle.Render("No repos yet."))
	} else {
		for i, repo := range m.repos {
			line := fmt.Sprintf("  %d. %s", i+1, repo)
			if i == m.selectedRepo {
				b.WriteString(selectedItemStyle.Render("> " + strings.TrimSpace(line)))
			} else {
				b.WriteString(line)
			}
			if i < len(m.repos)-1 {
				b.WriteString("\n")
			}
		}
	}

	b.WriteString("\n\n")
	b.WriteString(mutedStyle.Render("tab focus this section, j/k move, d delete"))

	style := cardStyle
	if m.projectFocus == focusRepoList {
		style = style.BorderForeground(lipgloss.Color("45"))
	}

	width := 42
	if m.width < 110 {
		width = max(44, m.width-8)
	}
	return style.Width(width).Render(b.String())
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
