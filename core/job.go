package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type RunJobRequest struct {
	RunID        string
	Repo         string
	Name         string
	Branch       string
	SHA          string
	CommitAuthor string
	ScriptPath   string
	WorkDir      string
	Env          []string
}

type JobRunner struct {
	dbRepo            DbRepo
	cancelGrace       time.Duration
	exitCleanupGrace  time.Duration
	logf              func(string, ...any)

	mu      sync.Mutex
	running map[string]*runningJob
}

type runningJob struct {
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	done     chan struct{}
	canceled atomic.Bool
	started  time.Time
}

func NewJobRunner(dbRepo DbRepo) *JobRunner {
	return &JobRunner{
		dbRepo:           dbRepo,
		cancelGrace:      5 * time.Second,
		exitCleanupGrace: 300 * time.Millisecond,
		running:          map[string]*runningJob{},
	}
}

func (j *JobRunner) SetLogger(logf func(string, ...any)) {
	j.logf = logf
}

func (j *JobRunner) QueueJob(jobConf JobConf, envs []string, branch, sha string) error {
	name := jobConf.Name
	if name == "" {
		return fmt.Errorf("job name is required")
	}

	latestJob, err := j.dbRepo.LatestJobByNameBranch(jobConf.Repo, name, branch)
	if err != nil {
		return err
	}
	if latestJob.SHA == sha {
		j.logEvent("queue skip job=%s branch=%s sha=%s unchanged", name, branch, shortSHA(sha))
		return nil
	}

	if latestJob.Status == StatusRunning || latestJob.Status == StatusPending {
		j.logEvent(
			"queue cancel previous job=%s branch=%s run=%s prev_sha=%s status=%s",
			latestJob.Name,
			latestJob.Branch,
			shortRunID(latestJob.RunID),
			shortSHA(latestJob.SHA),
			strings.ToLower(strings.TrimSpace(latestJob.Status)),
		)
		if err = j.Cancel(latestJob); err != nil {
			return err
		}
	}

	return j.runJobAtSHA(jobConf, envs, branch, sha)
}

func (j *JobRunner) RerunJob(jobConf JobConf, envs []string, branch, sha string) error {
	name := jobConf.Name
	if name == "" {
		return fmt.Errorf("job name is required")
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return fmt.Errorf("job sha is required")
	}

	j.logEvent("rerun queued job=%s branch=%s sha=%s", name, branch, shortSHA(sha))
	return j.runJobAtSHA(jobConf, envs, branch, sha)
}

func (j *JobRunner) runJobAtSHA(jobConf JobConf, envs []string, branch, sha string) error {
	name := jobConf.Name
	j.logEvent("prepare job=%s branch=%s sha=%s worktree", name, branch, shortSHA(sha))
	workDir, err := EnsureWorktree(context.Background(), jobConf.Repo, branch, sha)
	if err != nil {
		j.logEvent("prepare failed job=%s branch=%s sha=%s: %v", name, branch, shortSHA(sha), err)
		return err
	}
	scriptPath := filepath.Join(workDir, jobConf.ScriptPath)
	if _, err := os.Stat(scriptPath); err != nil {
		j.logEvent("prepare failed job=%s branch=%s sha=%s missing_script=%s", name, branch, shortSHA(sha), scriptPath)
		return fmt.Errorf("script not found: %s", scriptPath)
	}

	commitAuthor, err := CommitAuthorAtSHA(context.Background(), jobConf.Repo, sha)
	if err != nil {
		commitAuthor = ""
	}

	runID := newRunID()
	if _, err = j.Start(context.Background(), RunJobRequest{
		RunID:        runID,
		Repo:         jobConf.Repo,
		Name:         name,
		Branch:       branch,
		SHA:          sha,
		CommitAuthor: commitAuthor,
		ScriptPath:   scriptPath,
		WorkDir:      workDir,
		Env:          envs,
	}); err != nil {
		j.logEvent("start failed job=%s branch=%s sha=%s: %v", name, branch, shortSHA(sha), err)
		return err
	}

	return nil
}

func (r *JobRunner) Start(ctx context.Context, req RunJobRequest) (string, error) {
	key := strings.TrimSpace(req.RunID)
	if key == "" {
		return "", fmt.Errorf("job run id is required")
	}
	r.mu.Lock()
	if _, exists := r.running[key]; exists {
		r.mu.Unlock()
		return "", fmt.Errorf("job is already running: %s", key)
	}
	r.mu.Unlock()

	if err := r.dbRepo.CreateJob(req.RunID, req.Repo, req.Name, req.Branch, req.SHA, req.CommitAuthor); err != nil {
		return "", fmt.Errorf("create job row: %w", err)
	}

	logPath, logFile, err := createJobLogFile(req)
	if err != nil {
		_ = r.dbRepo.UpdateJob(req.RunID, StatusFailed, err.Error(), "")
		return "", err
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, "bash", req.ScriptPath)
	cmd.Dir = strings.TrimSpace(req.WorkDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := r.dbRepo.UpdateJob(req.RunID, StatusRunning, "", logPath); err != nil {
		_ = logFile.Close()
		cancel()
		return "", fmt.Errorf("set job running: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = r.dbRepo.UpdateJob(req.RunID, StatusFailed, err.Error(), "")
		cancel()
		return "", fmt.Errorf("start job process: %w", err)
	}

	rj := &runningJob{
		cancel:  cancel,
		cmd:     cmd,
		done:    make(chan struct{}),
		started: time.Now(),
	}

	r.mu.Lock()
	r.running[key] = rj
	r.mu.Unlock()

	r.logEvent("job started name=%s branch=%s run=%s sha=%s pid=%d log=%s", req.Name, req.Branch, shortRunID(req.RunID), shortSHA(req.SHA), cmd.Process.Pid, logPath)
	go r.waitJob(req, key, rj, logFile)

	return logPath, nil
}

func (r *JobRunner) Cancel(job Job) error {
	key := strings.TrimSpace(job.RunID)
	if key == "" {
		return fmt.Errorf("job run id is required")
	}

	r.mu.Lock()
	rj, ok := r.running[key]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("job is not running: %s %s %s %s", job.Repo, job.Name, job.Branch, job.SHA)
	}

	r.logEvent("cancel requested job=%s branch=%s run=%s sha=%s", job.Name, job.Branch, shortRunID(job.RunID), shortSHA(job.SHA))
	rj.canceled.Store(true)
	rj.cancel()

	if rj.cmd.Process != nil {
		_ = signalProcess(rj.cmd.Process.Pid, syscall.SIGTERM)
	}

	select {
	case <-rj.done:
		return nil
	case <-time.After(r.cancelGrace):
	}

	if rj.cmd.Process != nil {
		_ = signalProcess(rj.cmd.Process.Pid, syscall.SIGKILL)
		r.logEvent("cancel escalated to kill job=%s branch=%s run=%s sha=%s", job.Name, job.Branch, shortRunID(job.RunID), shortSHA(job.SHA))
	}

	return nil
}

func (r *JobRunner) IsRunning(runID string) bool {
	key := strings.TrimSpace(runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[key]
	return ok
}

func (r *JobRunner) waitJob(req RunJobRequest, key string, rj *runningJob, logFile *os.File) {
	pid := 0
	if rj.cmd.Process != nil {
		pid = rj.cmd.Process.Pid
	}
	err := rj.cmd.Wait()
	r.cleanupProcessGroup(req, pid)
	_ = logFile.Close()

	status, msg := classifyJobResult(err, rj.canceled.Load())
	_ = r.dbRepo.UpdateJob(req.RunID, status, msg, "")
	r.logEvent(
		"job finished name=%s branch=%s run=%s sha=%s status=%s duration=%s msg=%s",
		req.Name,
		req.Branch,
		shortRunID(req.RunID),
		shortSHA(req.SHA),
		status,
		time.Since(rj.started).Round(time.Millisecond),
		trimLogMessage(msg),
	)

	r.mu.Lock()
	delete(r.running, key)
	r.mu.Unlock()
	close(rj.done)
}

func (r *JobRunner) cleanupProcessGroup(req RunJobRequest, pid int) {
	if pid <= 0 || !processGroupExists(pid) {
		return
	}

	r.logEvent("job cleanup started name=%s branch=%s run=%s sha=%s pid=%d", req.Name, req.Branch, shortRunID(req.RunID), shortSHA(req.SHA), pid)
	_ = signalProcessGroup(pid, syscall.SIGTERM)
	if waitForProcessGroupExit(pid, r.exitCleanupGrace) {
		r.logEvent("job cleanup finished name=%s branch=%s run=%s sha=%s mode=term", req.Name, req.Branch, shortRunID(req.RunID), shortSHA(req.SHA))
		return
	}

	_ = signalProcessGroup(pid, syscall.SIGKILL)
	if waitForProcessGroupExit(pid, 100*time.Millisecond) {
		r.logEvent("job cleanup finished name=%s branch=%s run=%s sha=%s mode=kill", req.Name, req.Branch, shortRunID(req.RunID), shortSHA(req.SHA))
		return
	}

	r.logEvent("job cleanup incomplete name=%s branch=%s run=%s sha=%s pid=%d", req.Name, req.Branch, shortRunID(req.RunID), shortSHA(req.SHA), pid)
}

func (r *JobRunner) logEvent(format string, args ...any) {
	if r == nil || r.logf == nil {
		return
	}
	r.logf(format, args...)
}

func trimLogMessage(msg string) string {
	trimmed := strings.TrimSpace(msg)
	if trimmed == "" {
		return "-"
	}
	return trimmed
}

func classifyJobResult(waitErr error, canceled bool) (status, msg string) {
	if canceled {
		if waitErr == nil {
			return StatusCanceled, "canceled"
		}
		return StatusCanceled, strings.TrimSpace(waitErr.Error())
	}
	if waitErr == nil {
		return StatusFinished, ""
	}
	return StatusFailed, strings.TrimSpace(waitErr.Error())
}

func createJobLogFile(req RunJobRequest) (string, *os.File, error) {
	repoPart := ToLocalRepo(req.Repo)
	refPart := sanitizePathToken(req.Name)
	branchPart := sanitizePathToken(req.Branch)
	shaPart := sanitizePathToken(shortSHA(req.SHA))
	runPart := sanitizePathToken(shortRunID(req.RunID))

	dir := filepath.Join(Root, "logs", repoPart)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create log dir %q: %w", dir, err)
	}

	name := fmt.Sprintf("%s-%s-%s-%s.log", refPart, branchPart, shaPart, runPart)
	logPath := filepath.Join(dir, name)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", nil, fmt.Errorf("open log file %q: %w", logPath, err)
	}
	return logPath, f, nil
}

func signalProcess(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}

	// Try process group first to terminate spawned children too.
	if err := signalProcessGroup(pid, sig); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ESRCH) {
		// Fall back to direct process signal below.
	}

	err := syscall.Kill(pid, sig)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func signalProcessGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, sig)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func processGroupExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func waitForProcessGroupExit(pid int, timeout time.Duration) bool {
	if !processGroupExists(pid) {
		return true
	}
	if timeout <= 0 {
		return !processGroupExists(pid)
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if !processGroupExists(pid) {
			return true
		}
	}
	return !processGroupExists(pid)
}

func newRunID() string {
	return uuid.NewString()
}

func shortRunID(runID string) string {
	s := strings.TrimSpace(runID)
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

func shortSHA(sha string) string {
	s := strings.TrimSpace(sha)
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func sanitizePathToken(s string) string {
	out := strings.TrimSpace(s)
	out = strings.ReplaceAll(out, "/", "--")
	out = strings.ReplaceAll(out, "\\", "--")
	out = strings.ReplaceAll(out, ":", "_")
	out = strings.ReplaceAll(out, " ", "_")
	return out
}
