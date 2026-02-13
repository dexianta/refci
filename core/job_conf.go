package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadJobConfs loads job definitions from .refci/conf.yml format.
// Supported shape:
//
//	actions:
//	  my-action:
//	    branch_pattern: "main"
//	    path_patterns:
//	      - "services/**"
//	    script: ".refci/main.sh"
//
// It also accepts action map at top-level (without "actions:").
func LoadJobConfs(path string) ([]JobConf, error) {
	confPath := strings.TrimSpace(path)
	if confPath == "" {
		confPath = filepath.Join(".refci", "conf.yml")
	}

	data, err := os.ReadFile(confPath)
	if err != nil {
		return nil, fmt.Errorf("read job conf: %w", err)
	}

	return ParseJobConfs(string(data)), nil
}

func ParseJobConfs(raw string) []JobConf {
	lines := strings.Split(raw, "\n")
	byName := map[string]*JobConf{}
	order := []string{}

	actionIndent := 0
	current := (*JobConf)(nil)
	inPathPatterns := false
	pathPatternsIndent := 0

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := leadingSpaces(line)

		if trimmed == "actions:" {
			actionIndent = indent + 2
			current = nil
			inPathPatterns = false
			continue
		}

		if after, ok := strings.CutPrefix(trimmed, "- "); ok {
			if current != nil && inPathPatterns && indent > pathPatternsIndent {
				current.PathPatterns = append(current.PathPatterns, unquoteScalar(after))
			}
			continue
		}

		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		// action_name:
		if val == "" && indent == actionIndent && key != "path_patterns" && key != "branch_pattern" && key != "script" {
			cfg := &JobConf{Name: key}
			byName[key] = cfg
			order = append(order, key)
			current = cfg
			inPathPatterns = false
			continue
		}

		if current == nil {
			continue
		}

		switch key {
		case "branch_pattern":
			current.BranchPattern = unquoteScalar(val)
			inPathPatterns = false
		case "script":
			current.ScriptPath = unquoteScalar(val)
			inPathPatterns = false
		case "path_patterns":
			if val != "" {
				current.PathPatterns = parseInlineList(val)
				inPathPatterns = false
				continue
			}
			inPathPatterns = true
			pathPatternsIndent = indent
		default:
			inPathPatterns = false
		}
	}

	out := make([]JobConf, 0, len(order))
	seen := map[string]bool{}
	for _, name := range order {
		if seen[name] {
			continue
		}
		seen[name] = true
		if cfg := byName[name]; cfg != nil {
			out = append(out, *cfg)
		}
	}

	return out
}

func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

func unquoteScalar(v string) string {
	out := strings.TrimSpace(v)
	out = strings.Trim(out, "\"")
	out = strings.Trim(out, "'")
	return out
}

func parseInlineList(v string) []string {
	s := strings.TrimSpace(v)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		if s == "" {
			return nil
		}
		return []string{unquoteScalar(s)}
	}

	body := strings.TrimSpace(s[1 : len(s)-1])
	if body == "" {
		return nil
	}

	parts := strings.Split(body, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		item := unquoteScalar(p)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
