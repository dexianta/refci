package tui

import (
	"dexianta/refci/core"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestLogsPagination(t *testing.T) {
	jobs := make([]core.Job, 45)
	for i := range jobs {
		jobs[i].Name = fmt.Sprintf("job-%03d", i)
	}
	m := logsModel{repo: "repo", jobs: jobs}

	m, _, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.selected != 20 {
		t.Fatalf("selected = %d, want 20", m.selected)
	}
	view := m.renderJobList()
	if !strings.Contains(view, "Jobs (page 2/3)") || !strings.Contains(view, "job-020") || !strings.Contains(view, "job-039") {
		t.Fatal("second page is not rendered")
	}
	if strings.Contains(view, "job-019") || strings.Contains(view, "job-040") {
		t.Fatal("second page contains jobs from another page")
	}

	m, _, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.selected != 40 {
		t.Fatalf("selected = %d, want 40", m.selected)
	}
}

func TestSelectedJobHighlightsEntireRow(t *testing.T) {
	colorProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(0)
	defer lipgloss.SetColorProfile(colorProfile)

	job := core.Job{
		Name:         "build",
		Branch:       "main",
		SHA:          "123456789",
		CommitAuthor: "Dex",
		Status:       core.StatusFinished,
	}
	m := logsModel{jobs: []core.Job{job}}
	line := strings.Join([]string{
		fixedCell(job.Name, actionNameColWidth),
		fixedCell(job.Branch, branchColWidth),
		fixedCell(shortSHA(job.SHA), shaColWidth),
		fixedCell(job.CommitAuthor, authorColWidth),
		fixedCell(statusTag(job.Status), statusColWidth),
		fixedCell("--", elapsedColWidth),
		"--",
	}, "  ")

	if view := m.renderJobList(); !strings.Contains(view, selectedItemStyle.Render("> "+line)) {
		t.Fatal("selected style does not cover the entire row")
	}
}
