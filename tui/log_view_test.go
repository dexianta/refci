package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func logKey(m logsModel, key string) logsModel {
	types := map[string]tea.KeyType{
		"up": tea.KeyUp, "down": tea.KeyDown, "ctrl+u": tea.KeyCtrlU, "ctrl+d": tea.KeyCtrlD,
		"pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd,
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "backspace": tea.KeyBackspace,
	}
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if kind, ok := types[key]; ok {
		msg = tea.KeyMsg{Type: kind}
	}
	m, _, _ = m.Update(msg)
	return m
}

func refreshLogForTest(t *testing.T, m logsModel) (logsModel, loadJobLogMsg) {
	t.Helper()
	cmd := m.refreshLog()
	if cmd == nil {
		t.Fatal("log refresh was unexpectedly blocked")
	}
	msg := cmd().(loadJobLogMsg)
	if msg.err != nil {
		t.Fatal(msg.err)
	}
	m, _, _ = m.Update(msg)
	return m, msg
}

func TestLogHistoryScrollAndFollow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	var content strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&content, "line-%03d %s\n", i, strings.Repeat("x", 600))
	}
	if err := os.WriteFile(path, []byte(content.String()), 0600); err != nil {
		t.Fatal(err)
	}
	m := logsModel{height: 26, width: 100}
	cmd := m.openLog(logsModeDetail, path)
	m, _, _ = m.Update(cmd())
	if len(m.logRows) != 501 || !strings.HasPrefix(m.logRows[0], "line-000") {
		t.Fatal("log history was truncated")
	}
	if m.logPageHeight() != 20 || m.logOffset != 481 || !m.logFollow {
		t.Fatalf("initial viewport: %+v", m)
	}
	for _, step := range []struct {
		key    string
		offset int
	}{
		{"up", 480}, {"ctrl+u", 470}, {"ctrl+d", 480}, {"pgup", 460}, {"pgdown", 480}, {"down", 481},
		{"home", 0}, {"up", 0}, {"ctrl+u", 0}, {"end", 481},
	} {
		m = logKey(m, step.key)
		if m.logOffset != step.offset {
			t.Fatalf("%s offset=%d, want %d", step.key, m.logOffset, step.offset)
		}
	}
	m = logKey(m, "home")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new output\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	m, msg := refreshLogForTest(t, m)
	if string(msg.data) != "new output\n" || msg.reset {
		t.Fatal("refresh reread existing history")
	}
	if m.logOffset != 0 || m.logFollow {
		t.Fatal("new output moved a paused viewport")
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "line-000") || strings.Contains(view, "new output") {
		t.Fatal("viewport did not show the selected page")
	}
	m = logKey(m, "G")
	if !m.logFollow || !strings.Contains(ansi.Strip(m.View()), "new output") {
		t.Fatal("G did not return to live output")
	}
	m, msg = refreshLogForTest(t, m)
	if len(msg.data) != 0 {
		t.Fatal("unchanged log was reread")
	}
}

func TestLogSearchFindsHistoryAndWraps(t *testing.T) {
	m := logsModel{mode: logsModeCI, height: 10, width: 40, logMatch: -1, logFollow: true}
	for i := 0; i < 50; i++ {
		m.logRows = append(m.logRows, fmt.Sprintf("line %d", i))
	}
	m.logRows[2] = "\x1b[31mERROR [db]\x1b[0m"
	m.logRows[40] = strings.Repeat("x", 80) + "error [DB]"
	m.clampLogOffset()
	m = logKey(m, "/")
	m = logKey(m, "error [db]")
	m = logKey(m, "enter")
	if m.mode != logsModeCI || m.searching || !reflect.DeepEqual(m.logMatches, []int{2, 40}) {
		t.Fatalf("search matches=%v", m.logMatches)
	}
	if m.logOffset != 2 || m.logMatch != 0 || m.logFollow {
		t.Fatal("search did not wrap to the first historical match")
	}
	if !strings.Contains(ansi.Strip(m.View()), "ERROR [db]") {
		t.Fatal("matching line is not visible")
	}
	m = logKey(m, "n")
	if m.logOffset != 40 || m.logMatch != 1 || !strings.Contains(ansi.Strip(m.View()), "error [DB]") {
		t.Fatal("next match or horizontal reveal failed")
	}
	m = logKey(m, "n")
	if m.logOffset != 2 || m.logMatch != 0 {
		t.Fatal("next match did not wrap")
	}
	m = logKey(m, "N")
	if m.logOffset != 40 || m.logMatch != 1 {
		t.Fatal("previous match did not wrap")
	}
	m = logKey(m, "/")
	m = logKey(m, "different")
	m = logKey(m, "esc")
	if m.mode != logsModeCI || m.logQuery != "error [db]" {
		t.Fatal("canceling search closed the log or lost the old query")
	}
	m = logKey(m, "/")
	m = logKey(m, "absent")
	m = logKey(m, "enter")
	if !strings.Contains(ansi.Strip(m.View()), "no matches") {
		t.Fatal("missing search results are not reported")
	}
	m = logKey(m, "/")
	m = logKey(m, "enter")
	if m.logQuery != "" || len(m.logMatches) != 0 {
		t.Fatal("empty search did not clear matches")
	}
	m = logKey(m, "/")
	m = logKey(m, "错误")
	m = logKey(m, "backspace")
	if m.logInput != "错" {
		t.Fatal("backspace corrupted Unicode input")
	}
	m = logKey(m, "ctrl+u")
	m = logKey(m, "backspace")
	if m.logInput != "" {
		t.Fatal("clearing empty input failed")
	}
}

func TestLogPartialLinesTruncationAndRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ci.log")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("before\npar")
	m := logsModel{mode: logsModeCI, logPath: path, logQuery: "partial", logMatch: -1}
	m, _ = refreshLogForTest(t, m)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("tial\nafter\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	m, _ = refreshLogForTest(t, m)
	if !reflect.DeepEqual(m.logRows, []string{"before", "partial", "after", ""}) || !reflect.DeepEqual(m.logMatches, []int{1}) {
		t.Fatalf("partial line update: %v, matches=%v", m.logRows, m.logMatches)
	}
	write("short\n")
	m, msg := refreshLogForTest(t, m)
	if !msg.reset || !reflect.DeepEqual(m.logRows, []string{"short", ""}) || len(m.logMatches) != 0 {
		t.Fatal("truncation left old content or matches")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	write("replacement is longer\n")
	m, msg = refreshLogForTest(t, m)
	if !msg.reset || m.logRows[0] != "replacement is longer" {
		t.Fatal("rotation did not reload the new file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cmd := m.refreshLog()
	m, _, _ = m.Update(cmd())
	if m.logError == "" || len(m.logRows) == 0 {
		t.Fatal("read error discarded history or was not shown")
	}
	write("recovered\n")
	m, _ = refreshLogForTest(t, m)
	if m.logError != "" || m.logRows[0] != "recovered" {
		t.Fatal("read error did not recover")
	}
}

func TestLogIgnoresStaleLoadsAndAvoidsOverlappingRefresh(t *testing.T) {
	m := logsModel{}
	old := m.openLog(logsModeDetail, "")
	if cmd := m.refreshLog(); cmd != nil {
		t.Fatal("overlapping refresh was started")
	}
	current := m.openLog(logsModeDetail, "")
	m, _, _ = m.Update(old())
	if !m.logLoading {
		t.Fatal("stale load changed the current request")
	}
	m, _, _ = m.Update(current())
	if m.logLoading {
		t.Fatal("current load did not finish")
	}
}

func TestLogViewportFitsTerminalAndResizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{120, 40}, {80, 24}, {50, 20}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := newModel("acme/api", nil, nil, nil, nil)
			m.logsModel.mode = logsModeDetail
			m.logsModel.logFollow = true
			for i := 0; i < 100; i++ {
				m.logsModel.logRows = append(m.logsModel.logRows, strings.Repeat("long ", 50))
			}
			next, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			m = next.(topModel)
			view := m.View()
			if lipgloss.Height(view) != size.height || lipgloss.Width(view) > size.width {
				t.Fatalf("rendered %dx%d, want at most %dx%d", lipgloss.Width(view), lipgloss.Height(view), size.width, size.height)
			}
			if m.logsModel.logOffset+m.logsModel.logPageHeight() != 100 {
				t.Fatal("resize lost the bottom of a following log")
			}
			next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
			m = next.(topModel)
			offset := m.logsModel.logOffset
			next, _ = m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height - 1})
			m = next.(topModel)
			if m.logsModel.logOffset != offset || m.logsModel.logFollow {
				t.Fatal("resize moved a paused log")
			}
		})
	}
}
