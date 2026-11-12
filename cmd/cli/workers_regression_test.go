package main

import (
	"context"
	"dexianta/refci/core"
	"dexianta/refci/tui"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newWorkerTestRoot(t *testing.T) (string, core.DbRepo, func(string, string), func(string, ...string) string) {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	old := core.Root
	t.Cleanup(func() { core.Root = old })
	if err := core.InitRoot(root); err != nil {
		t.Fatal(err)
	}
	db, repo, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	return root, repo, write, git
}

func waitForWorkerTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestHistoricalSuccessRerunSurvivesPolling(t *testing.T) {
	root, dbRepo, write, git := newWorkerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, ".refci"), 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(source, ".refci/conf.yml"), "build: {branch_pattern: main, script: .refci/run.sh}\n")
	write(filepath.Join(source, ".refci/run.sh"), "while [ ! -f '"+filepath.Join(root, "release-job")+"' ]; do sleep 0.05; done\n")
	git(source, "init", "-b", "main")
	git(source, "add", ".")
	git(source, "commit", "-m", "A")
	shaA := git(source, "rev-parse", "HEAD")
	git(source, "commit", "--allow-empty", "-m", "B")
	shaB := git(source, "rev-parse", "HEAD")
	git(root, "clone", "--mirror", source, core.MirrorPath("acme/api"))
	for _, row := range []struct{ id, sha string }{{"success-A", shaA}, {"success-B", shaB}} {
		if err := dbRepo.CreateJob(row.id, "acme/api", "build", "main", row.sha, "Test"); err != nil {
			t.Fatal(err)
		}
		if err := dbRepo.UpdateJob(row.id, core.StatusFinished, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	runner := core.NewJobRunner(dbRepo)
	defer runner.Stop()
	cfg := runtimeConfig{Repo: "acme/api", MirrorPath: core.MirrorPath("acme/api")}
	if err := rerunJob(context.Background(), dbRepo, runner, cfg, tui.JobRequest{RunID: "success-A", Repo: cfg.Repo, Name: "build", Branch: "main", SHA: shaA}); err != nil {
		t.Fatal(err)
	}
	rerun, err := dbRepo.LatestJobByNameBranch(cfg.Repo, "build", "main")
	if err != nil || rerun.Status != core.StatusRunning || rerun.SHA != shaA {
		t.Fatalf("rerun: %+v, %v", rerun, err)
	}
	jobs, err := core.LoadJobConfsFromRepo(context.Background(), cfg.Repo, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if err := pollOnce(context.Background(), runner, cfg, jobs, nil); err != nil {
		t.Fatal(err)
	}
	rerun, err = dbRepo.JobByRunID(rerun.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if rerun.Status != core.StatusRunning {
		t.Fatalf("successful historical rerun was canceled by the next poll: status=%s", rerun.Status)
	}
	// Finishing the manual run must not make the unchanged head run again either.
	write(filepath.Join(root, "release-job"), "")
	waitForWorkerTest(t, func() bool {
		rerun, err = dbRepo.JobByRunID(rerun.RunID)
		return err == nil && rerun.Status == core.StatusFinished
	})
	if err := pollOnce(context.Background(), runner, cfg, jobs, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := dbRepo.ListJob(core.JobFilter{Repo: cfg.Repo})
	if err != nil || len(rows) != 3 {
		t.Fatalf("unchanged head queued another run: %+v, %v", rows, err)
	}

	// A genuine new commit still supersedes a running historical rerun.
	if err := os.Remove(filepath.Join(root, "release-job")); err != nil {
		t.Fatal(err)
	}
	if err := rerunJob(context.Background(), dbRepo, runner, cfg, tui.JobRequest{RunID: "success-A", Repo: cfg.Repo, Name: "build", Branch: "main", SHA: shaA}); err != nil {
		t.Fatal(err)
	}
	rerun, err = dbRepo.LatestJobByNameBranch(cfg.Repo, "build", "main")
	if err != nil || rerun.Status != core.StatusRunning {
		t.Fatalf("second rerun: %+v, %v", rerun, err)
	}
	git(source, "commit", "--allow-empty", "-m", "C")
	shaC := git(source, "rev-parse", "HEAD")
	if err := fetchMirror(context.Background(), cfg.MirrorPath); err != nil {
		t.Fatal(err)
	}
	if err := pollOnce(context.Background(), runner, cfg, jobs, nil); err != nil {
		t.Fatal(err)
	}
	rerun, err = dbRepo.JobByRunID(rerun.RunID)
	if err != nil || rerun.Status != core.StatusCanceled {
		t.Fatalf("new commit did not cancel historical rerun: %+v, %v", rerun, err)
	}
	latest, err := dbRepo.LatestJobByNameBranch(cfg.Repo, "build", "main")
	if err != nil || latest.SHA != shaC || latest.Status != core.StatusRunning {
		t.Fatalf("new commit did not start: %+v, %v", latest, err)
	}
}

func TestWorkerReloadDuringFetchUsesNewEnv(t *testing.T) {
	root, dbRepo, write, git := newWorkerTestRoot(t)
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, ".refci"), 0755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(source, ".refci/conf.yml"), "build: {branch_pattern: main, script: .refci/run.sh}\n")
	write(filepath.Join(source, ".refci/run.sh"), "printf '%s' \"$TOKEN\"\n")
	git(source, "init", "-b", "main")
	git(source, "add", ".")
	git(source, "commit", "-m", "A")
	mirror := core.MirrorPath("acme/api")
	git(root, "clone", "--mirror", source, mirror)
	write(filepath.Join(root, "upload-pack"), "#!/bin/sh\ntouch '"+filepath.Join(root, "fetch-started")+"'\nwhile [ ! -f '"+filepath.Join(root, "release-fetch")+"' ]; do sleep 0.01; done\nexec git-upload-pack \"$@\"\n")
	git(mirror, "config", "remote.origin.uploadpack", filepath.Join(root, "upload-pack"))
	write("api.env", "TOKEN=old\n")
	write("config.yml", "api: {repo: acme--api, env: api.env}\n")
	configs, err := loadWorkerConfigs("config.yml")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	status := make(chan tui.StatusEvent, 64)
	w, err := startRepoWorker(ctx, dbRepo, configs[0], time.Hour, false, status)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	defer func() { stop(); <-w.done }()
	waitForWorkerTest(t, func() bool { _, err := os.Stat(filepath.Join(root, "fetch-started")); return err == nil })
	write("api.env", "TOKEN=new\n")
	changed, err := reloadRepoWorkers(ctx, dbRepo, map[string]*repoWorker{configs[0].Repo: w}, "config.yml", time.Hour, status)
	if err != nil || !changed {
		t.Fatalf("reload: %v, %v", changed, err)
	}
	write(filepath.Join(root, "release-fetch"), "")
	var job core.Job
	waitForWorkerTest(t, func() bool {
		job, err = dbRepo.LatestJobByNameBranch("acme/api", "build", "main")
		return err == nil && job.Status == core.StatusFinished
	})
	output, err := os.ReadFile(job.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "new" {
		t.Fatalf("job started after successful reload used stale env: %q", output)
	}
}
