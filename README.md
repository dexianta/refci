# nci — zero-overhead CI for small teams

Push code, see results in seconds, not minutes. No YAML, no containers, no queue. Just your machine, your scripts, your speed.

## Why

GitHub Actions and similar CI tools are slow. A 3-second test suite takes 4 minutes because of:

- Queue wait (10s–2min to get a runner)
- Fresh clone every time
- Environment setup (install runtimes, pull images)
- Dependency install from scratch (`npm install`, `go mod download`)

nci eliminates all of that. It runs on your machine with persistent clones, warm caches, and zero queue time. Your 3-second test finishes in 3 seconds.

## Core concepts

- **nci root**: a directory created by `nci init`, contains all nci state
- **Project**: a GitHub repo registered with nci
- **Job**: a bash script in the repo's `.nci/` directory, mapped to a branch by filename
- **Secret**: a per-project key/value pair, stored in sqlite, injected as env vars

## CLI

```
nci init <path>     # create an nci root at <path>
nci                 # launch TUI (must be run from an nci root)
```

### `nci init <path>`

- Creates the directory at `<path>`
- Creates `nci.db` (sqlite) with initial schema
- Creates `repos/`, `worktrees/`, `logs/` subdirectories
- Prints: "nci root created at <path>"

### `nci`

- Looks for `nci.db` in the current directory
- If not found: "Error: not an nci root. Run 'nci init <path>' to create one."
- If found: launches the TUI and starts polling for all configured projects

## nci root structure

```
~/ci/                           # nci root (user-chosen path)
  nci.db                        # sqlite: projects, secrets, build history, state
  repos/                        # bare clones
    user--api-server/
    user--frontend/
  worktrees/                    # persistent worktrees per branch
    user--api-server/
      main/
      pr-42/
  logs/                         # build output logs
    user--api-server/
      main/
        a1b2c3.log
```

## Job configuration

Jobs are defined in the repo in a `.nci/` directory. Each bash file maps to a branch by its filename:

```
.nci/
  main.sh         # runs on push to main
  staging.sh      # runs on push to staging
  pr.sh           # runs on any pull request update
```

That's it. No YAML, no config files. The filename IS the branch mapping. If `.nci/main.sh` exists, nci runs it when `main` gets a new commit.

### Job script conventions

Scripts should use `set -e` to stop on first failure. Structure steps however you want:

```bash
#!/bin/bash
set -e

echo "=== Installing ==="
go mod download

echo "=== Testing ==="
go test ./...

echo "=== Deploying ==="
kubectl apply -f deploy.yaml
```

If a script needs to be broken into separate files, call them from the main script:

```bash
#!/bin/bash
set -e
./scripts/install.sh
./scripts/test.sh
./scripts/deploy.sh
```

### PR handling

`pr.sh` is a special name that matches all pull requests. nci detects PRs via `git ls-remote refs/pull/*/head` — no GitHub API needed.

When a PR job runs, the working directory is the PR branch checked out as-is (not a merge preview with the base branch).

## Polling

Each project has a poller (goroutine) that runs on a configurable interval (default: 5s):

1. `git ls-remote <repo-url>` to get all current refs + shas
2. Compare each ref's sha against last-seen sha in sqlite
3. For each changed ref:
   - Check if `.nci/<branch>.sh` (or `pr.sh` for PR refs) exists at that sha
   - If yes: trigger a run
   - Update last-seen sha in sqlite

### Branch-to-file matching

- `refs/heads/main` → look for `.nci/main.sh`
- `refs/heads/staging` → look for `.nci/staging.sh`
- `refs/pull/42/head` → look for `.nci/pr.sh`

If no matching `.sh` file exists, the ref is ignored.

## Job execution

When a job is triggered:

1. **Cancel previous run** for the same project+branch if still running (via `context.WithCancel`, kills the running script)
2. `git fetch` the new sha into the bare clone
3. Update the persistent worktree for this branch (create if first time)
4. Inject environment variables (see below)
5. Execute the `.sh` file in the worktree directory
6. Capture stdout/stderr to a log file
7. Record result (pass/fail, sha, duration) in sqlite
8. Prune old logs beyond retention limit

### Environment variables

Available to every job script:

```
NCI_PROJECT=user/api-server
NCI_BRANCH=main
NCI_SHA=a1b2c3d4e5f6
NCI_PR=42                    # only set for PR builds
NCI_ROOT=/home/user/ci       # path to nci root
+ all project-level key/value pairs
+ all project secrets
```

### Overlapping runs

If a new commit arrives on a branch while the previous job for that branch is still running: **kill the old job, start the new one**. The latest commit is what matters.

## Secrets management

Secrets are managed via the TUI and stored in sqlite (`nci.db`), scoped per project.

- Per-project key/value pairs (e.g., `DEPLOY_KEY=xyz`)
- Injected as environment variables when job scripts run
- Stored as clear text in sqlite — the machine is the trust boundary
- If you can SSH into the machine, you can see the secrets (this is intentional)

Projects can also have general key/value config pairs (non-secret) for things like feature flags or environment-specific settings. These are also injected as env vars.

## TUI

The TUI is the primary interface. It launches when you run `nci` from an nci root.

### Features

- **Project list**: show all registered projects, polling status
- **Add project**: paste a GitHub URL, nci clones it and starts polling
- **Branch overview**: for each project, show branches with `.nci/` configs, last build status, duration
- **Live log output**: stream stdout/stderr of currently running jobs
- **Secrets management**: add/edit/delete per-project secrets and key/value pairs
- **Manual trigger**: force-run a job for a specific branch
- **Settings**: poll interval, log retention count per project

### TUI layout (rough)

```
┌─ nci ─────────────────────────────────────────────┐
│ Projects:                                          │
│  > user/api-server     polling (5s)               │
│    user/frontend       polling (10s)              │
│                                                    │
│ ┌─ user/api-server ─────────────────────────────┐ │
│ │ Branches:                                     │ │
│ │   main     ✓ 4s ago   (0.8s)                 │ │
│ │   pr/42    ✗ 2m ago   (0.3s)                 │ │
│ │                                               │ │
│ │ Secrets: 3 configured                         │ │
│ │ [Logs] [Secrets] [Trigger] [Settings]         │ │
│ └───────────────────────────────────────────────┘ │
│                                                    │
│ Live: running main.sh (user/api-server)            │
│ > === Installing ===                               │
│ > go mod download                                  │
│ > === Testing ===                                  │
│ > ok  ./pkg/auth  0.02s                            │
└───────────────────────────────────────────────────┘
```

## Log retention

- Default: keep last 3 build logs per project per branch
- Configurable per project via TUI
- Older logs are deleted automatically after each build

## Architecture (Go)

```
main goroutine
  └── TUI (bubbletea event loop)

per project:
  └── poller goroutine (ticker + git ls-remote)
        for each changed ref:
          └── runner goroutine
                context.WithCancel for cancellation
                exec.CommandContext to run script
                stdout/stderr → log file + TUI stream
```

### Dependencies

- Go standard library for most things
- [bubbletea](https://github.com/charmbracelet/bubbletea) for TUI
- [go-git](https://github.com/go-git/go-git) or shelling out to `git` CLI
- `database/sql` + sqlite driver for state

## sqlite schema (rough)

```sql
CREATE TABLE projects (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,           -- e.g. "user/api-server"
  url TEXT NOT NULL,            -- github clone URL
  poll_interval INTEGER DEFAULT 5,
  log_retention INTEGER DEFAULT 3,
  enabled INTEGER DEFAULT 1
);

CREATE TABLE secrets (
  id INTEGER PRIMARY KEY,
  project_id INTEGER REFERENCES projects(id),
  key TEXT NOT NULL,
  value TEXT NOT NULL
);

CREATE TABLE refs (
  id INTEGER PRIMARY KEY,
  project_id INTEGER REFERENCES projects(id),
  ref TEXT NOT NULL,            -- e.g. "refs/heads/main"
  last_sha TEXT
);

CREATE TABLE builds (
  id INTEGER PRIMARY KEY,
  project_id INTEGER REFERENCES projects(id),
  ref TEXT NOT NULL,
  sha TEXT NOT NULL,
  status TEXT NOT NULL,         -- "running", "pass", "fail", "cancelled"
  started_at DATETIME,
  finished_at DATETIME,
  duration_ms INTEGER,
  log_path TEXT
);
```

## Non-goals (for v1)

- GitHub commit status API integration
- Webhook support (polling only)
- Containerized/isolated builds
- Multi-machine distribution
- Web UI
- User authentication / multi-tenancy
- Merge-preview builds for PRs (just test the PR branch as-is)

## Future ideas

- `nci start` convenience command to launch in a tmux session
- GitHub commit status updates (push ✓/✗ back to PRs)
- Notifications (slack webhook, email) on failure
- Global secrets shared across projects
- Wildcard branch patterns (e.g., `release-*.sh`)
