package core

import (
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
)

var Root, _ = os.Getwd()

func LocalPath(path ...string) string {
	return filepath.Join(append([]string{Root}, path...)...)
}

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

func SafeIdx[T any](idx int, slice []T) (ret T) {
	if len(slice) == 0 {
		return ret
	}
	if idx >= len(slice) {
		return slice[len(slice)-1]
	}
	return slice[idx]
}

func RemoveRepos(repos []CodeRepo, name string) (ret []CodeRepo) {
	for _, repo := range repos {
		if repo.Repo != name {
			ret = append(ret, repo)
		}
	}
	return ret
}

func RemoveJobConf(confs []JobConf, repo, name string) (ret []JobConf) {
	for _, conf := range confs {
		if conf.Name != name || conf.Repo != repo {
			ret = append(ret, conf)
		}
	}
	return ret
}
