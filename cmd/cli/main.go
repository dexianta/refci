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
	"strings"
	"syscall"
	"time"
)

type runtimeConfig struct {
	Repo string
	Env  []string
}

const appVersion = "0.5.3"

// - refci init (for init root)
// - refci clone <git-repo> (this download the code into repos folder)
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
		printMainUsage(os.Stdout)
		return nil
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
	if len(args) == 1 && isHelpArg(args[0]) {
		printCloneUsage(os.Stdout)
		return nil
	}
	if len(args) != 1 {
		printCloneUsage(os.Stderr)
		return errors.New("clone requires exactly one git URL")
	}

	repoURL := strings.TrimSpace(args[0])
	repo := core.ParseGithubUrl(repoURL)
	if repo == "" {
		return fmt.Errorf("invalid github URL: %q", repoURL)
	}

	if err := ensureRootAtCWD(); err != nil {
		return err
	}

	mirrorPath := filepath.Join(core.Root, "repos", core.ToLocalRepo(repo))
	if err := core.CloneMirror(context.Background(), repoURL, mirrorPath); err != nil {
		return err
	}

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
	if len(rest) != 1 {
		printPollUsage(os.Stderr)
		return errors.New("poll mode requires exactly one repo target")
	}
	if *interval <= 0 {
		return errors.New("interval must be > 0")
	}

	db, dbRepo, err := openDB()
	if err != nil {
		return err
	}
	defer db.Close()

	repo, mirrorPath, err := resolveRepoTarget(rest[0])
	if err != nil {
		return err
	}

	cfg := runtimeConfig{Repo: repo}
	runner := core.NewJobRunner(dbRepo)
	if !*monitorMode {
		cfg, err = parseRuntimeConfig(repo, *envPath)
		if err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	uiCtx, cancelUI := context.WithCancel(ctx)
	defer cancelUI()

	rerunCh := make(chan tui.RerunRequest, 8)
	cancelCh := make(chan tui.CancelRequest, 8)
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

		doPoll := func() {}
		var ticker *time.Ticker
		var tickerCh <-chan time.Time
		lastErr := ""

		if !*monitorMode {
			doPoll = func() {
				var loopErr error

				if err := fetchMirror(ctx, mirrorPath); err != nil {
					loopErr = fmt.Errorf("fetch mirror: %w", err)
				} else {
					jobs, err := core.LoadJobConfsFromRepo(ctx, cfg.Repo, "HEAD")
					if err != nil {
						loopErr = fmt.Errorf("load .refci/conf.yml: %w", err)
					} else if len(jobs) == 0 {
						loopErr = fmt.Errorf("no jobs found in .refci/conf.yml for %s", cfg.Repo)
					} else if err := pollOnce(ctx, dbRepo, runner, cfg, jobs); err != nil {
						loopErr = fmt.Errorf("poll failed: %w", err)
					}
				}

				if loopErr != nil {
					msg := loopErr.Error()
					if msg != lastErr {
						reportStatus(msg+" (will retry)", true)
						lastErr = msg
					}
				} else if lastErr != "" {
					// clear previously shown transient error once poll succeeds again
					reportStatus("", false)
					lastErr = ""
				}
			}

			doPoll()
			ticker = time.NewTicker(*interval)
			tickerCh = ticker.C
			defer ticker.Stop()
		}

		for {
			select {
			case <-ctx.Done():
				return
			case req := <-rerunCh:
				if err := rerunJob(ctx, runner, cfg, req); err != nil {
					reportStatus(fmt.Sprintf("restart failed for %s/%s: %v", req.Name, req.Branch, err), true)
					continue
				}
				reportStatus(fmt.Sprintf("restart started for %s/%s", req.Name, req.Branch), false)
			case req := <-cancelCh:
				if err := cancelJob(ctx, dbRepo, runner, req); err != nil {
					reportStatus(fmt.Sprintf("cancel failed for %s/%s@%s: %v", req.Name, req.Branch, shortSHA(req.SHA), err), true)
					continue
				}
				reportStatus(fmt.Sprintf("cancel requested for %s/%s@%s", req.Name, req.Branch, shortSHA(req.SHA)), false)
			case <-tickerCh:
				doPoll()
			}
		}
	}()

	if err := tui.Run(uiCtx, cfg.Repo, dbRepo, statusCh, rerunCh, cancelCh); err != nil {
		stop()
		cancelUI()
		<-done
		return err
	}
	stop()
	cancelUI()
	<-done
	return nil
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
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")
		val = strings.Trim(val, "'")
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

func fetchMirror(ctx context.Context, mirrorPath string) error {
	if _, err := os.Stat(mirrorPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat mirror path: %w", err)
		}
		return fmt.Errorf("repo mirror not found (%s), run: refci clone <git-repo>", mirrorPath)
	}

	return core.FetchMirror(ctx, mirrorPath)
}

func openDB() (*sql.DB, core.DbRepo, error) {
	if err := ensureRootAtCWD(); err != nil {
		return nil, nil, err
	}

	db, err := core.OpenDB(core.DBConfig{
		Kind:       core.DBSQLite,
		SQLitePath: filepath.Join(core.Root, "refci.db"),
	})
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
	return repo, filepath.Join(core.Root, "repos", core.ToLocalRepo(repo)), nil
}

func pollOnce(ctx context.Context, dbRepo core.DbRepo, runner *core.JobRunner, cfg runtimeConfig, jobs []core.JobConf) error {
	for _, jc := range jobs {
		branchSHA, err := core.ListBranchHeadsByPattern(ctx, cfg.Repo, jc.BranchPattern)
		if err != nil {
			return err
		}
		for branch, sha := range branchSHA {
			latestJob, err := dbRepo.LatestJobByNameBranch(cfg.Repo, jc.Name, branch)
			if err != nil {
				return err
			}
			prevSHA := latestJob.SHA

			shouldRun, err := core.ShouldRunByPathPatterns(ctx, cfg.Repo, prevSHA, sha, jc.PathPatterns)
			if err != nil {
				return err
			}
			if !shouldRun {
				continue
			}

			jobConf := jc
			jobConf.Repo = cfg.Repo
			if err := runner.QueueJob(jobConf, cfg.Env, branch, sha); err != nil {
				return err
			}
		}
	}
	return nil
}

func rerunJob(ctx context.Context, runner *core.JobRunner, cfg runtimeConfig, req tui.RerunRequest) error {
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Branch) == "" {
		return errors.New("invalid restart request")
	}

	jobConfs, err := core.LoadJobConfsFromRepo(ctx, cfg.Repo, "HEAD")
	if err != nil {
		return fmt.Errorf("load .refci/conf.yml: %w", err)
	}
	jobConf, err := findRerunJobConf(jobConfs, req.Name, req.Branch)
	if err != nil {
		return err
	}
	jobConf.Repo = cfg.Repo

	if err := runner.RerunJob(jobConf, cfg.Env, req.Branch); err != nil {
		return err
	}
	return nil
}

func cancelJob(ctx context.Context, dbRepo core.DbRepo, runner *core.JobRunner, req tui.CancelRequest) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.Branch) == "" || strings.TrimSpace(req.SHA) == "" {
		return errors.New("invalid cancel request")
	}

	jobRow, err := findJobByKey(dbRepo, strings.TrimSpace(req.Repo), req.Name, req.Branch, req.SHA)
	if err != nil {
		return err
	}
	status := strings.ToLower(jobRow.Status)
	if status != core.StatusRunning && status != core.StatusPending {
		return fmt.Errorf("job status is %q; only running/pending jobs can be canceled", jobRow.Status)
	}

	if err := runner.Cancel(jobRow.Repo, jobRow.Name, jobRow.Branch, jobRow.SHA); err == nil {
		return nil
	} else if status == core.StatusPending && strings.Contains(err.Error(), "job is not running") {
		if err := dbRepo.UpdateJob(jobRow.Repo, jobRow.Name, jobRow.Branch, jobRow.SHA, core.StatusCanceled, "canceled by user"); err != nil {
			return err
		}
		return nil
	} else {
		return err
	}
}

func findJobByKey(dbRepo core.DbRepo, repo, name, branch, sha string) (core.Job, error) {
	repoValue := strings.TrimSpace(repo)
	nameValue := strings.TrimSpace(name)
	branchValue := strings.TrimSpace(branch)
	shaValue := strings.TrimSpace(sha)
	if repoValue == "" || nameValue == "" || branchValue == "" || shaValue == "" {
		return core.Job{}, errors.New("job repo/name/branch/sha are required")
	}

	jobs, err := dbRepo.ListJob(core.JobFilter{
		Repo:   repoValue,
		Name:   nameValue,
		Branch: branchValue,
	})
	if err != nil {
		return core.Job{}, err
	}
	for _, j := range jobs {
		if strings.TrimSpace(j.SHA) == shaValue {
			return j, nil
		}
	}
	return core.Job{}, fmt.Errorf("job not found: %s/%s@%s", nameValue, branchValue, shortSHA(shaValue))
}

func findRerunJobConf(jobConfs []core.JobConf, name, branch string) (core.JobConf, error) {
	nameValue := strings.TrimSpace(name)
	branchValue := normalizeBranch(branch)
	if nameValue == "" || branchValue == "" {
		return core.JobConf{}, errors.New("job name and branch are required")
	}

	var matched []core.JobConf
	var sameName []core.JobConf
	for _, jc := range jobConfs {
		if strings.TrimSpace(jc.Name) != nameValue {
			continue
		}
		sameName = append(sameName, jc)
		if branchMatchesForRerun(branchValue, jc.BranchPattern) {
			matched = append(matched, jc)
		}
	}

	if len(matched) == 1 {
		return matched[0], nil
	}
	if len(matched) > 1 {
		return core.JobConf{}, fmt.Errorf("multiple job configs match %q on branch %q", nameValue, branchValue)
	}
	if len(sameName) == 1 {
		return sameName[0], nil
	}
	if len(sameName) > 1 {
		return core.JobConf{}, fmt.Errorf("multiple job configs share name %q; cannot choose for branch %q", nameValue, branchValue)
	}
	return core.JobConf{}, fmt.Errorf("job config %q not found", nameValue)
}

func branchMatchesForRerun(branch, pattern string) bool {
	p := normalizeBranch(pattern)
	if p == "" {
		p = "*"
	}
	if p == "*" {
		return true
	}
	if strings.Contains(p, "*") {
		// Keep matching behavior aligned with refci polling: only trailing wildcard is supported.
		if strings.Count(p, "*") != 1 || !strings.HasSuffix(p, "*") {
			return false
		}
		return strings.HasPrefix(branch, strings.TrimSuffix(p, "*"))
	}
	return branch == p
}

func normalizeBranch(v string) string {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "refs/heads/")
	s = strings.TrimPrefix(s, "refs/")
	return s
}

func shortSHA(sha string) string {
	s := strings.TrimSpace(sha)
	if len(s) <= 8 {
		return s
	}
	return s[:8]
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
	fmt.Fprintln(w, "  refci init [path]")
	fmt.Fprintln(w, "  refci clone <git-repo-url>")
	fmt.Fprintln(w, "  refci -e <env_file> [-interval 3s] <repo-target>")
	fmt.Fprintln(w, "  refci --monitor <repo-target>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Repo target:")
	fmt.Fprintln(w, "  owner/repo | owner--repo | repos/owner--repo | /abs/path/to/repos/owner--repo")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  refci init .")
	fmt.Fprintln(w, "  refci clone git@github.com:owner/repo.git")
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
	fmt.Fprintln(w, "Usage: refci clone <git-repo-url>")
	fmt.Fprintln(w, "Clone a mirror repo into <root>/repos.")
}

func printPollUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: refci -e <env_file> [-interval 3s] <repo-target>")
	fmt.Fprintln(w, "       refci --monitor <repo-target>")
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
