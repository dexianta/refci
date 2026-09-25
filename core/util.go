package core

import (
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
)

var Root, _ = os.Getwd()

func ToLocalRepo(repo string) string {
	return strings.ReplaceAll(repo, "/", "--")
}

// MirrorPath returns the bare mirror location of repo under the root.
func MirrorPath(repo string) string {
	return filepath.Join(Root, "repos", ToLocalRepo(strings.TrimSpace(repo)))
}

func ShortSHA(sha string) string {
	s := strings.TrimSpace(sha)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

// sanitizePathToken makes s safe to use as a single path element.
func sanitizePathToken(s string) string {
	out := strings.TrimSpace(s)
	out = strings.ReplaceAll(out, "/", "--")
	out = strings.ReplaceAll(out, "\\", "--")
	out = strings.ReplaceAll(out, ":", "_")
	out = strings.ReplaceAll(out, " ", "_")
	return out
}

// ExpandHome expands a leading "~" to the user's home directory.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

func ParseGithubUrl(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return ""
	}

	// Direct owner/repo form.
	if !strings.Contains(raw, "://") && !strings.Contains(raw, "@") {
		return normalizeGithubRepoPath(raw)
	}

	// SCP-like SSH form: git@github.com:owner/repo(.git)
	if strings.HasPrefix(strings.ToLower(raw), "git@github.com:") {
		return normalizeGithubRepoPath(raw[len("git@github.com:"):])
	}

	u, err := neturl.Parse(raw)
	if err != nil {
		return ""
	}

	host := strings.ToLower(u.Hostname())
	if host != "github.com" {
		return ""
	}

	return normalizeGithubRepoPath(u.Path)
}

func normalizeGithubRepoPath(path string) string {
	p := strings.TrimSpace(path)
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/")
	p = strings.TrimSuffix(p, ".git")

	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return ""
	}
	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSpace(parts[1])
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}
