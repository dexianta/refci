package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSQLiteRepoAllowsMultipleRunsPerSHA(t *testing.T) {
	db := openTestDB(t)
	repo, err := NewSQLiteRepo(db)
	if err != nil {
		t.Fatalf("NewSQLiteRepo() error = %v", err)
	}

	const (
		run1 = "run-1"
		run2 = "run-2"
		repoName = "acme/refci"
		jobName = "build"
		branch = "main"
		sha = "deadbeefcafebabe"
	)

	if err := repo.CreateJob(run1, repoName, jobName, branch, sha, "alice"); err != nil {
		t.Fatalf("CreateJob(run1) error = %v", err)
	}
	if err := repo.UpdateJob(run1, StatusCanceled, "canceled", "/tmp/first.log"); err != nil {
		t.Fatalf("UpdateJob(run1) error = %v", err)
	}

	time.Sleep(time.Millisecond)

	if err := repo.CreateJob(run2, repoName, jobName, branch, sha, "alice"); err != nil {
		t.Fatalf("CreateJob(run2) error = %v", err)
	}
	if err := repo.UpdateJob(run2, StatusRunning, "", "/tmp/second.log"); err != nil {
		t.Fatalf("UpdateJob(run2) error = %v", err)
	}

	jobs, err := repo.ListJob(JobFilter{Repo: repoName, Name: jobName, Branch: branch})
	if err != nil {
		t.Fatalf("ListJob() error = %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("ListJob() len = %d, want 2", len(jobs))
	}
	if jobs[0].RunID != run2 || jobs[1].RunID != run1 {
		t.Fatalf("ListJob() order = [%s %s], want [%s %s]", jobs[0].RunID, jobs[1].RunID, run2, run1)
	}

	latest, err := repo.LatestJobByNameBranch(repoName, jobName, branch)
	if err != nil {
		t.Fatalf("LatestJobByNameBranch() error = %v", err)
	}
	if latest.RunID != run2 {
		t.Fatalf("LatestJobByNameBranch().RunID = %q, want %q", latest.RunID, run2)
	}
	if latest.SHA != sha {
		t.Fatalf("LatestJobByNameBranch().SHA = %q, want %q", latest.SHA, sha)
	}
	if latest.LogPath != "/tmp/second.log" {
		t.Fatalf("LatestJobByNameBranch().LogPath = %q, want %q", latest.LogPath, "/tmp/second.log")
	}
}

func TestCreateJobLogFileUsesFreshRunPath(t *testing.T) {
	oldRoot := Root
	Root = t.TempDir()
	defer func() {
		Root = oldRoot
	}()

	req1 := RunJobRequest{
		RunID:  "run-11111111",
		Repo:   "acme/refci",
		Name:   "build",
		Branch: "main",
		SHA:    "deadbeefcafebabe",
	}
	req2 := req1
	req2.RunID = "run-22222222"

	path1, file1, err := createJobLogFile(req1)
	if err != nil {
		t.Fatalf("createJobLogFile(req1) error = %v", err)
	}
	if _, err := file1.WriteString("first\n"); err != nil {
		t.Fatalf("file1.WriteString() error = %v", err)
	}
	_ = file1.Close()

	path2, file2, err := createJobLogFile(req2)
	if err != nil {
		t.Fatalf("createJobLogFile(req2) error = %v", err)
	}
	if _, err := file2.WriteString("second\n"); err != nil {
		t.Fatalf("file2.WriteString() error = %v", err)
	}
	_ = file2.Close()

	if path1 == path2 {
		t.Fatalf("log paths should differ across reruns, both were %q", path1)
	}

	body1, err := os.ReadFile(path1)
	if err != nil {
		t.Fatalf("ReadFile(path1) error = %v", err)
	}
	body2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("ReadFile(path2) error = %v", err)
	}
	if strings.TrimSpace(string(body1)) != "first" {
		t.Fatalf("path1 contents = %q, want %q", string(body1), "first\n")
	}
	if strings.TrimSpace(string(body2)) != "second" {
		t.Fatalf("path2 contents = %q, want %q", string(body2), "second\n")
	}
}

func TestSQLiteRepoMigratesLegacyJobsTable(t *testing.T) {
	db := openTestDB(t)

	_, err := db.Exec(`
		CREATE TABLE jobs (
			repo TEXT NOT NULL,
			name TEXT NOT NULL,
			branch TEXT NOT NULL,
			sha TEXT NOT NULL,
			commit_author TEXT NOT NULL DEFAULT '',
			start_at TEXT NOT NULL,
			end_at TEXT,
			status TEXT NOT NULL,
			msg TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (repo, name, branch, sha)
		);
	`)
	if err != nil {
		t.Fatalf("create legacy jobs table: %v", err)
	}
	_, err = db.Exec(
		`INSERT INTO jobs (repo, name, branch, sha, commit_author, start_at, status, msg)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"acme/refci",
		"build",
		"main",
		"deadbeefcafebabe",
		"alice",
		formatStoredTime(time.Now().UTC()),
		StatusCanceled,
		"canceled",
	)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	repo, err := NewSQLiteRepo(db)
	if err != nil {
		t.Fatalf("NewSQLiteRepo() error = %v", err)
	}

	jobs, err := repo.ListJob(JobFilter{Repo: "acme/refci"})
	if err != nil {
		t.Fatalf("ListJob() error = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("ListJob() len = %d, want 1", len(jobs))
	}
	if strings.TrimSpace(jobs[0].RunID) == "" {
		t.Fatalf("migrated job RunID is empty")
	}
}

func TestJobRunnerKillsBackgroundWritersOnExit(t *testing.T) {
	oldRoot := Root
	Root = t.TempDir()
	defer func() {
		Root = oldRoot
	}()

	db := openTestDB(t)
	repo, err := NewSQLiteRepo(db)
	if err != nil {
		t.Fatalf("NewSQLiteRepo() error = %v", err)
	}

	workDir := t.TempDir()
	scriptPath := filepath.Join(workDir, "job.sh")
	script := "#!/bin/bash\n(sleep 0.4; echo child-after-exit) &\necho parent-done\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(script) error = %v", err)
	}

	runner := NewJobRunner(repo)
	runner.exitCleanupGrace = 100 * time.Millisecond

	req := RunJobRequest{
		RunID:      "run-bg-cleanup",
		Repo:       "acme/refci",
		Name:       "build",
		Branch:     "main",
		SHA:        "deadbeefcafebabe",
		ScriptPath: scriptPath,
		WorkDir:    workDir,
	}
	logPath, err := runner.Start(context.Background(), req)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := repo.JobByRunID(req.RunID)
		if err != nil {
			t.Fatalf("JobByRunID() error = %v", err)
		}
		if job.Status == StatusFinished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish before deadline; last status=%q", job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}

	time.Sleep(700 * time.Millisecond)

	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(logPath) error = %v", err)
	}
	logText := string(body)
	if !strings.Contains(logText, "parent-done") {
		t.Fatalf("log missing parent output: %q", logText)
	}
	if strings.Contains(logText, "child-after-exit") {
		t.Fatalf("log contains leaked child output after parent exit: %q", logText)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(DBConfig{
		Kind:       DBSQLite,
		SQLitePath: path,
	})
	if err != nil {
		t.Fatalf("OpenDB() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
