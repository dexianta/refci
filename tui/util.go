package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func renderHint(key, desc string) string {
	return keycapStyle.Render(key) + " " + desc
}

func modIdx(idx, mod, delta int) int {
	if mod == 0 {
		return 0
	}
	if delta > 0 {
		delta = 1
	}
	if delta < 0 {
		delta = -1
	}

	if idx+delta >= mod {
		return (idx + delta) % mod
	}

	if idx+delta < 0 {
		//
		return mod + delta
	}
	return idx + delta
}

func renderLabelInputRow(label, value string, focused bool, width int) string {
	display := value
	if strings.TrimSpace(display) == "" {
		display = placeholderStyle.Render("not set")
	}

	prefix := "  "
	v := valueStyle.Width(width)

	if focused {
		prefix = "▶ "
		v = valueFocus.Width(width)
	}

	left := labelStyle.Render(label + ":")
	right := v.Render(display)

	return prefix + lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}
