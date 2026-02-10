package core

import (
	neturl "net/url"
	"os"
	"strings"
)

var Root, _ = os.Getwd()

func ToLocalRepo(repo string) string {
	return strings.ReplaceAll(repo, "/", "--")
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
