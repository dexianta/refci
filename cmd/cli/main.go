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

const appVersion = "0.1"

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
	runner := core.NewJobRunner(dbRepo)

	cfg, err := parseRuntimeConfig(repo, *envPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	uiCtx, cancelUI := context.WithCancel(ctx)
	defer cancelUI()

	fatalErrCh := make(chan error, 1)
	done := make(chan struct{})
	reportFatal := func(err error) {
		if err == nil || ctx.Err() != nil {
			return
		}
		select {
		case fatalErrCh <- err:
		default:
		}
		cancelUI()
		stop()
	}

	go func() {
		defer close(done)

		ticker := time.NewTicker(*interval)
		defer ticker.Stop()
		for {
			if err := fetchMirror(ctx, mirrorPath); err != nil {
				reportFatal(fmt.Errorf("fetch mirror: %w", err))
				return
			} else {
				jobs, err := core.LoadJobConfsFromRepo(ctx, cfg.Repo, "HEAD")
				if err != nil {
					reportFatal(fmt.Errorf("load .refci/conf.yml: %w", err))
					return
				} else if len(jobs) == 0 {
					reportFatal(fmt.Errorf("no jobs found in .refci/conf.yml for %s", cfg.Repo))
					return
				} else if err := pollOnce(ctx, dbRepo, runner, cfg, jobs); err != nil {
					reportFatal(fmt.Errorf("poll failed: %w", err))
					return
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	if err := tui.Run(uiCtx, cfg.Repo, dbRepo); err != nil {
		stop()
		cancelUI()
		<-done
		return err
	}
	stop()
	cancelUI()
	<-done

	select {
	case err := <-fatalErrCh:
		return err
	default:
	}
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
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Repo target:")
	fmt.Fprintln(w, "  owner/repo | owner--repo | repos/owner--repo | /abs/path/to/repos/owner--repo")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  refci init .")
	fmt.Fprintln(w, "  refci clone git@github.com:owner/repo.git")
	fmt.Fprintln(w, "  refci -e .env owner/repo")
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
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  -e string")
	fmt.Fprintln(w, "      env file path (default \".env\")")
	fmt.Fprintln(w, "  -interval duration")
	fmt.Fprintln(w, "      poll interval (default 3s)")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Repo target:")
	fmt.Fprintln(w, "  owner/repo | owner--repo | repos/owner--repo | /abs/path/to/repos/owner--repo")
}
