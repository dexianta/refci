package tui

import (
	"context"
	"dexianta/refci/core"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type topModel struct {
	width  int
	height int
	now    time.Time
	repo   string

	logsModel logsModel
}

type tickMsg time.Time

func newModel(repo string, dbRepo core.DbRepo) topModel {
	return topModel{
		now:       time.Now(),
		repo:      repo,
		logsModel: newLogsModel(dbRepo, repo),
	}
}

func Run(ctx context.Context, repo string, dbRepo core.DbRepo) error {
	p := tea.NewProgram(newModel(repo, dbRepo), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func (m topModel) Init() tea.Cmd {
	return tea.Batch(tickCmd(), m.logsModel.Init())
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case loadRepoJobsMsg, loadJobLogMsg:
		m.logsModel, cmd, _ = m.logsModel.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		var handled bool
		m.logsModel, cmd, handled = m.logsModel.Update(msg)
		switch msg.String() {
		case "ctrl+c":
			if !handled {
				return m, tea.Quit
			}
		}
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tickMsg:
		m.now = time.Time(msg)
		var cmd1 tea.Cmd
		m.logsModel, cmd1, _ = m.logsModel.Update(msg)
		return m, tea.Batch(tickCmd(), cmd1)
	}

	return m, cmd
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

var globalFooter = footerBarStyle.Render(
	renderHint("CTRL+C", "quit"),
)

func (m topModel) View() string {
	if m.width == 0 || m.height == 0 {
		return "loading..."
	}

	subHeader := mutedStyle.Render(fmt.Sprintf("%s", m.now.Format("2006-01-02 15:04:05 Z07:00")))
	header := lipgloss.JoinHorizontal(lipgloss.Top, headerStyle.Render("refci  -  zero-overhead CI"), " ", subHeader)
	body := m.logsModel.View()
	footer := lipgloss.JoinVertical(lipgloss.Top, m.logsModel.help(), "", globalFooter)
	repoLabel := sectionTitleStyle.Render(fmt.Sprint("\n", ">> "+m.repo, "\n"))
	return appStyle.Render(strings.Join([]string{
		header,
		repoLabel,
		"",
		body,
		"\n\n\n\n\n",
		footer,
	}, "\n"))
}
