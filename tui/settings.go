package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type settingsModel struct {
	sectionIdx   int
	sectionFocus bool
	sectionItems []string

	// sections
	globalConf globalConf
	EnvVars    []KV
	sshConfig  []SSHConfig
}

type SSHConfig struct {
}

type KV struct {
	key string
	val string
}

type globalConf struct {
	pollInterval      int
	logRetentionDays  int
	maxConcurrentJobs int
	gitTimeoutSec     int
}

func newSettingModel() settingsModel {
	return settingsModel{sectionFocus: true, sectionItems: []string{"Global", "Env Vars", "SSH"}}
}

func (s settingsModel) Update(msg tea.KeyMsg) settingsModel {
	switch s.sectionFocus {
	case true:
		// focus on section
		switch msg.String() {
		case "up":
			s.sectionIdx = modIdx(s.sectionIdx, 3, -1)
		case "down":
			s.sectionIdx = modIdx(s.sectionIdx, 3, 1)
		}
	case false:
		// focus on editor
	}
	return s
}

func (s settingsModel) View() string {
	editor := ""
	switch s.sectionIdx {
	case 0:
		editor = s.renderGlobalConfEditor()
	case 1:
		editor = s.renderKVEditor()
	case 2:
		editor = s.renderSSHConf()
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, s.renderSection(), editor)
}

func (s settingsModel) renderSection() string {
	var text = []string{sectionTitleStyle.Render("Sections"), ""}
	for i, t := range s.sectionItems {
		if i == s.sectionIdx {
			text = append(text, itemSelectedStyle.Render(t))
		} else {
			text = append(text, itemStyle.Background(lipgloss.Color("000")).Render(t))
		}
	}

	return lipgloss.JoinVertical(lipgloss.Top, text...)
}

func (s settingsModel) renderGlobalConfEditor() string {
	return ""
}

func (s settingsModel) renderKVEditor() string {
	return ""
}

func (s settingsModel) renderSSHConf() string {
	return ""
}
