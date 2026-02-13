package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
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
	return runGit(ctx, path, "fetch", "--prune", "origin")
}

func EnsureWorktree(ctx context.Context, repo, branch, sha string) (string, error) {
	repoPart := ToLocalRepo(strings.TrimSpace(repo))
	mirrorPath := filepath.Join(Root, "repos", repoPart)
	branchPart := toLocalBranch(branch)
	worktreePath := filepath.Join(Root, "worktrees", repoPart, branchPart)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return "", fmt.Errorf("create worktree parent dir: %w", err)
	}

	shaValue := strings.TrimSpace(sha)
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		if err := runGit(ctx, mirrorPath, "worktree", "add", "--detach", worktreePath, shaValue); err != nil {
			return "", err
		}
		return worktreePath, nil
	} else if err != nil {
		return "", fmt.Errorf("stat worktree path: %w", err)
	}

	if err := runGit(ctx, worktreePath, "reset", "--hard", shaValue); err != nil {
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
	if strings.Contains(pattern, "*") && !strings.HasSuffix(pattern, "*") {
		return nil, fmt.Errorf("only trailing wildcard is supported: %q", branchPattern)
	}

	mirrorPath := filepath.Join(Root, "repos", ToLocalRepo(repoName))
	heads, err := ListBranchHeads(ctx, mirrorPath)
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

	mirrorPath := filepath.Join(Root, "repos", ToLocalRepo(repoName))
	content, err := runGitOutput(ctx, mirrorPath, "show", rev+":.refci/conf.yml")
	if err != nil {
		return nil, err
	}

	confs := ParseJobConfs(content)
	for i := range confs {
		confs[i].Repo = repoName
	}
	return confs, nil
}

func ListChangedFiles(ctx context.Context, repo, oldSHA, newSHA string) ([]string, error) {
	if repo == "" {
		return nil, fmt.Errorf("repo is required")
	}
	if oldSHA == "" || newSHA == "" {
		return nil, nil
	}

	mirrorPath := filepath.Join(Root, "repos", ToLocalRepo(repo))
	out, err := runGitOutput(ctx, mirrorPath, "diff", "--name-only", oldSHA, newSHA)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(out, "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		file := line
		if file == "" {
			continue
		}
		files = append(files, file)
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
		for i := 0; i < len(targetParts); i++ {
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

func toLocalBranch(branch string) string {
	s := branch
	s = strings.TrimPrefix(s, "refs/heads/")
	s = strings.TrimPrefix(s, "refs/")
	s = strings.ReplaceAll(s, "/", "--")
	s = strings.ReplaceAll(s, "\\", "--")
	s = strings.ReplaceAll(s, ":", "_")
	s = strings.ReplaceAll(s, " ", "_")
	if s == "" {
		return "default"
	}
	return s
}

func runGit(ctx context.Context, dir string, args ...string) error {
	_, err := runGitOutput(ctx, dir, args...)
	return err
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
