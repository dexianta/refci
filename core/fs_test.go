package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListLocalRepos(t *testing.T) {
	oldRoot := Root
	Root = t.TempDir()
	defer func() {
		Root = oldRoot
	}()

	reposDir := filepath.Join(Root, "repos")
	if err := os.MkdirAll(filepath.Join(reposDir, "acme--api"), 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(reposDir, "acme--web"), 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reposDir, "README"), []byte("ignore"), 0o644); err != nil {
		t.Fatalf("create file: %v", err)
	}

	repos, err := ListLocalRepos()
	if err != nil {
		t.Fatalf("ListLocalRepos() error = %v", err)
	}

	want := []string{"acme/api", "acme/web"}
	if len(repos) != len(want) {
		t.Fatalf("ListLocalRepos() len = %d, want %d: %#v", len(repos), len(want), repos)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Fatalf("ListLocalRepos()[%d] = %q, want %q", i, repos[i], want[i])
		}
	}
}
