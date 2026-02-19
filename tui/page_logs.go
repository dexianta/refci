package tui

import (
	"dexianta/refci/core"
	"fmt"
	"hash/fnv"
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
	dbRepo   core.DbRepo
	repo     string
	rerunCh  chan<- RerunRequest
	cancelCh chan<- CancelRequest

	jobs     []core.Job
	selected int

	mode    logsViewMode
	logPath string
	logRows []string

	statusMsg   string
	statusInErr bool
	jobsLoadErr bool
}

func newLogsModel(dbRepo core.DbRepo, repo string, rerunCh chan<- RerunRequest, cancelCh chan<- CancelRequest) logsModel {
	return logsModel{
		dbRepo:   dbRepo,
		repo:     repo,
		rerunCh:  rerunCh,
		cancelCh: cancelCh,
		mode:     logsModeList,
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
		jobs, err := dbRepo.ListJob(core.JobFilter{Repo: repo, Limit: 10})
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

func requestRerunCmd(ch chan<- RerunRequest, req RerunRequest) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case ch <- req:
			return statusEventMsg{
				message: fmt.Sprintf("restart queued for %s/%s@%s", req.Name, req.Branch, shortSHA(req.SHA)),
				inErr:   false,
			}
		default:
			return statusEventMsg{
				message: "restart queue is full, try again",
				inErr:   true,
			}
		}
	}
}

func requestCancelCmd(ch chan<- CancelRequest, req CancelRequest) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case ch <- req:
			return statusEventMsg{
				message: fmt.Sprintf("cancel requested for %s/%s@%s", req.Name, req.Branch, shortSHA(req.SHA)),
				inErr:   false,
			}
		default:
			return statusEventMsg{
				message: "cancel queue is full, try again",
				inErr:   true,
			}
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
			m.jobsLoadErr = true
			return m, nil, true
		}
		m.jobs = mg.jobs
		if len(m.jobs) == 0 {
			m.selected = 0
		} else if m.selected >= len(m.jobs) {
			m.selected = len(m.jobs) - 1
		}
		if m.jobsLoadErr && m.statusInErr {
			m.statusMsg = ""
			m.statusInErr = false
		}
		m.jobsLoadErr = false
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
		if m.mode == logsModeDetail && strings.TrimSpace(m.logPath) != "" {
			return m, loadJobLogCmd(m.logPath), true
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
		case "r":
			if len(m.jobs) == 0 {
				return m, nil, true
			}
			job := m.jobs[m.selected]
			if strings.ToLower(job.Status) != core.StatusFailed {
				m.statusInErr = true
				m.statusMsg = "select a failed job to restart"
				return m, nil, true
			}
			return m, requestRerunCmd(m.rerunCh, RerunRequest{
				Repo:   job.Repo,
				Name:   job.Name,
				Branch: job.Branch,
				SHA:    job.SHA,
			}), true
		case "c":
			if len(m.jobs) == 0 {
				return m, nil, true
			}
			job := m.jobs[m.selected]
			status := strings.ToLower(job.Status)
			if status != core.StatusRunning && status != core.StatusPending {
				m.statusInErr = true
				m.statusMsg = "select a running/pending job to cancel"
				return m, nil, true
			}
			return m, requestCancelCmd(m.cancelCh, CancelRequest{
				Repo:   job.Repo,
				Name:   job.Name,
				Branch: job.Branch,
				SHA:    job.SHA,
			}), true
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

	hints := []string{
		renderHint("UP/DOWN", "move"),
		renderHint("ENTER", "open log"),
		renderHint("R", "restart"),
		renderHint("C", "cancel"),
	}
	return footerBarStyle.Render(hints...)
}

const (
	actionNameColWidth = 24
	branchColWidth     = 14
	shaColWidth        = 8
	statusColWidth     = 9
	elapsedColWidth    = 7
)

var actionNamePalette = []lipgloss.Color{
	"196", "202", "220", "118", "47", "51", "39", "99", "201", "165", "208", "178",
}

func (m logsModel) renderJobList() string {
	lines := make([]string, 0, len(m.jobs))
	now := time.Now()
	for i, j := range m.jobs {
		nameCell := renderActionName(j.Name, actionNameColWidth)
		branchCell := fixedCell(j.Branch, branchColWidth)
		shaCell := fixedCell(shortSHA(j.SHA), shaColWidth)
		statusCell := fixedCell(statusTag(j.Status), statusColWidth)
		elapsedCell := fixedCell(elapsedForJob(now, j), elapsedColWidth)

		line := strings.Join([]string{
			nameCell,
			branchCell,
			shaCell,
			statusCell,
			elapsedCell,
			timeAgo(now, j.Start),
		}, "  ")

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

func renderActionName(name string, width int) string {
	cell := fixedCell(name, width)
	if strings.TrimSpace(name) == "" {
		return cell
	}
	return actionNameStyle(name).Render(cell)
}

func actionNameStyle(name string) lipgloss.Style {
	if len(actionNamePalette) == 0 {
		return lipgloss.NewStyle().Bold(true)
	}
	idx := actionNameColorIndex(name)
	return lipgloss.NewStyle().Foreground(actionNamePalette[idx]).Bold(true)
}

func actionNameColorIndex(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(name))))
	return int(h.Sum32() % uint32(len(actionNamePalette)))
}

func fixedCell(v string, width int) string {
	if width <= 0 {
		return strings.TrimSpace(v)
	}
	r := []rune(strings.TrimSpace(v))
	if len(r) > width {
		r = r[:width]
	}
	s := string(r)
	if pad := width - len(r); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
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
		return "FINISHED"
	case core.StatusFailed:
		return "FAILED"
	case core.StatusRunning:
		return "RUNNING"
	case core.StatusPending:
		return "PENDING"
	case core.StatusCanceled:
		return "CANCELED"
	default:
		return strings.ToUpper(v)
	}
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
		m := d / time.Minute
		s := d % time.Minute / time.Second
		return fmt.Sprintf("%dm%ds", m, s)
	case d < 24*time.Hour:
		h := d / time.Hour
		m := d % time.Hour / time.Minute
		return fmt.Sprintf("%dh%dm", h, m)
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
