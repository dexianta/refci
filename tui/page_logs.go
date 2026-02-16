package tui

import (
	"dexianta/refci/core"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type logsViewMode int

const (
	logsModeList logsViewMode = iota
	logsModeDetail
)

type logsModel struct {
	dbRepo core.DbRepo
	repo   string

	jobs     []core.Job
	selected int

	mode    logsViewMode
	logPath string
	logRows []string

	statusMsg   string
	statusInErr bool
}

func newLogsModel(dbRepo core.DbRepo, repo string) logsModel {
	return logsModel{
		dbRepo: dbRepo,
		repo:   repo,
		mode:   logsModeList,
	}
}

func (m logsModel) Init() tea.Cmd {
	if m.repo == "" {
		return nil
	}
	return loadRepoJobsCmd(m.dbRepo, m.repo)
}

func loadRepoJobsCmd(dbRepo core.DbRepo, repo string) tea.Cmd {
	return func() tea.Msg {
		jobs, err := dbRepo.ListJob(core.JobFilter{Repo: repo})
		return loadRepoJobsMsg{
			repo: repo,
			jobs: jobs,
			err:  err,
		}
	}
}

func loadJobLogCmd(path string) tea.Cmd {
	return func() tea.Msg {
		rows, err := readTail(path, 220)
		return loadJobLogMsg{
			path:  path,
			lines: rows,
			err:   err,
		}
	}
}

func (m logsModel) Update(msg tea.Msg) (logsModel, tea.Cmd, bool) {
	switch mg := msg.(type) {
	case loadRepoJobsMsg:
		if mg.repo != m.repo {
			return m, nil, true
		}
		if mg.err != nil {
			m.statusInErr = true
			m.statusMsg = mg.err.Error()
			return m, nil, true
		}
		m.jobs = mg.jobs
		if len(m.jobs) == 0 {
			m.selected = 0
		} else if m.selected >= len(m.jobs) {
			m.selected = len(m.jobs) - 1
		}
		return m, nil, true

	case loadJobLogMsg:
		if m.mode != logsModeDetail || mg.path != m.logPath {
			return m, nil, true
		}
		if mg.err != nil {
			m.statusInErr = true
			m.statusMsg = mg.err.Error()
			m.logRows = nil
			return m, nil, true
		}
		m.logRows = mg.lines
		return m, nil, true
	case statusEventMsg:
		m.statusInErr = mg.inErr
		m.statusMsg = strings.TrimSpace(mg.message)
		return m, nil, true

	case tickMsg:
		if m.repo == "" {
			return m, nil, false
		}
		return m, loadRepoJobsCmd(m.dbRepo, m.repo), true

	case tea.KeyMsg:
		if m.repo == "" {
			return m, nil, false
		}

		if m.mode == logsModeDetail {
			switch mg.String() {
			case "esc", "enter", "backspace":
				m.mode = logsModeList
				return m, nil, true
			}
			return m, nil, false
		}

		switch mg.String() {
		case "up":
			m.selected = modIdx(m.selected, len(m.jobs), -1)
			return m, nil, true
		case "down":
			m.selected = modIdx(m.selected, len(m.jobs), 1)
			return m, nil, true
		case "enter":
			if len(m.jobs) == 0 {
				return m, nil, true
			}
			m.mode = logsModeDetail
			m.logPath = pathForJob(m.jobs[m.selected])
			m.logRows = nil
			return m, loadJobLogCmd(m.logPath), true
		}
	}

	return m, nil, false
}

func (m logsModel) View() string {
	if m.mode == logsModeDetail {
		return m.renderLogDetail()
	}
	return m.renderJobList()
}

func (m logsModel) help() string {
	if m.mode == logsModeDetail {
		return footerBarStyle.Render(
			renderHint("ESC/ENTER", "back"),
		)
	}

	return footerBarStyle.Render(
		renderHint("UP/DOWN", "move"),
		renderHint("ENTER", "open log"),
	)
}

func (m logsModel) renderJobList() string {
	lines := make([]string, 0, len(m.jobs))
	now := time.Now()
	for i, j := range m.jobs {
		line := fmt.Sprintf("%-14s  %-14s  %-8s  %-6s  %-7s  %s",
			j.Name,
			j.Branch,
			shortSHA(j.SHA),
			statusTag(j.Status),
			elapsedForJob(now, j),
			timeAgo(now, lastTime(j)),
		)
		if i == m.selected {
			lines = append(lines, selectedItemStyle.Render("> "+line))
		} else {
			lines = append(lines, "  "+line)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, mutedStyle.Render("No jobs yet."))
	}

	help := ""
	if m.statusMsg != "" {
		if m.statusInErr {
			help = errorStyle.Render(m.statusMsg)
		} else {
			help = successStyle.Render(m.statusMsg)
		}
	}

	return renderRegion("Jobs", []string{strings.Join(lines, "\n")}, help, true)
}

func (m logsModel) renderLogDetail() string {
	header := sectionTitleStyle.Render("Log Detail")
	meta := mutedStyle.Render(fmt.Sprintf("path=%s", m.logPath))

	body := mutedStyle.Render("(empty)")
	if len(m.logRows) > 0 {
		body = strings.Join(m.logRows, "\n")
	}
	if m.statusMsg != "" && m.statusInErr {
		body = errorStyle.Render(m.statusMsg) + "\n\n" + body
	}

	content := lipgloss.JoinVertical(lipgloss.Left, header, meta, "", body)
	return regionFocusedStyle.Render(content)
}

func shortSHA(sha string) string {
	s := strings.TrimSpace(sha)
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

func shortLogSHA(sha string) string {
	s := strings.TrimSpace(sha)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func statusTag(v string) string {
	switch strings.ToLower(v) {
	case core.StatusFinished:
		return "PASS"
	case core.StatusFailed:
		return "FAIL"
	case core.StatusRunning:
		return "RUN"
	case core.StatusPending:
		return "WAIT"
	case core.StatusCanceled:
		return "CANC"
	default:
		return strings.ToUpper(v)
	}
}

func lastTime(j core.Job) time.Time {
	if !j.End.IsZero() {
		return j.End
	}
	return j.Start
}

func timeAgo(now, t time.Time) string {
	if t.IsZero() {
		return "--"
	}
	d := now.Sub(t)
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func elapsedForJob(now time.Time, j core.Job) string {
	if j.Start.IsZero() {
		return "--"
	}

	end := j.End
	if end.IsZero() {
		end = now
	}
	return compactDuration(end.Sub(j.Start))
}

func compactDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func readTail(path string, max int) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	rows := strings.Split(string(b), "\n")
	if len(rows) > max {
		rows = rows[len(rows)-max:]
	}
	return rows, nil
}

func pathForJob(job core.Job) string {
	msg := job.Msg
	if msg != "" {
		if _, err := os.Stat(msg); err == nil {
			return msg
		}
	}
	repoPart := core.ToLocalRepo(job.Repo)
	namePart := sanitizeLogToken(job.Name)
	branchPart := sanitizeLogToken(job.Branch)
	shaPart := sanitizeLogToken(shortLogSHA(job.SHA))
	return filepath.Join(core.Root, "logs", repoPart, fmt.Sprintf("%s-%s-%s.log", namePart, branchPart, shaPart))
}

func sanitizeLogToken(s string) string {
	out := strings.ReplaceAll(s, "/", "--")
	out = strings.ReplaceAll(out, "\\", "--")
	out = strings.ReplaceAll(out, ":", "_")
	out = strings.ReplaceAll(out, " ", "_")
	return out
}

func renderRegion(title string, lines []string, helpText string, focused bool) string {
	style := regionStyle
	if focused {
		style = regionFocusedStyle
	}

	parts := []string{
		sectionTitleStyle.Render(title),
		"",
		strings.Join(lines, "\n\n"),
	}
	if strings.TrimSpace(helpText) != "" {
		parts = append(parts, "", "", mutedStyle.Render(helpText))
	}
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	return style.Render(content)
}
