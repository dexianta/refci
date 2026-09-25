package main

import (
	"bufio"
	"context"
	"database/sql"
	"dexianta/refci/core"
	"dexianta/refci/tui"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
)

type runtimeConfig struct {
	Repo       string
	Env        []string
	MirrorPath string
}

const appVersion = "0.5.4"

// - refci init (for init root)
// - refci clone -i <ssh-key> <git-repo> (this download the code into repos folder)
// - refci -e <env_path>  <repos/repo_name>  // to start running poll for this one repo
// - future direction: parse each repos root/.refci folder, and generate .env file, the bash script file name can match the branch pattern
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return runMonitorPicker()
	}
	if isHelpArg(args[0]) || args[0] == "help" {
		printMainUsage(os.Stdout)
		return nil
	}

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "clone":
		return runClone(args[1:])
	case "version":
		fmt.Println(appVersion)
		return nil
	}

	return runPollLoop(args)
}

func runInit(args []string) error {
	if len(args) == 1 && isHelpArg(args[0]) {
		printInitUsage(os.Stdout)
		return nil
	}
	if len(args) > 1 {
		printInitUsage(os.Stderr)
		return fmt.Errorf("init accepts at most one argument")
	}

	path := "."
	if len(args) == 1 {
		path = args[0]
	}

	if err := core.InitRoot(path); err != nil {
		return err
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	fmt.Printf("refci root created at %s\n", absPath)
	return nil
}

func runClone(args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var identityPath string
	fs.StringVar(&identityPath, "i", "", "ssh private key path")
	fs.StringVar(&identityPath, "identity", "", "ssh private key path")
	fs.StringVar(&identityPath, "ssh-key", "", "ssh private key path")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printCloneUsage(os.Stdout)
			return nil
		}
		printCloneUsage(os.Stderr)
		return err
	}

	rest := fs.Args()
	if len(rest) != 1 {
		printCloneUsage(os.Stderr)
		return errors.New("clone requires exactly one git URL")
	}

	keyPath, err := normalizeSSHIdentityPath(identityPath)
	if err != nil {
		printCloneUsage(os.Stderr)
		return err
	}

	repoURL := strings.TrimSpace(rest[0])
	repo := core.ParseGithubUrl(repoURL)
	if repo == "" {
		return fmt.Errorf("invalid github URL: %q", repoURL)
	}

	if err := ensureRootAtCWD(); err != nil {
		return err
	}

	mirrorPath := core.MirrorPath(repo)
	sshHost := refciSSHHostAlias(repo)
	if err := ensureRefciSSHHost(sshHost, keyPath); err != nil {
		return err
	}
	cloneURL := githubSSHURLForHost(sshHost, repo)
	if err := core.CloneMirror(context.Background(), cloneURL, mirrorPath); err != nil {
		return err
	}

	fmt.Printf("configured ssh host %s with %s\n", sshHost, keyPath)
	fmt.Printf("cloned %s into %s\n", repo, mirrorPath)
	return nil
}

func runPollLoop(args []string) error {
	fs := flag.NewFlagSet("refci", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	envPath := fs.String("e", ".env", "env file path")
	interval := fs.Duration("interval", 3*time.Second, "poll interval")
	monitorMode := fs.Bool("monitor", false, "monitor only (no automatic fetch/poll; manual restart/cancel still available)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printPollUsage(os.Stdout)
			return nil
		}
		printPollUsage(os.Stderr)
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 && *monitorMode {
		return runMonitorPicker()
	}
	if len(rest) != 1 {
		printPollUsage(os.Stderr)
		return errors.New("poll mode requires exactly one repo target or YAML config file")
	}
	if *interval <= 0 {
		return errors.New("interval must be > 0")
	}

	db, dbRepo, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	configs := []runtimeConfig{}
	configMode := isWorkerConfig(rest[0])
	if configMode {
		if *monitorMode {
			return errors.New("use refci --monitor without a config file to monitor all repositories")
		}
		var hasEnvFlag bool
		fs.Visit(func(f *flag.Flag) { hasEnvFlag = hasEnvFlag || f.Name == "e" })
		if hasEnvFlag {
			return errors.New("set env per repository in the YAML config instead of using -e")
		}
		configs, err = loadWorkerConfigs(rest[0])
	} else {
		var cfg runtimeConfig
		cfg, err = loadRepoRuntimeConfig(rest[0], *envPath, *monitorMode)
		configs = append(configs, cfg)
	}
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rerunCh := make(chan tui.JobRequest, 8)
	cancelCh := make(chan tui.JobRequest, 8)
	statusCh := make(chan tui.StatusEvent, 8)
	workers := make(map[string]*repoWorker, len(configs))
	defer func() {
		stop()
		for _, worker := range workers {
			<-worker.done
		}
		close(statusCh)
	}()
	for _, cfg := range configs {
		worker, err := startRepoWorker(ctx, dbRepo, cfg, *interval, *monitorMode, statusCh)
		if err != nil {
			return fmt.Errorf("%s: %w", cfg.Repo, err)
		}
		workers[cfg.Repo] = worker
	}

	routerDone := make(chan struct{})
	go func() {
		defer close(routerDone)
		routeWorkerRequests(ctx, workers, statusCh, rerunCh, cancelCh)
	}()
	defer func() {
		stop()
		<-routerDone
	}()
	if configMode {
		return tui.RunRepoPicker(ctx, dbRepo, statusCh, rerunCh, cancelCh)
	}
	return tui.Run(ctx, configs[0].Repo, dbRepo, statusCh, rerunCh, cancelCh)
}

func runMonitorPicker() error {
	db, dbRepo, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	uiCtx, cancelUI := context.WithCancel(ctx)
	defer cancelUI()

	rerunCh := make(chan tui.JobRequest, 8)
	cancelCh := make(chan tui.JobRequest, 8)
	statusCh := make(chan tui.StatusEvent, 8)
	done := make(chan struct{})

	reportStatus := func(msg string, isErr bool) {
		if ctx.Err() != nil {
			return
		}
		select {
		case statusCh <- tui.StatusEvent{Message: msg, IsError: isErr}:
		default:
		}
	}

	go func() {
		defer close(done)
		defer close(statusCh)

		// This process runs no jobs: restarts need a worker's env and must be
		// owned by the worker, and cancel here only fixes up stale rows.
		runner := core.NewJobRunner(dbRepo)
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-rerunCh:
				reportStatus(fmt.Sprintf("restart %s/%s from the worker process (refci -e <env> <repo> or refci config.yml)", req.Name, req.Branch), true)
			case req := <-cancelCh:
				// A live worker holds the repo lock and owns the process; marking
				// the row here would leave the job running.
				lock, err := lockRepoWorker(req.Repo)
				if err != nil {
					reportStatus(fmt.Sprintf("cancel %s/%s from its worker process: %v", req.Name, req.Branch, err), true)
					continue
				}
				err = cancelJob(ctx, dbRepo, runner, req)
				_ = lock.Close()
				if err != nil {
					reportStatus(fmt.Sprintf("cancel failed for %s/%s@%s: %v", req.Name, req.Branch, core.ShortSHA(req.SHA), err), true)
					continue
				}
				reportStatus(fmt.Sprintf("cancel requested for %s/%s@%s", req.Name, req.Branch, core.ShortSHA(req.SHA)), false)
			}
		}
	}()

	err = tui.RunRepoPicker(uiCtx, dbRepo, statusCh, rerunCh, cancelCh)
	stop()
	cancelUI()
	<-done
	return err
}

func parseRuntimeConfig(repo, envPath string) (runtimeConfig, error) {
	f, err := os.Open(envPath)
	if err != nil {
		return runtimeConfig{}, fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()

	var cfg runtimeConfig
	cfg.Repo = repo

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimPrefix(line, "export ")
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		val := unquote(strings.TrimSpace(parts[1]))
		if key == "" {
			continue
		}

		cfg.Env = append(cfg.Env, key+"="+val)
	}
	if err := scanner.Err(); err != nil {
		return runtimeConfig{}, fmt.Errorf("read env file: %w", err)
	}

	return cfg, nil
}

// unquote strips one pair of matching surrounding quotes.
func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

func fetchMirror(ctx context.Context, mirrorPath string) error {
	if _, err := os.Stat(mirrorPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat mirror path: %w", err)
		}
		return fmt.Errorf("repo mirror not found (%s), run: refci clone -i <ssh-private-key> <git-repo>", mirrorPath)
	}

	return core.FetchMirror(ctx, mirrorPath)
}

func normalizeSSHIdentityPath(path string) (string, error) {
	raw := strings.TrimSpace(path)
	if raw == "" {
		return "", errors.New("clone requires -i <ssh-private-key>")
	}

	expanded, err := core.ExpandHome(raw)
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve ssh private key path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("ssh private key not found: %s", abs)
		}
		return "", fmt.Errorf("stat ssh private key: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("ssh private key is a directory: %s", abs)
	}
	return abs, nil
}

func refciSSHHostAlias(repo string) string {
	return "refci-" + core.ToLocalRepo(strings.ToLower(strings.TrimSpace(repo)))
}

func githubSSHURLForHost(host, repo string) string {
	return fmt.Sprintf("git@%s:%s.git", strings.TrimSpace(host), strings.TrimSpace(repo))
}

func ensureRefciSSHHost(host, identityPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home directory: %w", err)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("create ssh config dir: %w", err)
	}

	configPath := filepath.Join(sshDir, "config")
	raw, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read ssh config: %w", err)
	}

	next, err := upsertRefciSSHHostBlock(string(raw), host, identityPath)
	if err != nil {
		return err
	}
	if string(raw) == next {
		return nil
	}
	if err := os.WriteFile(configPath, []byte(next), 0o600); err != nil {
		return fmt.Errorf("write ssh config: %w", err)
	}
	return nil
}

func upsertRefciSSHHostBlock(config, host, identityPath string) (string, error) {
	host = strings.TrimSpace(host)
	identityPath = strings.TrimSpace(identityPath)
	if host == "" {
		return "", errors.New("ssh host alias is required")
	}
	if identityPath == "" {
		return "", errors.New("ssh identity path is required")
	}

	begin := refciSSHConfigBegin(host)
	end := refciSSHConfigEnd(host)
	block := refciSSHHostBlock(host, identityPath)
	if start := strings.Index(config, begin); start >= 0 {
		endStart := strings.Index(config[start:], end)
		if endStart < 0 {
			return "", fmt.Errorf("ssh config has incomplete refci block for %s", host)
		}
		endIdx := start + endStart + len(end)
		for endIdx < len(config) && (config[endIdx] == '\n' || config[endIdx] == '\r') {
			endIdx++
		}
		return config[:start] + block + config[endIdx:], nil
	}

	if sshConfigHasHost(config, host) {
		return "", fmt.Errorf("ssh config already has Host %s; remove or rename it before running refci clone", host)
	}

	if strings.TrimSpace(config) == "" {
		return block, nil
	}
	if !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	return config + "\n" + block, nil
}

func refciSSHHostBlock(host, identityPath string) string {
	return fmt.Sprintf(
		"%s\nHost %s\n  HostName github.com\n  User git\n  IdentityFile %s\n  IdentitiesOnly yes\n%s\n",
		refciSSHConfigBegin(host),
		host,
		identityPath,
		refciSSHConfigEnd(host),
	)
}

func refciSSHConfigBegin(host string) string {
	return "# refci:begin " + strings.TrimSpace(host)
}

func refciSSHConfigEnd(host string) string {
	return "# refci:end " + strings.TrimSpace(host)
}

func sshConfigHasHost(config, host string) bool {
	host = strings.TrimSpace(host)
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Host") {
			continue
		}
		for _, candidate := range fields[1:] {
			if strings.EqualFold(candidate, host) {
				return true
			}
		}
	}
	return false
}

func openDB() (*sql.DB, core.DbRepo, error) {
	if err := ensureRootAtCWD(); err != nil {
		return nil, nil, err
	}

	db, err := core.OpenDB(filepath.Join(core.Root, "refci.db"))
	if err != nil {
		return nil, nil, err
	}

	dbRepo, err := core.NewSQLiteRepo(db)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, dbRepo, nil
}

func ensureRootAtCWD() error {
	absRoot, err := filepath.Abs(".")
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	core.Root = absRoot

	dbPath := filepath.Join(core.Root, "refci.db")
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("not an refci root (%s missing). run: refci init", dbPath)
		}
		return fmt.Errorf("stat db: %w", err)
	}
	return nil
}

func resolveRepoTarget(target string) (repo string, mirrorPath string, err error) {
	input := strings.TrimSpace(target)
	if input == "" {
		return "", "", errors.New("repo target is required")
	}

	base := filepath.Base(filepath.Clean(input))

	if strings.HasPrefix(input, "repos/") || strings.Contains(input, string(os.PathSeparator)) {
		repo = strings.ReplaceAll(base, "--", "/")
		if repo == "" {
			return "", "", fmt.Errorf("cannot infer repo from %q", target)
		}
		path := input
		if !filepath.IsAbs(path) {
			path = filepath.Join(core.Root, path)
		}
		return repo, path, nil
	}

	repo = input
	if strings.Contains(repo, "--") && !strings.Contains(repo, "/") {
		repo = strings.ReplaceAll(repo, "--", "/")
	}
	return repo, core.MirrorPath(repo), nil
}

// pollOnce queues jobs whose branch moved. A failure in one job or branch is
// collected and does not stop the others from being checked.
func pollOnce(ctx context.Context, dbRepo core.DbRepo, runner *core.JobRunner, cfg runtimeConfig, jobs []core.JobConf, logf func(string, ...any)) error {
	var errs []error
	for _, jc := range jobs {
		branchSHA, err := core.ListBranchHeadsByPattern(ctx, cfg.Repo, jc.BranchPattern)
		if err != nil {
			logPollEvent(logf, "scan job=%s pattern=%q failed listing branches: %v", jc.Name, jc.BranchPattern, err)
			errs = append(errs, err)
			continue
		}

		for _, branch := range sortedBranchNames(branchSHA) {
			sha := branchSHA[branch]
			latestJob, err := dbRepo.LatestJobByNameBranch(cfg.Repo, jc.Name, branch)
			if err != nil {
				logPollEvent(logf, "scan job=%s branch=%s failed reading latest job: %v", jc.Name, branch, err)
				errs = append(errs, err)
				continue
			}
			prevSHA := latestJob.SHA
			if prevSHA == sha {
				continue
			}

			shouldRun, err := core.ShouldRunByPathPatterns(ctx, cfg.Repo, prevSHA, sha, jc.PathPatterns)
			if err != nil {
				logPollEvent(logf, "scan job=%s branch=%s failed checking paths: %v", jc.Name, branch, err)
				errs = append(errs, err)
				continue
			}
			if !shouldRun {
				continue
			}

			jobConf := jc
			jobConf.Repo = cfg.Repo
			if prevSHA == "" {
				prevSHA = "first-run"
			}
			logPollEvent(logf, "queue job=%s branch=%s sha=%s prev=%s", jc.Name, branch, core.ShortSHA(sha), core.ShortSHA(prevSHA))
			if err := runner.QueueJob(jobConf, cfg.Env, branch, sha); err != nil {
				logPollEvent(logf, "queue job=%s branch=%s sha=%s failed: %v", jc.Name, branch, core.ShortSHA(sha), err)
				errs = append(errs, fmt.Errorf("%s/%s: %w", jc.Name, branch, err))
			}
		}
	}
	return errors.Join(errs...)
}

func sortedBranchNames(branchSHA map[string]string) []string {
	branches := make([]string, 0, len(branchSHA))
	for branch := range branchSHA {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	return branches
}

func logPollEvent(logf func(string, ...any), format string, args ...any) {
	if logf == nil {
		return
	}
	logf(format, args...)
}

func rerunJob(ctx context.Context, dbRepo core.DbRepo, runner *core.JobRunner, cfg runtimeConfig, req tui.JobRequest) error {
	if strings.TrimSpace(req.RunID) == "" || strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Branch) == "" || strings.TrimSpace(req.SHA) == "" {
		return errors.New("invalid restart request")
	}

	jobRow, err := findJobByRunID(dbRepo, req.RunID)
	if err != nil {
		return err
	}
	if jobRow.Status != core.StatusFailed && jobRow.Status != core.StatusCanceled {
		return fmt.Errorf("job status is %q; only failed/canceled jobs can be restarted", jobRow.Status)
	}

	latestJob, err := dbRepo.LatestJobByNameBranch(cfg.Repo, req.Name, req.Branch)
	if err != nil {
		return err
	}
	if latestJob.RunID != "" && latestJob.RunID != jobRow.RunID && (latestJob.Status == core.StatusRunning || latestJob.Status == core.StatusPending) {
		return fmt.Errorf("latest job %s/%s is %q; cancel it before restarting an older run", req.Name, req.Branch, latestJob.Status)
	}

	jobConfs, err := core.LoadJobConfsFromRepo(ctx, cfg.Repo, "HEAD")
	if err != nil {
		return fmt.Errorf("load .refci/conf.yml: %w", err)
	}
	idx := slices.IndexFunc(jobConfs, func(jc core.JobConf) bool { return jc.Name == req.Name })
	if idx < 0 {
		return fmt.Errorf("job config %q not found", req.Name)
	}
	jobConf := jobConfs[idx]
	jobConf.Repo = cfg.Repo

	return runner.RerunJob(jobConf, cfg.Env, req.Branch, jobRow.SHA)
}

func cancelJob(ctx context.Context, dbRepo core.DbRepo, runner *core.JobRunner, req tui.JobRequest) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if strings.TrimSpace(req.RunID) == "" {
		return errors.New("invalid cancel request")
	}

	jobRow, err := findJobByRunID(dbRepo, req.RunID)
	if err != nil {
		return err
	}
	if jobRow.Status != core.StatusRunning && jobRow.Status != core.StatusPending {
		return fmt.Errorf("job status is %q; only running/pending jobs can be canceled", jobRow.Status)
	}

	err = runner.Cancel(jobRow)
	if errors.Is(err, core.ErrJobNotRunning) {
		// no process in this refci instance; just fix up the stale row
		return dbRepo.UpdateJob(jobRow.RunID, core.StatusCanceled, "canceled by user (stale job state)", "")
	}
	return err
}

func markRepoJobsCanceled(dbRepo core.DbRepo, repo, reason string, logf func(string, ...any)) (int, error) {
	if dbRepo == nil || strings.TrimSpace(repo) == "" {
		return 0, nil
	}

	statuses := []string{core.StatusRunning, core.StatusPending}
	canceled := 0
	for _, status := range statuses {
		jobs, err := dbRepo.ListJob(core.JobFilter{Repo: repo, Status: status})
		if err != nil {
			return canceled, err
		}
		for _, job := range jobs {
			if err := dbRepo.UpdateJob(job.RunID, core.StatusCanceled, reason, ""); err != nil {
				return canceled, err
			}
			canceled++
			logPollEvent(
				logf,
				"mark stale job canceled name=%s branch=%s sha=%s previous_status=%s",
				job.Name,
				job.Branch,
				core.ShortSHA(job.SHA),
				status,
			)
		}
	}
	return canceled, nil
}

func findJobByRunID(dbRepo core.DbRepo, runID string) (core.Job, error) {
	runIDValue := strings.TrimSpace(runID)
	if runIDValue == "" {
		return core.Job{}, errors.New("job run id is required")
	}
	job, err := dbRepo.JobByRunID(runIDValue)
	if err != nil {
		return core.Job{}, err
	}
	if strings.TrimSpace(job.RunID) == "" {
		return core.Job{}, fmt.Errorf("job not found: %s", runIDValue)
	}
	return job, nil
}

func isHelpArg(v string) bool {
	switch strings.TrimSpace(v) {
	case "-h", "--help":
		return true
	default:
		return false
	}
}

func printMainUsage(w io.Writer) {
	fmt.Fprintf(w, "refci v%s - local CI runner\n", appVersion)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  refci")
	fmt.Fprintln(w, "  refci init [path]")
	fmt.Fprintln(w, "  refci clone -i <ssh-private-key> <git-repo-url>")
	fmt.Fprintln(w, "  refci -e <env_file> [-interval 3s] <repo-target>")
	fmt.Fprintln(w, "  refci [-interval 3s] config.yml")
	fmt.Fprintln(w, "  refci --monitor [repo-target]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Repo target:")
	fmt.Fprintln(w, "  owner/repo | owner--repo | repos/owner--repo | /abs/path/to/repos/owner--repo")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  refci init .")
	fmt.Fprintln(w, "  refci clone -i ~/.ssh/refci-owner-repo git@github.com:owner/repo.git")
	fmt.Fprintln(w, "  refci")
	fmt.Fprintln(w, "  refci -e .env owner/repo")
	fmt.Fprintln(w, "  refci --monitor owner/repo")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Help:")
	fmt.Fprintln(w, "  refci --help")
	fmt.Fprintln(w, "  refci init --help")
	fmt.Fprintln(w, "  refci clone --help")
}

func printInitUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: refci init [path]")
	fmt.Fprintln(w, "Create a refci root at path (default: current directory).")
}

func printCloneUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: refci clone -i <ssh-private-key> <git-repo-url>")
	fmt.Fprintln(w, "Clone a mirror repo into <root>/repos and configure a per-repo SSH host alias.")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -i, --identity, --ssh-key string")
	fmt.Fprintln(w, "      ssh private key path for this repo's GitHub deploy key")
}

func printPollUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: refci -e <env_file> [-interval 3s] <repo-target>")
	fmt.Fprintln(w, "       refci [-interval 3s] config.yml")
	fmt.Fprintln(w, "       refci --monitor [repo-target]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -e string")
	fmt.Fprintln(w, "      env file path (default \".env\")")
	fmt.Fprintln(w, "  -interval duration")
	fmt.Fprintln(w, "      poll interval (default 3s)")
	fmt.Fprintln(w, "  --monitor")
	fmt.Fprintln(w, "      monitor mode (no automatic fetch/poll; manual restart/cancel only; no env file required)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Repo target:")
	fmt.Fprintln(w, "  owner/repo | owner--repo | repos/owner--repo | /abs/path/to/repos/owner--repo")
}
