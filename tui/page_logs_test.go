package tui

import (
	"dexianta/refci/core"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
