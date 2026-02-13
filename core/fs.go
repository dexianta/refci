package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func InitRoot(path string) error {
	root, err := resolveRootPath(path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create root %q: %w", root, err)
	}

	for _, name := range []string{"repos", "worktrees", "logs"} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("create %s dir %q: %w", name, p, err)
		}
	}

	dbPath := filepath.Join(root, "refci.db")
	db, err := OpenDB(DBConfig{
		Kind:       DBSQLite,
		SQLitePath: dbPath,
	})
	if err != nil {
		return fmt.Errorf("open sqlite db %q: %w", dbPath, err)
	}
	defer db.Close()

	if _, err := NewSQLiteRepo(db); err != nil {
		return fmt.Errorf("init sqlite schema: %w", err)
	}

	return nil
}

func resolveRootPath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", fmt.Errorf("root path is required")
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
