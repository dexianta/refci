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
	width         int
	height        int
	now           time.Time
	repo          string
	mode          topViewMode
	pickerEnabled bool
	repos         []string
	selectedRepo  int
	repoListErr   string

	statusCh  <-chan StatusEvent
	dbRepo    core.DbRepo
	rerunCh   chan<- RerunRequest
	cancelCh  chan<- CancelRequest
	logsModel logsModel
}

type topViewMode int

const (
	topModeLogs topViewMode = iota
	topModeRepoPicker
)

type tickMsg time.Time

func newModel(repo string, dbRepo core.DbRepo, statusCh <-chan StatusEvent, rerunCh chan<- RerunRequest, cancelCh chan<- CancelRequest) topModel {
	return topModel{
		now:       time.Now(),
		repo:      repo,
		mode:      topModeLogs,
		statusCh:  statusCh,
		dbRepo:    dbRepo,
		rerunCh:   rerunCh,
		cancelCh:  cancelCh,
		logsModel: newLogsModel(dbRepo, repo, rerunCh, cancelCh),
	}
}

func newRepoPickerModel(dbRepo core.DbRepo, statusCh <-chan StatusEvent, rerunCh chan<- RerunRequest, cancelCh chan<- CancelRequest) topModel {
	return topModel{
		now:           time.Now(),
		mode:          topModeRepoPicker,
		pickerEnabled: true,
		statusCh:      statusCh,
		dbRepo:        dbRepo,
		rerunCh:       rerunCh,
		cancelCh:      cancelCh,
	}
}

func Run(ctx context.Context, repo string, dbRepo core.DbRepo, statusCh <-chan StatusEvent, rerunCh chan<- RerunRequest, cancelCh chan<- CancelRequest) error {
	p := tea.NewProgram(newModel(repo, dbRepo, statusCh, rerunCh, cancelCh), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func RunRepoPicker(ctx context.Context, dbRepo core.DbRepo, statusCh <-chan StatusEvent, rerunCh chan<- RerunRequest, cancelCh chan<- CancelRequest) error {
	p := tea.NewProgram(newRepoPickerModel(dbRepo, statusCh, rerunCh, cancelCh), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func (m topModel) Init() tea.Cmd {
	cmds := []tea.Cmd{tickCmd(), waitStatusCmd(m.statusCh)}
	if m.mode == topModeRepoPicker {
		cmds = append(cmds, loadRepoListCmd())
	} else {
		cmds = append(cmds, m.logsModel.Init())
	}
	return tea.Batch(cmds...)
}

func (m topModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case loadRepoListMsg:
		if msg.err != nil {
			m.repoListErr = msg.err.Error()
			m.repos = nil
			m.selectedRepo = 0
			return m, nil
		}
		m.repoListErr = ""
		m.repos = msg.repos
		if len(m.repos) == 0 {
			m.selectedRepo = 0
		} else if m.selectedRepo >= len(m.repos) {
			m.selectedRepo = len(m.repos) - 1
		}
		return m, nil
	case loadRepoJobsMsg, loadJobLogMsg:
		if m.mode != topModeLogs {
			return m, nil
		}
		m.logsModel, cmd, _ = m.logsModel.Update(msg)
		return m, cmd
	case tea.KeyMsg:
		if m.mode == topModeRepoPicker {
			return m.updateRepoPickerKey(msg)
		}
		var handled bool
		m.logsModel, cmd, handled = m.logsModel.Update(msg)
		switch msg.String() {
		case "esc", "p", "P":
			if !handled && m.pickerEnabled {
				m.mode = topModeRepoPicker
				return m, loadRepoListCmd()
			}
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
		if m.mode == topModeRepoPicker {
			return m, tickCmd()
		}
		var cmd1 tea.Cmd
		m.logsModel, cmd1, _ = m.logsModel.Update(msg)
		return m, tea.Batch(tickCmd(), cmd1)
	case statusEventMsg:
		m.logsModel, cmd, _ = m.logsModel.Update(msg)
		return m, tea.Batch(waitStatusCmd(m.statusCh), cmd)
	}

	return m, cmd
}

func (m topModel) updateRepoPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "up":
		m.selectedRepo = modIdx(m.selectedRepo, len(m.repos), -1)
		return m, nil
	case "down":
		m.selectedRepo = modIdx(m.selectedRepo, len(m.repos), 1)
		return m, nil
	case "r", "R":
		return m, loadRepoListCmd()
	case "enter":
		if len(m.repos) == 0 {
			return m, nil
		}
		repo := m.repos[m.selectedRepo]
		m.repo = repo
		m.mode = topModeLogs
		m.logsModel = newLogsModel(m.dbRepo, repo, m.rerunCh, m.cancelCh)
		return m, m.logsModel.Init()
	}
	return m, nil
}

func loadRepoListCmd() tea.Cmd {
	return func() tea.Msg {
		repos, err := core.ListLocalRepos()
		return loadRepoListMsg{repos: repos, err: err}
	}
}

func waitStatusCmd(statusCh <-chan StatusEvent) tea.Cmd {
	if statusCh == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-statusCh
		if !ok {
			return nil
		}
		return statusEventMsg{
			message: ev.Message,
			inErr:   ev.IsError,
		}
	}
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
	if m.mode == topModeRepoPicker {
		return appStyle.Render(strings.Join([]string{
			header,
			"",
			m.renderRepoPicker(),
			"\n\n\n\n\n",
			lipgloss.JoinVertical(lipgloss.Top, m.repoPickerHelp(), "", globalFooter),
		}, "\n"))
	}

	body := m.logsModel.View()
	footer := lipgloss.JoinVertical(lipgloss.Top, m.logsFooter(), "", globalFooter)
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

func (m topModel) renderRepoPicker() string {
	lines := make([]string, 0, len(m.repos))
	for i, repo := range m.repos {
		line := fixedCell(repo, 48)
		if i == m.selectedRepo {
			lines = append(lines, selectedItemStyle.Render("> "+line))
		} else {
			lines = append(lines, "  "+line)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, mutedStyle.Render("No repos found in ./repos."))
	}

	help := ""
	if strings.TrimSpace(m.repoListErr) != "" {
		help = errorStyle.Render(m.repoListErr)
	}
	return renderRegion("Repos", []string{strings.Join(lines, "\n")}, help, true)
}

func (m topModel) repoPickerHelp() string {
	return footerBarStyle.Render(
		renderHint("UP/DOWN", "move"),
		renderHint("ENTER", "monitor"),
		renderHint("R", "refresh"),
	)
}

func (m topModel) logsFooter() string {
	if !m.pickerEnabled || m.logsModel.mode != logsModeList {
		return m.logsModel.help()
	}
	return footerBarStyle.Render(
		renderHint("UP/DOWN", "move"),
		renderHint("ENTER", "job log"),
		renderHint("L", "ci log"),
		renderHint("R", "restart"),
		renderHint("C", "cancel"),
		renderHint("ESC/P", "repos"),
	)
}
