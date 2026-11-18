package tui

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func loadJobLogCmd(path string, offset int64, previous os.FileInfo, request int) tea.Cmd {
	return func() tea.Msg {
		msg := loadJobLogMsg{path: path, request: request}
		if path == "" {
			return msg
		}
		f, err := os.Open(path)
		if err != nil {
			msg.err = err
			return msg
		}
		defer f.Close()
		msg.info, msg.err = f.Stat()
		if msg.err != nil {
			return msg
		}
		msg.reset = previous == nil || !os.SameFile(previous, msg.info) || msg.info.Size() < offset
		if msg.reset {
			offset = 0
		}
		// Read only appended bytes after opening the full log once.
		msg.data, msg.err = io.ReadAll(io.NewSectionReader(f, offset, msg.info.Size()-offset))
		msg.size = offset + int64(len(msg.data))
		return msg
	}
}

func (m *logsModel) openLog(mode logsViewMode, path string) tea.Cmd {
	m.mode, m.logPath = mode, path
	m.logRows, m.logMatches = nil, nil
	m.logOffset, m.logColumn, m.logMatch = 0, 0, -1
	m.logFollow, m.logLoading, m.searching = true, false, false
	m.logSize, m.logInfo = 0, nil
	m.logError, m.logQuery, m.logInput = "", "", ""
	return m.refreshLog()
}

func (m *logsModel) refreshLog() tea.Cmd {
	if m.logLoading {
		return nil
	}
	m.logRequest++
	m.logLoading = true
	return loadJobLogCmd(m.logPath, m.logSize, m.logInfo, m.logRequest)
}

func (m *logsModel) applyLogUpdate(msg loadJobLogMsg) {
	m.logError = ""
	m.logInfo, m.logSize = msg.info, msg.size
	if msg.reset {
		m.logRows, m.logMatches = nil, nil
		m.logMatch = -1
	}
	if len(msg.data) > 0 {
		start := max(0, len(m.logRows)-1)
		text := string(msg.data)
		if len(m.logRows) > 0 {
			text = m.logRows[start] + text
		}
		// shortcut: open logs are kept in memory; use a file index if logs outgrow memory.
		m.logRows = append(m.logRows[:start], strings.Split(text, "\n")...)
		m.updateLogMatches(start)
	}
	m.clampLogOffset()
}

func (m logsModel) logWidth() int {
	if m.width == 0 {
		return 80
	}
	return max(1, m.width-regionFocusedStyle.GetHorizontalFrameSize())
}

func (m logsModel) logPageHeight() int {
	if m.height == 0 {
		return jobsPerPage
	}
	// Title, path, two blank lines, and status surround the visible log rows.
	return max(1, m.height-regionFocusedStyle.GetVerticalFrameSize()-5)
}

func (m *logsModel) clampLogOffset() {
	bottom := max(0, len(m.logRows)-m.logPageHeight())
	if m.logFollow {
		m.logOffset = bottom
	} else {
		m.logOffset = min(m.logOffset, bottom)
	}
}

func (m *logsModel) scrollLog(delta int) {
	bottom := max(0, len(m.logRows)-m.logPageHeight())
	m.logOffset = max(0, min(bottom, m.logOffset+delta))
	m.logFollow = m.logOffset == bottom
}

func (m logsModel) updateLogKey(key tea.KeyMsg) (logsModel, tea.Cmd, bool) {
	if key.String() == "ctrl+c" {
		return m, nil, false
	}
	if m.searching {
		switch key.String() {
		case "esc":
			m.searching = false
		case "enter":
			m.searching = false
			m.logQuery = m.logInput
			m.logMatch = -1
			m.updateLogMatches(0)
			m.findLogMatch(1)
		case "backspace", "ctrl+h":
			_, size := utf8.DecodeLastRuneInString(m.logInput)
			m.logInput = m.logInput[:len(m.logInput)-size]
		case "ctrl+u":
			m.logInput = ""
		default:
			if key.Type == tea.KeyRunes && !key.Alt {
				m.logInput += strings.Map(func(r rune) rune {
					if unicode.IsControl(r) {
						return -1
					}
					return r
				}, string(key.Runes))
			}
		}
		return m, nil, true
	}
	switch key.String() {
	case "esc", "enter", "backspace":
		m.mode = logsModeList
		m.logRows, m.logMatches = nil, nil
		return m, nil, true
	case "up":
		m.scrollLog(-1)
	case "down":
		m.scrollLog(1)
	case "ctrl+u":
		m.scrollLog(-max(1, m.logPageHeight()/2))
	case "ctrl+d":
		m.scrollLog(max(1, m.logPageHeight()/2))
	case "pgup":
		m.scrollLog(-m.logPageHeight())
	case "pgdown":
		m.scrollLog(m.logPageHeight())
	case "home", "g":
		m.logOffset, m.logFollow = 0, false
	case "end", "G":
		m.logFollow = true
		m.clampLogOffset()
	case "left":
		m.logColumn = max(0, m.logColumn-8)
	case "right":
		m.logColumn += 8
	case "/":
		m.searching, m.logInput = true, ""
	case "n":
		m.findLogMatch(1)
	case "N":
		m.findLogMatch(-1)
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m *logsModel) updateLogMatches(start int) {
	m.logMatches = m.logMatches[:sort.SearchInts(m.logMatches, start)]
	if m.logQuery != "" {
		pattern := regexp.MustCompile("(?i)" + regexp.QuoteMeta(m.logQuery))
		for i := start; i < len(m.logRows); i++ {
			if pattern.MatchString(ansi.Strip(m.logRows[i])) {
				m.logMatches = append(m.logMatches, i)
			}
		}
	}
	m.logMatch = min(m.logMatch, len(m.logMatches)-1)
}

func (m *logsModel) findLogMatch(direction int) {
	if len(m.logMatches) == 0 {
		return
	}
	if m.logMatch < 0 {
		m.logMatch = sort.SearchInts(m.logMatches, m.logOffset)
		if direction < 0 {
			m.logMatch--
		}
	} else {
		m.logMatch += direction
	}
	m.logMatch = (m.logMatch + len(m.logMatches)) % len(m.logMatches)
	row := m.logMatches[m.logMatch]
	m.logOffset, m.logFollow = row, false
	m.clampLogOffset()
	text := ansi.Strip(m.logRows[row])
	pattern := regexp.MustCompile("(?i)" + regexp.QuoteMeta(m.logQuery))
	match := pattern.FindStringIndex(text)
	m.logColumn = 0
	if match != nil {
		column := ansi.StringWidth(text[:match[0]])
		if column >= m.logWidth() {
			m.logColumn = column
		}
	}
}

func (m logsModel) logFrame(body string) string {
	title := "Job Log"
	if m.mode == logsModeCI {
		title = "CI Activity"
	}
	end := min(len(m.logRows), m.logOffset+m.logPageHeight())
	position := fmt.Sprintf("Lines %d–%d/%d · paused", min(len(m.logRows), m.logOffset+1), end, len(m.logRows))
	if m.logFollow {
		position = strings.TrimSuffix(position, "paused") + "following"
	}
	if m.searching {
		position = ansi.TruncateLeft("/"+m.logInput+"▏", m.logWidth(), "…")
	} else if m.logQuery != "" {
		if len(m.logMatches) == 0 {
			position = fmt.Sprintf("/%s · no matches · %s", m.logQuery, position)
		} else {
			position = fmt.Sprintf("/%s · match %d/%d · %s", m.logQuery, m.logMatch+1, len(m.logMatches), position)
		}
	}
	if !m.searching && m.statusInErr && m.statusMsg != "" {
		position = m.statusMsg
	}
	if !m.searching && m.logError != "" {
		position = m.logError
	}
	content := strings.Join([]string{
		sectionTitleStyle.Render(title),
		mutedStyle.Render(ansi.Truncate("path="+m.logPath, m.logWidth(), "…")),
		"", body, "",
		mutedStyle.Render(ansi.Truncate(position, m.logWidth(), "…")),
	}, "\n")
	return regionFocusedStyle.Render(content)
}

func (m logsModel) renderLogDetail() string {
	height := m.logPageHeight()
	end := min(len(m.logRows), m.logOffset+height)
	lines := make([]string, 0, height)
	var pattern *regexp.Regexp
	if m.logQuery != "" {
		pattern = regexp.MustCompile("(?i)" + regexp.QuoteMeta(m.logQuery))
	}
	for row := m.logOffset; row < end; row++ {
		line := strings.ReplaceAll(strings.TrimSuffix(m.logRows[row], "\r"), "\t", "    ")
		if pattern != nil && pattern.MatchString(ansi.Strip(line)) {
			line = ansi.Strip(line)
			style := warningStyle
			if m.logMatch >= 0 && m.logMatches[m.logMatch] == row {
				style = selectedItemStyle
			}
			line = style.Render(ansi.Cut(line, m.logColumn, m.logColumn+m.logWidth()))
		} else {
			line = ansi.Cut(line, m.logColumn, m.logColumn+m.logWidth())
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, mutedStyle.Render("(empty)"))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return m.logFrame(strings.Join(lines, "\n"))
}
