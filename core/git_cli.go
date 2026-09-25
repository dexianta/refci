package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	// FetchTimeout bounds a single background fetch. Stalled connections are
	// normally detected sooner by the ssh keepalive / http low-speed settings
	// below; this is the backstop so the poll loop can never hang forever.
	FetchTimeout = 3 * time.Minute
)

const (
	// gitWaitDelay bounds how long we wait for git's output pipes to close
	// after git exits or is killed. Without it, a surviving child (ssh,
	// git-remote-https) holding the pipe makes Wait block forever.
	gitWaitDelay = 5 * time.Second
)

func CloneMirror(ctx context.Context, repoURL, dstPath string) error {
	url := strings.TrimSpace(repoURL)
	dst := strings.TrimSpace(dstPath)
	if url == "" {
		return fmt.Errorf("repo url is required")
	}
	if dst == "" {
		return fmt.Errorf("destination path is required")
	}

	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check destination %q: %w", dst, err)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create clone parent dir: %w", err)
	}

	if err := runGit(ctx, "", "clone", "--mirror", url, dst); err != nil {
		return err
	}
	return nil
}

func FetchMirror(ctx context.Context, mirrorPath string) error {
	path := strings.TrimSpace(mirrorPath)
	if path == "" {
		return fmt.Errorf("mirror path is required")
	}

	fetchCtx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	args := []string{"fetch", "--prune", "origin"}
	cmd := newGitCmd(fetchCtx, path, args...)
	setNonInteractive(cmd)
	_, err := execGit(cmd, args)
	if err != nil && errors.Is(fetchCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("git fetch timed out after %s: %w", FetchTimeout, err)
	}
	return err
}

// EnsureWorktree checks out sha in a worktree owned by one job on one branch,
// so jobs sharing a branch never reset each other's checkout.
func EnsureWorktree(ctx context.Context, repo, name, branch, sha string) (string, error) {
	mirrorPath := MirrorPath(repo)
	worktreePath := filepath.Join(Root, "worktrees", ToLocalRepo(strings.TrimSpace(repo)), sanitizePathToken(name), sanitizePathToken(branch))
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", fmt.Errorf("create worktree parent dir: %w", err)
	}

	shaValue := strings.TrimSpace(sha)
	// Only reset a directory that is really a worktree: without its .git
	// file, git would walk up and act on whatever repo encloses the root.
	if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err == nil {
		if err := runGit(ctx, worktreePath, "reset", "--hard", shaValue); err == nil {
			return worktreePath, nil
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

	// Missing or damaged (interrupted add, re-cloned mirror, ...): recreate it.
	if err := os.RemoveAll(worktreePath); err != nil {
		return "", fmt.Errorf("remove worktree %q: %w", worktreePath, err)
	}
	if err := runGit(ctx, mirrorPath, "worktree", "prune"); err != nil {
		return "", err
	}
	if err := runGit(ctx, mirrorPath, "worktree", "add", "--detach", worktreePath, shaValue); err != nil {
		return "", err
	}
	return worktreePath, nil
}

func ListBranchHeads(ctx context.Context, mirrorPath string) (map[string]string, error) {
	path := strings.TrimSpace(mirrorPath)
	if path == "" {
		return nil, fmt.Errorf("mirror path is required")
	}

	out, err := runGitOutput(
		ctx,
		path,
		"for-each-ref",
		"refs/heads",
		"--format=%(refname:short)\t%(objectname)",
	)
	if err != nil {
		return nil, err
	}

	heads := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		row := strings.TrimSpace(line)
		if row == "" {
			continue
		}
		parts := strings.SplitN(row, "\t", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid git ref row: %q", row)
		}

		branch := strings.TrimSpace(parts[0])
		sha := strings.TrimSpace(parts[1])
		if branch == "" || sha == "" {
			return nil, fmt.Errorf("invalid git ref row: %q", row)
		}
		heads[branch] = sha
	}

	return heads, nil
}

func ListBranchHeadsByPattern(ctx context.Context, repo, branchPattern string) (map[string]string, error) {
	repoName := strings.TrimSpace(repo)
	if repoName == "" {
		return nil, fmt.Errorf("repo is required")
	}

	pattern := normalizeBranchPattern(branchPattern)
	heads, err := ListBranchHeads(ctx, MirrorPath(repoName))
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	for branch, sha := range heads {
		if branchMatchesPattern(branch, pattern) {
			out[branch] = sha
		}
	}
	return out, nil
}

func ShouldRunByPathPatterns(ctx context.Context, repo, prevSHA, newSHA string, patterns []string) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}

	if newSHA == "" {
		return false, nil
	}
	if prevSHA == "" {
		return true, nil
	}
	if prevSHA == newSHA {
		return false, nil
	}

	files, err := ListChangedFiles(ctx, repo, prevSHA, newSHA)
	if err != nil {
		return false, err
	}
	for _, file := range files {
		if matchAnyPathPattern(file, patterns) {
			return true, nil
		}
	}
	return false, nil
}

func LoadJobConfsFromRepo(ctx context.Context, repo, ref string) ([]JobConf, error) {
	repoName := repo
	if repoName == "" {
		return nil, fmt.Errorf("repo is required")
	}
	rev := ref
	if rev == "" {
		rev = "HEAD"
	}

	// get the latest config with git show
	content, err := runGitOutput(ctx, MirrorPath(repoName), "show", rev+":.refci/conf.yml")
	if err != nil {
		return nil, err
	}

	confs, err := ParseJobConfs(content)
	if err != nil {
		return nil, err
	}
	for i := range confs {
		confs[i].Repo = repoName
	}
	return confs, nil
}

func CommitAuthorAtSHA(ctx context.Context, repo, sha string) (string, error) {
	repoName := strings.TrimSpace(repo)
	if repoName == "" {
		return "", fmt.Errorf("repo is required")
	}
	shaValue := strings.TrimSpace(sha)
	if shaValue == "" {
		return "", fmt.Errorf("sha is required")
	}

	out, err := runGitOutput(ctx, MirrorPath(repoName), "show", "-s", "--format=%an", shaValue)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func ListChangedFiles(ctx context.Context, repo, oldSHA, newSHA string) ([]string, error) {
	if repo == "" {
		return nil, fmt.Errorf("repo is required")
	}
	if oldSHA == "" || newSHA == "" {
		return nil, nil
	}

	out, err := runGitOutput(ctx, MirrorPath(repo), "diff", "--name-only", oldSHA, newSHA)
	if err != nil {
		return nil, err
	}

	var files []string
	for _, file := range strings.Split(out, "\n") {
		if file != "" {
			files = append(files, file)
		}
	}
	return files, nil
}

func normalizeBranchPattern(pattern string) string {
	p := strings.TrimSpace(pattern)
	p = strings.TrimPrefix(p, "refs/heads/")
	if p == "" {
		return "*"
	}
	return p
}

func branchMatchesPattern(branch, pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(branch, prefix)
	}
	return branch == pattern
}

func matchAnyPathPattern(file string, patterns []string) bool {
	target := normalizeRepoRelPath(file)
	for _, p := range patterns {
		if matchPathPattern(normalizeRepoRelPath(p), target) {
			return true
		}
	}
	return false
}

func normalizeRepoRelPath(v string) string {
	s := v
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimPrefix(s, "/")
	s = strings.ReplaceAll(s, "\\", "/")
	return s
}

func matchPathPattern(pattern, target string) bool {
	ps := splitPathParts(pattern)
	ts := splitPathParts(target)
	return matchPathParts(ps, ts)
}

func splitPathParts(v string) []string {
	if v == "" {
		return nil
	}
	raw := strings.Split(v, "/")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func matchPathParts(patternParts, targetParts []string) bool {
	if len(patternParts) == 0 {
		return len(targetParts) == 0
	}

	head := patternParts[0]
	if head == "**" {
		if matchPathParts(patternParts[1:], targetParts) {
			return true
		}
		for i := range targetParts {
			if matchPathParts(patternParts[1:], targetParts[i+1:]) {
				return true
			}
		}
		return false
	}

	if len(targetParts) == 0 {
		return false
	}

	ok, err := path.Match(head, targetParts[0])
	if err != nil || !ok {
		return false
	}
	return matchPathParts(patternParts[1:], targetParts[1:])
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := runGitOutput(ctx, dir, args...)
	return err
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	return execGit(newGitCmd(ctx, dir, args...), args)
}

func newGitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	cmd.WaitDelay = gitWaitDelay
	return cmd
}

// setNonInteractive prepares a network git command for unattended use: it
// fails instead of prompting (a prompt on /dev/tty would block forever behind
// the TUI), drops dead connections, and kills the whole process group
// (git + ssh/remote helper) on cancel or timeout.
func setNonInteractive(cmd *exec.Cmd) {
	env := append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		// abort https transfers slower than 1KB/s for 60s
		"GIT_HTTP_LOW_SPEED_LIMIT=1000",
		"GIT_HTTP_LOW_SPEED_TIME=60",
	)
	if os.Getenv("GIT_SSH_COMMAND") == "" && os.Getenv("GIT_SSH") == "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=30 -o ServerAliveInterval=15 -o ServerAliveCountMax=3")
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return signalProcess(cmd.Process.Pid, syscall.SIGKILL)
	}
}

func execGit(cmd *exec.Cmd, args []string) (string, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
