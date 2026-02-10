package tui

import (
	"dexianta/nci/core"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type sshViewer struct {
	path          string
	hosts         []core.SSHHost
	selected      int
	statusMessage string
	statusIsError bool
}

func newSSHViewer() sshViewer {
	host, err := core.LoadSSHHosts("")
	msg := "Read-only preview. Press r to reload (not wired yet)."

	if err != nil {
		msg = fmt.Sprintf("Error loading ssh config, Press r to reload: %s", err.Error())
	}
	return sshViewer{
		path:          "~/.ssh/config",
		hosts:         host,
		statusMessage: msg,
	}
}

func (v sshViewer) Update(msg tea.KeyMsg) sshViewer {
	switch msg.String() {
	case "up", "k":
		if len(v.hosts) > 0 {
			v.selected = modIdx(v.selected, len(v.hosts), -1)
		}
	case "down", "j":
		if len(v.hosts) > 0 {
			v.selected = modIdx(v.selected, len(v.hosts), 1)
		}
	case "r":
		v.statusMessage = "Reload not wired yet."
		v.statusIsError = false
	}
	return v
}

func (v sshViewer) View() string {
	var lines []string
	lines = append(lines, sectionTitleStyle.Render("SSH Config"))
	lines = append(lines, mutedStyle.Render("Path: "+v.path))
	lines = append(lines, "")
	lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, v.renderHostList(), v.renderHostDetails()))
	lines = append(lines, "")
	lines = append(lines, mutedStyle.Render("j/k move host  r reload"))

	if v.statusMessage != "" {
		lines = append(lines, "")
		if v.statusIsError {
			lines = append(lines, errorStyle.Render(v.statusMessage))
		} else {
			lines = append(lines, successStyle.Render(v.statusMessage))
		}
	}

	return strings.Join(lines, "\n")
}

func (v sshViewer) renderHostList() string {
	var items []string
	items = append(items, sectionTitleStyle.Render("Hosts"))
	items = append(items, "")

	if len(v.hosts) == 0 {
		items = append(items, mutedStyle.Render("No host entries found."))
		return lipgloss.NewStyle().Width(28).Render(strings.Join(items, "\n"))
	}

	for i, h := range v.hosts {
		if i == v.selected {
			items = append(items, itemSelectedStyle.Render(h.Pattern))
		} else {
			items = append(items, itemStyle.Background(lipgloss.Color("236")).Render(h.Pattern))
		}
	}

	return lipgloss.NewStyle().Width(28).Render(strings.Join(items, "\n"))
}

func (v sshViewer) renderHostDetails() string {
	var lines []string
	lines = append(lines, sectionTitleStyle.Render("Details"))
	lines = append(lines, "")

	h, ok := v.selectedHost()
	if !ok {
		lines = append(lines, mutedStyle.Render("Select a host entry."))
		return strings.Join(lines, "\n")
	}

	lines = append(lines, fmt.Sprintf("Host %s", h.Pattern))
	lines = append(lines, "")
	for _, e := range h.Entries {
		lines = append(lines, renderLabelInputRow(e.Key, e.Value, false, 28))
	}

	return strings.Join(lines, "\n")
}

func (v sshViewer) selectedHost() (core.SSHHost, bool) {
	if len(v.hosts) == 0 || v.selected < 0 || v.selected >= len(v.hosts) {
		return core.SSHHost{}, false
	}
	return v.hosts[v.selected], true
}
