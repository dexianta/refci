package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

func ListLocalRepos() ([]string, error) {
	reposDir := LocalPath("repos")
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		return nil, fmt.Errorf("list repos dir %q: %w", reposDir, err)
	}

	repos := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		repos = append(repos, strings.ReplaceAll(name, "--", "/"))
	}
	sort.Strings(repos)
	return repos, nil
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
