package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type settings struct {
	sectionIdx   int
	sectionFocus bool
	sectionItems []string

	// sections
	globalConf globalConf
	EnvVars    []KV
	sshConfig  []SSHConfig

	globalConfForm form
	varsForm       form
	sshViewer      sshViewer
}

type SSHConfig struct {
}

type KV struct {
	key   string
	val   string
	dtype string // string, number, bool, date
}

type globalConf struct {
	pollInterval      int
	logRetentionDays  int
	maxConcurrentJobs int
	gitTimeoutSec     int
}

func newSettingModel() settings {
	return settings{
		sectionFocus: true,
		sectionItems: []string{"Global", "Env Vars", "SSH"},
		globalConfForm: newForm([]KV{
			{key: "Poll Interval (s)", val: "3", dtype: "int"},
			{key: "Git Timeout (s)", val: "3", dtype: "int"},
			{key: "Log Retention Days", val: "3", dtype: "int"},
		}, 14, false),
		varsForm: newForm([]KV{
			{key: "NCI_ENV", val: "dev", dtype: "string"},
			{key: "GOFLAGS", val: "-count=1", dtype: "string"},
		}, 20, true),
		sshViewer: newSSHViewer(),
	}
}

func (s settings) Update(msg tea.KeyMsg) settings {
	switch s.sectionFocus {
	case true:
		// focus on section
		switch msg.String() {
		case "up", "k":
			s.sectionIdx = modIdx(s.sectionIdx, len(s.sectionItems), -1)
		case "down", "j":
			s.sectionIdx = modIdx(s.sectionIdx, len(s.sectionItems), 1)
		case "tab":
			s.sectionFocus = false
		}
	case false:
		// focus on editor
		switch msg.String() {
		case "tab":
			if !s.activeForm().IsEditing() {
				s.sectionFocus = true
			}
			return s // early return
		}
		switch s.sectionIdx {
		case 0:
			s.globalConfForm = s.globalConfForm.Update(msg)
		case 1:
			s.varsForm = s.varsForm.Update(msg)
		case 2:
			s.sshViewer = s.sshViewer.Update(msg)
		}
	}
	return s
}

func (s settings) View() string {
	editor := ""
	switch s.sectionIdx {
	case 0:
		editor = s.globalConfForm.View()
	case 1:
		editor = s.varsForm.View()
	case 2:
		editor = s.sshViewer.View()
	}

	if !s.sectionFocus {
		editor = cardFocusedStyle.Render(editor)
	} else {
		editor = cardStyle.Render(editor)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, s.renderSection(), editor)
}

func (s settings) renderSection() string {
	var text = []string{sectionTitleStyle.Render("Sections"), ""}
	for i, t := range s.sectionItems {
		if i == s.sectionIdx {
			text = append(text, itemSelectedStyle.Render(t))
		} else {
			text = append(text, itemStyle.Background(lipgloss.Color("236")).Render(t))
		}
	}
	list := lipgloss.NewStyle().Width(16).Render(lipgloss.JoinVertical(lipgloss.Top, text...))
	return list
}

func (s settings) activeForm() form {
	switch s.sectionIdx {
	case 0:
		return s.globalConfForm
	case 1:
		return s.varsForm
	default:
		return s.globalConfForm
	}
}

func (s settings) help() string {
	return footerBarStyle.Render(
		renderHint("up/down", "move "),
		renderHint("tab", "switch pane"),
	)
}
