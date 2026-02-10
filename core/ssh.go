package core

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SSHEntry struct {
	Key   string
	Value string
}

type SSHHost struct {
	Pattern string
	Entries []SSHEntry
}

// LoadSSHHosts parses ssh config entries under Host blocks.
// It ignores global directives defined before the first Host line.
func LoadSSHHosts(path string) ([]SSHHost, error) {
	resolved, err := resolveSSHConfigPath(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, err
	}

	var hosts []SSHHost
	var current *SSHHost

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		key := parts[0]
		value := strings.TrimSpace(line[len(key):])
		if value == "" {
			continue
		}

		if strings.EqualFold(key, "Host") {
			hosts = append(hosts, SSHHost{Pattern: value})
			current = &hosts[len(hosts)-1]
			continue
		}

		if current == nil {
			continue
		}

		current.Entries = append(current.Entries, SSHEntry{
			Key:   key,
			Value: value,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return hosts, nil
}

func resolveSSHConfigPath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		p = "~/.ssh/config"
	}

	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}

	return p, nil
}
