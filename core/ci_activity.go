package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type CIActivityLogger struct {
	path string
	mu   sync.Mutex
}

func NewCIActivityLogger(repo string) (*CIActivityLogger, error) {
	path := CIActivityLogPath(repo)
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("ci activity log path is required")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create ci log dir %q: %w", dir, err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create ci log file %q: %w", path, err)
	}
	_ = f.Close()

	return &CIActivityLogger{path: path}, nil
}

func CIActivityLogPath(repo string) string {
	return filepath.Join(Root, "logs", ToLocalRepo(strings.TrimSpace(repo)), "ci.log")
}

func (l *CIActivityLogger) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

func (l *CIActivityLogger) Logf(format string, args ...any) {
	if l == nil {
		return
	}

	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}

	line := fmt.Sprintf("%s  %s\n", time.Now().Format("2006-01-02 15:04:05.000 Z07:00"), msg)

	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	_, _ = f.WriteString(line)
}
