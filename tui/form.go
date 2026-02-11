package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type formMode int

const (
	formModeBrowse formMode = iota
	formModeEditValue
	formModeEditKey
	formModeAddKey
	formModeAddValue
)

type form struct {
	idx        int
	valueWidth int
	kvs        []KV
	editable   bool

	mode     formMode
	input    string
	draftKey string
	addKV    *KV
	delKV    *KV

	statusMessage string
	statusIsError bool
}

func newForm(kvs []KV, valueWidth int, editable bool) form {
	return form{
		kvs:        kvs,
		valueWidth: valueWidth,
		editable:   editable,
	}
}

func (f form) Update(msg tea.KeyMsg) form {
	if f.mode != formModeBrowse {
		return f.updateInputMode(msg)
	}

	switch msg.String() {
	case "up", "k":
		if len(f.kvs) > 0 {
			f.idx = modIdx(f.idx, len(f.kvs), -1)
		}
	case "down", "j":
		if len(f.kvs) > 0 {
			f.idx = modIdx(f.idx, len(f.kvs), 1)
		}
	case "enter":
		if len(f.kvs) > 0 {
			f.mode = formModeEditValue
			f.input = f.kvs[f.idx].val
		}
	case "e":
		if f.editable && len(f.kvs) > 0 {
			f.mode = formModeEditKey
			f.input = f.kvs[f.idx].key
		}
	case "a":
		if f.editable {
			f.mode = formModeAddKey
			f.input = ""
			f.draftKey = ""
		}
	case "d":
		if f.editable {
			f = f.deleteAtSelection()
		}
	}
	return f
}

func (f form) View() string {
	var text = []string{}
	for i, pair := range f.kvs {
		rowFocused := i == f.idx && f.mode == formModeBrowse
		label := pair.key
		value := pair.val

		if i == f.idx {
			switch f.mode {
			case formModeEditKey:
				label = f.input + "█"
			case formModeEditValue:
				value = f.input + "█"
			}
		}

		text = append(text, renderLabelInputRow(label, value, rowFocused, f.valueWidth))
	}

	if len(f.kvs) == 0 {
		text = append(text, mutedStyle.Render("No entries yet."))
	}

	if f.mode == formModeAddKey || f.mode == formModeAddValue {
		addLabel := "new key"
		addValue := ""
		if f.mode == formModeAddKey {
			addLabel = f.input + "█"
		}
		if f.mode == formModeAddValue {
			addLabel = f.draftKey
			addValue = f.input + "█"
		}
		text = append(text, renderLabelInputRow(addLabel, addValue, true, f.valueWidth))
	}

	text = append(text, mutedStyle.Render(f.helpText()))

	if f.statusMessage != "" {
		if f.statusIsError {
			text = append(text, errorStyle.Render(f.statusMessage))
		} else {
			text = append(text, successStyle.Render(f.statusMessage))
		}
	}

	return strings.Join(text, "\n\n")
}

func (f form) IsEditing() bool {
	return f.mode != formModeBrowse
}

func (f form) helpText() string {
	if f.mode == formModeBrowse {
		if f.editable {
			return "[j/k]move [e]dit [k]ey [a]dd [d]elete"
		}
		return "[j/k]move [e]dit"
	}

	switch f.mode {
	case formModeEditKey:
		return "Editing key: [enter]save [esc]cancel"
	case formModeEditValue:
		return "Editing value: [enter]save [esc]cancel"
	case formModeAddKey:
		return "Adding entry key: [enter]to val [esc]cancel"
	case formModeAddValue:
		return "Adding entry value: [enter]save [esc]cancel"
	default:
		return ""
	}
}

func (f form) updateInputMode(msg tea.KeyMsg) form {
	switch msg.String() {
	case "esc":
		f.mode = formModeBrowse
		f.input = ""
		f.draftKey = ""
		return f
	case "enter":
		return f.commitInput()
	case "backspace":
		if len(f.input) > 0 {
			runes := []rune(f.input)
			f.input = string(runes[:len(runes)-1])
		}
		return f
	}

	if len(msg.Runes) > 0 {
		f.input += string(msg.Runes)
	}
	return f
}

func (f form) commitInput() form {
	switch f.mode {
	case formModeEditValue:
		if len(f.kvs) == 0 {
			return f
		}
		sanitized, err := sanitizeValueForType(f.kvs[f.idx].dtype, f.input)
		if err != nil {
			f.statusMessage = fmt.Sprintf("Invalid value for %s: %v", f.kvs[f.idx].key, err)
			f.statusIsError = true
			return f
		}
		f.kvs[f.idx].val = sanitized
		f.statusMessage = fmt.Sprintf("Updated value: %s", f.kvs[f.idx].key)
		f.statusIsError = false
		f.mode = formModeBrowse
		f.input = ""
		f.addKV = &f.kvs[f.idx]
	case formModeEditKey:
		if len(f.kvs) == 0 {
			return f
		}
		key := strings.TrimSpace(f.input)
		if key == "" {
			f.statusMessage = "Key cannot be empty."
			f.statusIsError = true
			return f
		}
		if f.hasDuplicateKey(key, f.idx) {
			f.statusMessage = "Key already exists."
			f.statusIsError = true
			return f
		}
		f.kvs[f.idx].key = key
		f.statusMessage = "Updated key."
		f.statusIsError = false
		f.mode = formModeBrowse
		f.input = ""
		f.addKV = &f.kvs[f.idx]

	case formModeAddKey:
		key := strings.TrimSpace(f.input)
		if key == "" {
			f.statusMessage = "Key cannot be empty."
			f.statusIsError = true
			return f
		}
		if f.hasDuplicateKey(key, -1) {
			f.statusMessage = "Key already exists."
			f.statusIsError = true
			return f
		}
		f.draftKey = key
		f.input = ""
		f.mode = formModeAddValue
		f.statusMessage = ""
		f.statusIsError = false

	case formModeAddValue:
		dtype := "string"
		sanitized, err := sanitizeValueForType(dtype, f.input)
		if err != nil {
			f.statusMessage = fmt.Sprintf("Invalid value: %v", err)
			f.statusIsError = true
			return f
		}
		f.kvs = append(f.kvs, KV{
			key:   f.draftKey,
			val:   sanitized,
			dtype: dtype,
		})
		f.idx = len(f.kvs) - 1
		f.mode = formModeBrowse
		f.statusMessage = "Added new entry."
		f.statusIsError = false
		f.input = ""
		f.draftKey = ""
		f.addKV = &f.kvs[f.idx]
	}
	return f
}

func (f form) hasDuplicateKey(key string, exceptIdx int) bool {
	for i, kv := range f.kvs {
		if i == exceptIdx {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(kv.key), strings.TrimSpace(key)) {
			return true
		}
	}
	return false
}

func (f form) deleteAtSelection() form {
	if len(f.kvs) == 0 {
		f.statusMessage = "No entry to delete."
		f.statusIsError = true
		return f
	}

	deleted := f.kvs[f.idx].key
	f.kvs = append(f.kvs[:f.idx], f.kvs[f.idx+1:]...)
	if f.idx >= len(f.kvs) && f.idx > 0 {
		f.idx--
	}
	f.statusMessage = fmt.Sprintf("Deleted: %s", deleted)
	f.statusIsError = false
	f.delKV = &f.kvs[f.idx]
	return f
}

func sanitizeValueForType(dtype, raw string) (string, error) {
	kind := strings.ToLower(strings.TrimSpace(dtype))
	switch kind {
	case "", "string":
		return raw, nil
	case "int", "integer", "number":
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return "", fmt.Errorf("must be an integer")
		}
		return strconv.Itoa(n), nil
	case "bool", "boolean":
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "1", "yes", "y", "on":
			return "true", nil
		case "false", "0", "no", "n", "off":
			return "false", nil
		default:
			return "", fmt.Errorf("must be true/false")
		}
	case "date":
		s := strings.TrimSpace(raw)
		if s == "" {
			return "", fmt.Errorf("date cannot be empty")
		}
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t.Format("2006-01-02"), nil
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Format(time.RFC3339), nil
		}
		return "", fmt.Errorf("must be YYYY-MM-DD or RFC3339")
	default:
		return raw, nil
	}
}
