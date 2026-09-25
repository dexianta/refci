package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFetchMirrorTimesOutOnHangingRemote(t *testing.T) {
	mirror := t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", mirror).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", mirror, "remote", "add", "origin", "ssh://git@example.invalid/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}

	// simulate a transport that connects but never responds
	pidFile := filepath.Join(t.TempDir(), "ssh.pid")
	t.Setenv("GIT_SSH_COMMAND", "echo $$ > "+pidFile+"; exec sleep 600 #")
	prev := FetchTimeout
	FetchTimeout = 500 * time.Millisecond
	t.Cleanup(func() { FetchTimeout = prev })

	start := time.Now()
	err := FetchMirror(context.Background(), mirror)
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("fetch returned after %s; expected it to be killed promptly", elapsed)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read ssh pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse ssh pid: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("ssh transport process %d outlived the fetch", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestEnsureWorktreeRecreatesBrokenWorktree(t *testing.T) {
	oldRoot := Root
	Root = t.TempDir()
	t.Cleanup(func() { Root = oldRoot })

	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	source := filepath.Join(Root, "src")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(source, "init", "-b", "main")
	git(source, "add", ".")
	git(source, "commit", "-m", "init")
	sha := git(source, "rev-parse", "HEAD")
	git(Root, "clone", "--mirror", source, MirrorPath("acme/app"))

	wt, err := EnsureWorktree(context.Background(), "acme/app", "build", "main", sha)
	if err != nil {
		t.Fatal(err)
	}
	// simulate an interrupted add: the directory exists but is not a worktree
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureWorktree(context.Background(), "acme/app", "build", "main", sha); err != nil {
		t.Fatalf("broken worktree was not recreated: %v", err)
	}
	if got := git(wt, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("worktree HEAD = %s, want %s", got, sha)
	}
}
