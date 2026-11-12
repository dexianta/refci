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
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
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
	rerunCh  chan tui.JobRequest
	cancelCh chan tui.JobRequest
	done     chan struct{}
	stop     context.CancelFunc
	config   atomic.Pointer[runtimeConfig]
}

const configReloadInterval = 3 * time.Second

// Stage additions before changing existing workers. If any addition cannot start,
// release its resources and leave the previous configuration in effect.
func reloadRepoWorkers(ctx context.Context, dbRepo core.DbRepo, workers map[string]*repoWorker, path string, interval time.Duration, statusCh chan<- tui.StatusEvent) (bool, error) {
	configs, err := loadWorkerConfigs(path)
	if err != nil {
		return false, err
	}
	ready := make(chan struct{})
	added := make(map[string]*repoWorker)
	applied := false
	defer func() {
		if !applied {
			for _, worker := range added {
				worker.stop()
			}
			for _, worker := range added {
				<-worker.done
			}
		}
	}()
	for _, cfg := range configs {
		if workers[cfg.Repo] != nil {
			continue
		}
		worker, err := prepareRepoWorker(ctx, dbRepo, cfg, interval, false, statusCh, ready)
		if err != nil {
			return false, fmt.Errorf("%s: %w", cfg.Repo, err)
		}
		added[cfg.Repo] = worker
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	changed := len(added) > 0
	wanted := make(map[string]bool, len(configs))
	for _, cfg := range configs {
		wanted[cfg.Repo] = true
		worker := workers[cfg.Repo]
		if worker == nil {
			workers[cfg.Repo] = added[cfg.Repo]
			continue
		}
		previous := worker.config.Load()
		if previous.MirrorPath != cfg.MirrorPath || !slices.Equal(previous.Env, cfg.Env) {
			worker.config.Store(&cfg)
			changed = true
		}
	}
	for repo, worker := range workers {
		if !wanted[repo] {
			worker.stop()
		}
	}
	for repo, worker := range workers {
		if !wanted[repo] {
			<-worker.done
			delete(workers, repo)
			changed = true
		}
	}
	applied = true
	close(ready)
	return changed, nil
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

// This loop owns the worker map, serializing config changes with UI requests.
func routeWorkerRequests(ctx context.Context, workers map[string]*repoWorker, statusCh chan<- tui.StatusEvent, rerunCh <-chan tui.JobRequest, cancelCh <-chan tui.JobRequest, dbRepo core.DbRepo, configPath string, interval time.Duration) {
	var reloadCh <-chan time.Time
	if configPath != "" {
		ticker := time.NewTicker(configReloadInterval)
		defer ticker.Stop()
		reloadCh = ticker.C
	}
	lastReloadError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-reloadCh:
			changed, err := reloadRepoWorkers(ctx, dbRepo, workers, configPath, interval, statusCh)
			if err != nil {
				if msg := err.Error(); msg != lastReloadError && reportWorkerStatus(ctx, statusCh, "config", "reload failed; keeping previous configuration: "+msg, true) {
					lastReloadError = msg
				}
			} else if changed || lastReloadError != "" {
				if reportWorkerStatus(ctx, statusCh, "config", "configuration reloaded", false) {
					lastReloadError = ""
				}
			}
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

// lockRepoWorker takes an exclusive lock on logs/<repo>/worker.lock; it is
// released when the returned file is closed or the process exits.
func lockRepoWorker(repo string) (*os.File, error) {
	dir := filepath.Dir(core.CIActivityLogPath(repo))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create worker lock dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "worker.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open worker lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another refci worker is already polling %s; stop it first", repo)
		}
		return nil, fmt.Errorf("lock worker: %w", err)
	}
	return f, nil
}

func startRepoWorker(ctx context.Context, dbRepo core.DbRepo, cfg runtimeConfig, interval time.Duration, monitorMode bool, statusCh chan<- tui.StatusEvent) (*repoWorker, error) {
	return prepareRepoWorker(ctx, dbRepo, cfg, interval, monitorMode, statusCh, nil)
}

func prepareRepoWorker(ctx context.Context, dbRepo core.DbRepo, cfg runtimeConfig, interval time.Duration, monitorMode bool, statusCh chan<- tui.StatusEvent, ready <-chan struct{}) (*repoWorker, error) {
	ciLogger, err := core.NewCIActivityLogger(cfg.Repo)
	if err != nil {
		return nil, err
	}
	runner := core.NewJobRunner(dbRepo)
	runner.SetLogger(ciLogger.Logf)
	rerunCh := make(chan tui.JobRequest, 8)
	cancelCh := make(chan tui.JobRequest, 8)
	done := make(chan struct{})
	reportStatus := func(msg string, isErr bool) bool {
		if msg == "" {
			msg = "poll recovered"
		}
		return reportWorkerStatus(ctx, statusCh, cfg.Repo, msg, isErr)
	}

	var lock *os.File
	if !monitorMode {
		// Held for the worker's lifetime: a second poller for the same repo
		// would mark this one's running jobs as stale and race it on queuing.
		if lock, err = lockRepoWorker(cfg.Repo); err != nil {
			return nil, err
		}
		staleCount, err := markRepoJobsCanceled(dbRepo, cfg.Repo, "worker restarted before job completion", ciLogger.Logf)
		if err != nil {
			_ = lock.Close()
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
	ctx, stop := context.WithCancel(ctx)
	worker := &repoWorker{rerunCh: rerunCh, cancelCh: cancelCh, done: done, stop: stop}
	worker.config.Store(&cfg)
	runner.SetEnv(func() []string { return worker.config.Load().Env })

	go func() {
		defer close(done)
		defer stop()
		if lock != nil {
			defer lock.Close()
		}
		if ready != nil {
			select {
			case <-ctx.Done():
				return
			case <-ready:
			}
		}

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
				cfg := *worker.config.Load()

				// Only failures and queue decisions are logged; quiet ticks would
				// otherwise add several lines to ci.log every interval.
				if err := fetchMirror(ctx, cfg.MirrorPath); err != nil {
					ciLogger.Logf("fetch mirror failed after %s: %v", time.Since(started).Round(time.Millisecond), err)
					loopErr = fmt.Errorf("fetch mirror: %w", err)
				} else if jobs, err := core.LoadJobConfsFromRepo(ctx, cfg.Repo, "HEAD"); err != nil {
					loopErr = fmt.Errorf("load .refci/conf.yml: %w", err)
				} else if len(jobs) == 0 {
					loopErr = fmt.Errorf("no jobs found in .refci/conf.yml for %s", cfg.Repo)
				} else if err := pollOnce(ctx, runner, cfg, jobs, ciLogger.Logf); err != nil {
					loopErr = fmt.Errorf("poll failed: %w", err)
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
				ciLogger.Logf("rerun requested job=%s branch=%s sha=%s", req.Name, req.Branch, core.ShortSHA(req.SHA))
				if err := rerunJob(ctx, dbRepo, runner, *worker.config.Load(), req); err != nil {
					ciLogger.Logf("rerun failed job=%s branch=%s sha=%s: %v", req.Name, req.Branch, core.ShortSHA(req.SHA), err)
					reportStatus(fmt.Sprintf("restart failed for %s/%s: %v", req.Name, req.Branch, err), true)
					continue
				}
				ciLogger.Logf("rerun started job=%s branch=%s sha=%s", req.Name, req.Branch, core.ShortSHA(req.SHA))
				reportStatus(fmt.Sprintf("restart started for %s/%s", req.Name, req.Branch), false)
			case req := <-cancelCh:
				ciLogger.Logf("cancel requested job=%s branch=%s sha=%s", req.Name, req.Branch, core.ShortSHA(req.SHA))
				if err := cancelJob(ctx, dbRepo, runner, req); err != nil {
					ciLogger.Logf("cancel failed job=%s branch=%s sha=%s: %v", req.Name, req.Branch, core.ShortSHA(req.SHA), err)
					reportStatus(fmt.Sprintf("cancel failed for %s/%s@%s: %v", req.Name, req.Branch, core.ShortSHA(req.SHA), err), true)
					continue
				}
				ciLogger.Logf("cancel accepted job=%s branch=%s sha=%s", req.Name, req.Branch, core.ShortSHA(req.SHA))
				reportStatus(fmt.Sprintf("cancel requested for %s/%s@%s", req.Name, req.Branch, core.ShortSHA(req.SHA)), false)
			case <-tickerCh:
				doPoll()
			}
		}
	}()

	return worker, nil
}
