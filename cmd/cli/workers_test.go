package main

import (
	"context"
	"dexianta/refci/core"
	"dexianta/refci/tui"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerConfigs(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("test.env", []byte("TOKEN=one\nexport REGION='two'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, yaml, wantError string
	}{
		{"valid", "api: {repo: repos/acme--api, env: test.env}\nweb: {repo: acme--web, env: test.env}\n", ""},
		{"empty", "{}", "at least one"},
		{"missing env", "api: {repo: acme--api}", "requires"},
		{"missing repo", "api: {env: test.env}", "requires"},
		{"blank name", "' ': {repo: acme--api, env: test.env}", "requires"},
		{"unknown key", "api: {repo: acme--api, env: test.env, typo: true}", "field typo"},
		{"duplicate key", "api: {repo: acme--api, env: test.env}\napi: {repo: acme--web, env: test.env}", "already defined"},
		{"duplicate repo", "api: {repo: repos/acme--api, env: test.env}\nother: {repo: acme--api, env: test.env}", "same repository"},
		{"missing file", "api: {repo: acme--api, env: missing.env}", "open env file"},
		{"multiple documents", "api: {repo: acme--api, env: test.env}\n---\nweb: {repo: acme--web, env: test.env}", "single YAML document"},
		{"malformed", "api: [", "read worker config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile("config.yml", []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			configs, err := loadWorkerConfigs("config.yml")
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(configs) != 2 || configs[0].Repo != "acme/api" || configs[1].Repo != "acme/web" {
				t.Fatalf("unexpected configs: %+v", configs)
			}
			for _, cfg := range configs {
				if !reflect.DeepEqual(cfg.Env, []string{"TOKEN=one", "REGION=two"}) {
					t.Fatalf("unexpected env: %v", cfg.Env)
				}
				if cfg.MirrorPath != filepath.Join(core.Root, "repos", core.ToLocalRepo(cfg.Repo)) {
					t.Fatalf("unexpected mirror: %s", cfg.MirrorPath)
				}
			}
		})
	}
	if err := os.Mkdir("repo.yaml", 0o755); err != nil {
		t.Fatal(err)
	}
	if isWorkerConfig("repo.yaml") || !isWorkerConfig("config.yml") || !isWorkerConfig("missing.yaml") {
		t.Fatal("YAML files and repo directories must be distinguished")
	}
	if cfg, err := loadRepoRuntimeConfig("acme--api", "missing.env", true); err != nil || cfg.Repo != "acme/api" {
		t.Fatalf("single-repo monitor must not need an env file: %+v, %v", cfg, err)
	}
}

func TestRepoWorkersPollRerunCancelAndStop(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	oldRoot := core.Root
	t.Cleanup(func() { core.Root = oldRoot })
	if err := core.InitRoot(root); err != nil {
		t.Fatal(err)
	}
	db, dbRepo, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	var yaml strings.Builder
	for _, name := range []string{"api", "web"} {
		source := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(source, ".refci"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(source, ".refci/conf.yml"), "build:\n  branch_pattern: main\n  script: .refci/run.sh\n")
		write(filepath.Join(source, ".refci/run.sh"), "printf '%s' \"$WORKER_TOKEN\"\nexit 1\n")
		git(source, "init", "-b", "main")
		git(source, "add", ".")
		git(source, "commit", "-m", "test job")
		git(root, "clone", "--mirror", source, filepath.Join(root, "repos", "acme--"+name))
		write(name+".env", "WORKER_TOKEN="+name+"\n")
		fmt.Fprintf(&yaml, "%s: {repo: repos/acme--%s, env: %s.env}\n", name, name, name)
	}
	// A failing fetch must keep retrying without stopping either healthy worker.
	yaml.WriteString("broken: {repo: repos/acme--missing, env: api.env}\n")
	write("config.yml", yaml.String())
	configs, err := loadWorkerConfigs("config.yml")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	workers := map[string]*repoWorker{}
	statusCh := make(chan tui.StatusEvent, 64)
	rerunCh := make(chan tui.JobRequest, 8)
	cancelCh := make(chan tui.JobRequest, 8)
	routerDone := make(chan struct{})
	defer func() {
		stop()
		for _, worker := range workers {
			select {
			case <-worker.done:
			case <-time.After(10 * time.Second):
				t.Error("worker did not stop")
			}
		}
	}()
	for _, cfg := range configs {
		worker, err := startRepoWorker(ctx, dbRepo, cfg, 100*time.Millisecond, false, statusCh)
		if err != nil {
			t.Fatal(err)
		}
		workers[cfg.Repo] = worker
	}
	go func() {
		defer close(routerDone)
		routeWorkerRequests(ctx, workers, statusCh, rerunCh, cancelCh)
	}()
	defer func() { stop(); <-routerDone }()
	waitJob := func(repo string, count int, status string) core.Job {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			jobs, err := dbRepo.ListJob(core.JobFilter{Repo: repo})
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) == count && jobs[0].Status == status {
				return jobs[0]
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s did not reach %d jobs with latest status %s", repo, count, status)
		return core.Job{}
	}
	for _, name := range []string{"api", "web"} {
		job := waitJob("acme/"+name, 1, core.StatusFailed)
		body, err := os.ReadFile(job.LogPath)
		if err != nil || string(body) != name {
			t.Fatalf("%s env output = %q, error = %v", name, body, err)
		}
		rerunCh <- tui.JobRequest{RunID: job.RunID, Repo: job.Repo, Name: job.Name, Branch: job.Branch, SHA: job.SHA}
		job = waitJob(job.Repo, 2, core.StatusFailed)
		body, err = os.ReadFile(job.LogPath)
		if err != nil || string(body) != name {
			t.Fatalf("%s rerun env output = %q, error = %v", name, body, err)
		}
		source := filepath.Join(root, name)
		write(filepath.Join(source, ".refci/run.sh"), "printf '%s' \"$WORKER_TOKEN\"\nsleep 60\n")
		git(source, "add", ".")
		git(source, "commit", "-m", "long running job")
	}
	api := waitJob("acme/api", 3, core.StatusRunning)
	web := waitJob("acme/web", 3, core.StatusRunning)
	cancelCh <- tui.JobRequest{RunID: web.RunID, Repo: web.Repo, Name: web.Name, Branch: web.Branch, SHA: web.SHA}
	waitJob(web.Repo, 3, core.StatusCanceled)
	waitJob(api.Repo, 3, core.StatusRunning)
	stop()
	for _, worker := range workers {
		select {
		case <-worker.done:
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not finish shutdown")
		}
	}
	waitJob(api.Repo, 3, core.StatusCanceled)
	log, err := os.ReadFile(core.CIActivityLogPath("acme/missing"))
	if err != nil || strings.Count(string(log), "fetch mirror failed") < 2 {
		t.Fatalf("broken worker did not retry: %s, %v", log, err)
	}
}

func TestRepoWorkersRetryUndeliveredErrors(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	oldRoot := core.Root
	defer func() { core.Root = oldRoot }()
	if err := core.InitRoot(root); err != nil {
		t.Fatal(err)
	}
	db, dbRepo, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	statusCh := make(chan tui.StatusEvent, 8)
	var workers []*repoWorker
	defer func() {
		cancel()
		for _, w := range workers {
			<-w.done
		}
	}()
	for i := 0; i < 10; i++ {
		repo := fmt.Sprintf("acme/repo%d", i)
		worker, err := startRepoWorker(ctx, dbRepo, runtimeConfig{
			Repo: repo, MirrorPath: filepath.Join(root, "repos", core.ToLocalRepo(repo)),
		}, 10*time.Millisecond, false, statusCh)
		if err != nil {
			t.Fatal(err)
		}
		workers = append(workers, worker)
	}
	// No reader yet, just as during worker startup and terminal initialization.
	deadline := time.Now().Add(2 * time.Second)
	for {
		ready := true
		for i := 0; i < 10; i++ {
			log, _ := os.ReadFile(core.CIActivityLogPath(fmt.Sprintf("acme/repo%d", i)))
			ready = ready && strings.Count(string(log), "fetch mirror failed") >= 2
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("workers never reached their retry")
		}
		time.Sleep(time.Millisecond)
	}
	seen := map[string]bool{}
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case status := <-statusCh:
			if status.IsError {
				seen[strings.SplitN(status.Message, ": ", 2)[0]] = true
			}
			if len(seen) == 10 {
				return
			}
		case <-timeout.C:
			if len(seen) != 10 {
				t.Fatalf("only %d/10 worker errors reached UI, including subsequent retries", len(seen))
			}
			return
		}
	}
}

func TestWorkerConfigDetectionWithYAMLRepoNames(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	oldRoot := core.Root
	core.Root = root
	defer func() { core.Root = oldRoot }()
	for _, ext := range []string{".yml", ".yaml"} {
		name := "acme--config" + ext
		mirror := filepath.Join(root, "repos", name)
		if err := os.MkdirAll(mirror, 0755); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{name, filepath.Join("repos", name), mirror} {
			if isWorkerConfig(target) {
				t.Fatalf("repo %q misclassified as a YAML config", target)
			}
		}
		// An explicit config file still wins over a repo with the same name.
		if err := os.WriteFile(name, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if !isWorkerConfig(name) {
			t.Fatalf("config file %q misclassified as a repo", name)
		}
	}
}

func TestPollOnceRecordsPrepareFailureAndContinues(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	oldRoot := core.Root
	t.Cleanup(func() { core.Root = oldRoot })
	if err := core.InitRoot(root); err != nil {
		t.Fatal(err)
	}
	db, dbRepo, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	source := filepath.Join(root, "src")
	if err := os.MkdirAll(filepath.Join(source, ".refci"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "a-broken:\n  branch_pattern: main\n  script: .refci/missing.sh\nb-ok:\n  branch_pattern: main\n  script: .refci/ok.sh\n"
	for path, body := range map[string]string{".refci/conf.yml": conf, ".refci/ok.sh": "exit 0\n"} {
		if err := os.WriteFile(filepath.Join(source, path), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-b", "main"}, {"add", "."}, {"commit", "-m", "init"},
		{"clone", "--mirror", source, core.MirrorPath("acme/app")},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = source
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	runner := core.NewJobRunner(dbRepo)
	defer runner.Stop()
	cfg := runtimeConfig{Repo: "acme/app"}
	jobs, err := core.LoadJobConfsFromRepo(context.Background(), cfg.Repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := pollOnce(context.Background(), dbRepo, runner, cfg, jobs, nil); err == nil || !strings.Contains(err.Error(), "script not found") {
		t.Fatalf("pollOnce error = %v, want script not found", err)
	}
	broken, _ := dbRepo.LatestJobByNameBranch(cfg.Repo, "a-broken", "main")
	if broken.Status != core.StatusFailed || !strings.Contains(broken.Msg, "script not found") {
		t.Fatalf("broken job not recorded as failed: %+v", broken)
	}
	if ok, _ := dbRepo.LatestJobByNameBranch(cfg.Repo, "b-ok", "main"); ok.RunID == "" {
		t.Fatal("a failing job blocked the next job from being queued")
	}
	// the failure is recorded, so the next tick does not retry it
	if err := pollOnce(context.Background(), dbRepo, runner, cfg, jobs, nil); err != nil {
		t.Fatalf("second pollOnce error = %v", err)
	}
}

func TestRepoWorkerLockRejectsSecondPoller(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	oldRoot := core.Root
	t.Cleanup(func() { core.Root = oldRoot })
	if err := core.InitRoot(root); err != nil {
		t.Fatal(err)
	}
	db, dbRepo, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cfg := runtimeConfig{Repo: "acme/app", MirrorPath: core.MirrorPath("acme/app")}
	statusCh := make(chan tui.StatusEvent, 64)
	ctx, stop := context.WithCancel(context.Background())
	first, err := startRepoWorker(ctx, dbRepo, cfg, time.Hour, false, statusCh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := startRepoWorker(context.Background(), dbRepo, cfg, time.Hour, false, statusCh); err == nil || !strings.Contains(err.Error(), "already polling") {
		t.Fatalf("second worker error = %v, want already polling", err)
	}
	if _, err := lockRepoWorker(cfg.Repo); err == nil {
		t.Fatal("monitor cancel must see the live worker's lock")
	}
	stop()
	<-first.done
	lock, err := lockRepoWorker(cfg.Repo)
	if err != nil {
		t.Fatalf("lock not released after worker stopped: %v", err)
	}
	_ = lock.Close()
}
