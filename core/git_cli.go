package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
