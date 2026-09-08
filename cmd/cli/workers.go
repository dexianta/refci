package main

import (
	"context"
	"dexianta/refci/core"
	"dexianta/refci/tui"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func isWorkerConfig(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".yml" && ext != ".yaml" {
		return false
	}
	if info, err := os.Stat(path); err == nil {
		return !info.IsDir()
	}
	_, mirrorPath, err := resolveRepoTarget(path)
	if err != nil {
		return true
	}
	info, err := os.Stat(mirrorPath)
	return err != nil || !info.IsDir()
}

func loadRepoRuntimeConfig(target, envPath string, monitorMode bool) (runtimeConfig, error) {
	repo, mirrorPath, err := resolveRepoTarget(target)
	if err != nil {
		return runtimeConfig{}, err
	}
	cfg := runtimeConfig{Repo: repo}
	if !monitorMode {
		cfg, err = parseRuntimeConfig(repo, envPath)
		if err != nil {
			return runtimeConfig{}, err
		}
	}
	cfg.MirrorPath = mirrorPath
	return cfg, nil
}

func loadWorkerConfigs(path string) ([]runtimeConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open worker config: %w", err)
	}
	defer f.Close()

	var entries map[string]struct {
		Repo string `yaml:"repo"`
		Env  string `yaml:"env"`
	}
	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("read worker config: %w", err)
	}
	if len(entries) == 0 {
		return nil, errors.New("worker config must contain at least one repository")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("worker config must contain a single YAML document")
	}

	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	configs := make([]runtimeConfig, 0, len(entries))
	seen := make(map[string]string, len(entries))
	for _, name := range names {
		entry := entries[name]
		if strings.TrimSpace(name) == "" || strings.TrimSpace(entry.Repo) == "" || strings.TrimSpace(entry.Env) == "" {
			return nil, fmt.Errorf("worker %q requires a name, repo, and env", name)
		}
		cfg, err := loadRepoRuntimeConfig(entry.Repo, entry.Env, false)
		if err != nil {
			return nil, fmt.Errorf("worker %q: %w", name, err)
		}
		if previous, ok := seen[cfg.Repo]; ok {
			return nil, fmt.Errorf("workers %q and %q target the same repository %s", previous, name, cfg.Repo)
		}
		seen[cfg.Repo] = name
		configs = append(configs, cfg)
	}
	return configs, nil
}

type repoWorker struct {
	rerunCh  chan tui.RerunRequest
	cancelCh chan tui.CancelRequest
	done     chan struct{}
}

func reportWorkerStatus(ctx context.Context, statusCh chan<- tui.StatusEvent, repo, msg string, isErr bool) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case statusCh <- tui.StatusEvent{Message: repo + ": " + msg, IsError: isErr}:
		return true
	default:
		return false
	}
}

func routeWorkerRequests(ctx context.Context, workers map[string]*repoWorker, statusCh chan<- tui.StatusEvent, rerunCh <-chan tui.RerunRequest, cancelCh <-chan tui.CancelRequest) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-rerunCh:
			worker := workers[req.Repo]
			if worker == nil {
				reportWorkerStatus(ctx, statusCh, req.Repo, "repository is not running in this config", true)
				continue
			}
			select {
			case worker.rerunCh <- req:
			default:
				reportWorkerStatus(ctx, statusCh, req.Repo, "restart queue is full; try again", true)
			}
		case req := <-cancelCh:
			worker := workers[req.Repo]
			if worker == nil {
				reportWorkerStatus(ctx, statusCh, req.Repo, "repository is not running in this config", true)
				continue
			}
			select {
			case worker.cancelCh <- req:
			default:
				reportWorkerStatus(ctx, statusCh, req.Repo, "cancel queue is full; try again", true)
			}
		}
	}
}

func startRepoWorker(ctx context.Context, dbRepo core.DbRepo, cfg runtimeConfig, interval time.Duration, monitorMode bool, statusCh chan<- tui.StatusEvent) (*repoWorker, error) {
	ciLogger, err := core.NewCIActivityLogger(cfg.Repo)
	if err != nil {
		return nil, err
	}
	runner := core.NewJobRunner(dbRepo)
	runner.SetLogger(ciLogger.Logf)
	rerunCh := make(chan tui.RerunRequest, 8)
	cancelCh := make(chan tui.CancelRequest, 8)
	done := make(chan struct{})
	reportStatus := func(msg string, isErr bool) bool {
		if msg == "" {
			msg = "poll recovered"
		}
		return reportWorkerStatus(ctx, statusCh, cfg.Repo, msg, isErr)
	}

	if !monitorMode {
		staleCount, err := markRepoJobsCanceled(dbRepo, cfg.Repo, "worker restarted before job completion", ciLogger.Logf)
		if err != nil {
			return nil, err
		}
		if staleCount > 0 {
			reportStatus(fmt.Sprintf("marked %d stale jobs as canceled", staleCount), false)
		}
	}

	modeLabel := "poll"
	if monitorMode {
		modeLabel = "monitor"
	}
	ciLogger.Logf("worker start repo=%s mode=%s interval=%s", cfg.Repo, modeLabel, interval.String())

	go func() {
		defer close(done)

		doPoll := func() {}
		var ticker *time.Ticker
		var tickerCh <-chan time.Time
		lastErr := ""

		if !monitorMode {
			doPoll = func() {
				if ctx.Err() != nil {
					return
				}
				var loopErr error
				started := time.Now()
				ciLogger.Logf("poll tick start")

				fetchStarted := time.Now()
				ciLogger.Logf("fetch mirror start path=%s", cfg.MirrorPath)
				if err := fetchMirror(ctx, cfg.MirrorPath); err != nil {
					ciLogger.Logf("fetch mirror failed after %s: %v", time.Since(fetchStarted).Round(time.Millisecond), err)
					loopErr = fmt.Errorf("fetch mirror: %w", err)
				} else {
					ciLogger.Logf("fetch mirror done in %s", time.Since(fetchStarted).Round(time.Millisecond))
					loadStarted := time.Now()
					ciLogger.Logf("load job config start ref=HEAD")
					jobs, err := core.LoadJobConfsFromRepo(ctx, cfg.Repo, "HEAD")
					if err != nil {
						ciLogger.Logf("load job config failed after %s: %v", time.Since(loadStarted).Round(time.Millisecond), err)
						loopErr = fmt.Errorf("load .refci/conf.yml: %w", err)
					} else if len(jobs) == 0 {
						ciLogger.Logf("load job config done in %s count=0", time.Since(loadStarted).Round(time.Millisecond))
						loopErr = fmt.Errorf("no jobs found in .refci/conf.yml for %s", cfg.Repo)
					} else {
						ciLogger.Logf("load job config done in %s count=%d", time.Since(loadStarted).Round(time.Millisecond), len(jobs))
						if err := pollOnce(ctx, dbRepo, runner, cfg, jobs, ciLogger.Logf); err != nil {
							loopErr = fmt.Errorf("poll failed: %w", err)
						}
					}
				}

				if loopErr != nil {
					msg := loopErr.Error()
					ciLogger.Logf("poll tick failed after %s: %v", time.Since(started).Round(time.Millisecond), loopErr)
					if msg != lastErr && reportStatus(msg+" (will retry)", true) {
						lastErr = msg
					}
				} else if lastErr != "" {
					// clear previously shown transient error once poll succeeds again
					ciLogger.Logf("poll tick recovered in %s", time.Since(started).Round(time.Millisecond))
					if reportStatus("", false) {
						lastErr = ""
					}
				} else {
					ciLogger.Logf("poll tick done in %s", time.Since(started).Round(time.Millisecond))
				}
			}

			doPoll()
			ticker = time.NewTicker(interval)
			tickerCh = ticker.C
			defer ticker.Stop()
		}

		for {
			select {
			case <-ctx.Done():
				runner.Stop()
				if !monitorMode {
					if count, err := markRepoJobsCanceled(dbRepo, cfg.Repo, "worker stopped before job completion", ciLogger.Logf); err != nil {
						ciLogger.Logf("worker stop cleanup failed: %v", err)
					} else if count > 0 {
						ciLogger.Logf("worker stop cleanup marked=%d", count)
					}
				}
				ciLogger.Logf("worker stop")
				return
			case req := <-rerunCh:
				ciLogger.Logf("rerun requested job=%s branch=%s sha=%s", req.Name, req.Branch, shortSHA(req.SHA))
				if err := rerunJob(ctx, dbRepo, runner, cfg, req); err != nil {
					ciLogger.Logf("rerun failed job=%s branch=%s sha=%s: %v", req.Name, req.Branch, shortSHA(req.SHA), err)
					reportStatus(fmt.Sprintf("restart failed for %s/%s: %v", req.Name, req.Branch, err), true)
					continue
				}
				ciLogger.Logf("rerun started job=%s branch=%s sha=%s", req.Name, req.Branch, shortSHA(req.SHA))
				reportStatus(fmt.Sprintf("restart started for %s/%s", req.Name, req.Branch), false)
			case req := <-cancelCh:
				ciLogger.Logf("cancel requested job=%s branch=%s sha=%s", req.Name, req.Branch, shortSHA(req.SHA))
				if err := cancelJob(ctx, dbRepo, runner, req); err != nil {
					ciLogger.Logf("cancel failed job=%s branch=%s sha=%s: %v", req.Name, req.Branch, shortSHA(req.SHA), err)
					reportStatus(fmt.Sprintf("cancel failed for %s/%s@%s: %v", req.Name, req.Branch, shortSHA(req.SHA), err), true)
					continue
				}
				ciLogger.Logf("cancel accepted job=%s branch=%s sha=%s", req.Name, req.Branch, shortSHA(req.SHA))
				reportStatus(fmt.Sprintf("cancel requested for %s/%s@%s", req.Name, req.Branch, shortSHA(req.SHA)), false)
			case <-tickerCh:
				doPoll()
			}
		}
	}()

	return &repoWorker{rerunCh: rerunCh, cancelCh: cancelCh, done: done}, nil
}
