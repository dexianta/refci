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
)

type RunJobRequest struct {
	Repo       string
	Name       string
	Branch     string
	SHA        string
	ScriptPath string
	WorkDir    string
	Env        []string
}

type JobRunner struct {
	dbRepo      DbRepo
	cancelGrace time.Duration

	mu      sync.Mutex
	running map[string]*runningJob
}

type runningJob struct {
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	done     chan struct{}
	canceled atomic.Bool
}

func NewJobRunner(dbRepo DbRepo) *JobRunner {
	return &JobRunner{
		dbRepo:      dbRepo,
		cancelGrace: 5 * time.Second,
		running:     map[string]*runningJob{},
	}
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
		return nil
	}

	// sha is new
	if latestJob.Status == StatusRunning || latestJob.Status == StatusPending {
		if err = j.Cancel(latestJob.Repo, latestJob.Name, latestJob.Branch, latestJob.SHA); err != nil {
			return err
		}
	}

	workDir, err := EnsureWorktree(context.Background(), jobConf.Repo, branch, sha)
	if err != nil {
		return err
	}
	scriptPath := filepath.Join(workDir, jobConf.ScriptPath)
	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("script not found: %s", scriptPath)
	}

	if _, err = j.Start(context.Background(), RunJobRequest{
		Repo:       jobConf.Repo,
		Name:       name,
		Branch:     branch,
		SHA:        sha,
		ScriptPath: scriptPath,
		WorkDir:    workDir,
		Env:        envs,
	}); err != nil {
		return err
	}

	return nil
}

func (r *JobRunner) Start(ctx context.Context, req RunJobRequest) (string, error) {
	key := jobKey(req.Repo, req.Name, req.Branch, req.SHA)
	r.mu.Lock()
	if _, exists := r.running[key]; exists {
		r.mu.Unlock()
		return "", fmt.Errorf("job is already running: %s %s %s %s", req.Repo, req.Name, req.Branch, req.SHA)
	}
	r.mu.Unlock()

	if err := r.dbRepo.CreateJob(req.Repo, req.Name, req.Branch, req.SHA); err != nil {
		return "", fmt.Errorf("create job row: %w", err)
	}

	logPath, logFile, err := createJobLogFile(req)
	if err != nil {
		_ = r.dbRepo.UpdateJob(req.Repo, req.Name, req.Branch, req.SHA, StatusFailed, err.Error())
		return "", err
	}

	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, "bash", req.ScriptPath)
	cmd.Dir = strings.TrimSpace(req.WorkDir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := r.dbRepo.UpdateJob(req.Repo, req.Name, req.Branch, req.SHA, StatusRunning, logPath); err != nil {
		_ = logFile.Close()
		cancel()
		return "", fmt.Errorf("set job running: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = r.dbRepo.UpdateJob(req.Repo, req.Name, req.Branch, req.SHA, StatusFailed, err.Error())
		cancel()
		return "", fmt.Errorf("start job process: %w", err)
	}

	rj := &runningJob{
		cancel: cancel,
		cmd:    cmd,
		done:   make(chan struct{}),
	}

	r.mu.Lock()
	r.running[key] = rj
	r.mu.Unlock()

	go r.waitJob(req, key, rj, logFile)

	return logPath, nil
}

func (r *JobRunner) Cancel(repo, name, branch, sha string) error {
	key := jobKey(repo, name, branch, sha)

	r.mu.Lock()
	rj, ok := r.running[key]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("job is not running: %s %s %s %s", repo, name, branch, sha)
	}

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
	}

	return nil
}

func (r *JobRunner) IsRunning(repo, name, branch, sha string) bool {
	key := jobKey(repo, name, branch, sha)
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.running[key]
	return ok
}

func (r *JobRunner) waitJob(req RunJobRequest, key string, rj *runningJob, logFile *os.File) {
	err := rj.cmd.Wait()
	_ = logFile.Close()

	status, msg := classifyJobResult(err, rj.canceled.Load())
	_ = r.dbRepo.UpdateJob(req.Repo, req.Name, req.Branch, req.SHA, status, msg)

	r.mu.Lock()
	delete(r.running, key)
	r.mu.Unlock()
	close(rj.done)
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

	dir := filepath.Join(Root, "logs", repoPart)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create log dir %q: %w", dir, err)
	}

	name := fmt.Sprintf("%s-%s-%s.log", refPart, branchPart, shaPart)
	logPath := filepath.Join(dir, name)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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
	if err := syscall.Kill(-pid, sig); err == nil {
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

func jobKey(repo, name, branch, sha string) string {
	return repo + "\x00" + name + "\x00" + branch + "\x00" + sha
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
