# refci

`refci` is a local CI runner for script-first workflows.

## Why this exists

- [The Pain That is Github Actions](https://news.ycombinator.com/item?id=43419701)
- GitHub Actions YAML can be painful for small, branch/script-driven pipelines.
- A local, warm runtime is often faster and simpler than remote CI startup overhead.
  - especially coding agent can help you write these bash scripts nowadays, wouldn't you want something you can test more easily locally?
- I can't count the times that it takes absurd amount of time for the queue to pick up a job..

## How it works

### 1) Install

```bash
curl -fsSL https://raw.githubusercontent.com/dexianta/refci/main/install.sh | bash
```

Optional:

```bash
REFCI_REF=v0.1.0 curl -fsSL https://raw.githubusercontent.com/dexianta/refci/main/install.sh | bash
REFCI_INSTALL_DIR="$HOME/bin" curl -fsSL https://raw.githubusercontent.com/dexianta/refci/main/install.sh | bash
```

### 2) Initialize a root

```bash
refci init .
```

This creates:
- `refci.db`
- `repos/` (mirror repos)
- `worktrees/` (per-branch worktrees)
- `logs/` (job logs)

### 3) Clone a repo mirror

```bash
refci clone git@github.com:owner/repo.git
```

### 4) Add job config to the repo

Create `.refci/conf.yml` in the repo (top-level dynamic map):

```yaml
main-test:
  branch_pattern: main
  path_patterns: []
  script: .refci/main.sh

feature-test:
  branch_pattern: feature-*
  path_patterns:
    - services/**
  script: .refci/feature.sh
```

Each key is the job name. `script` is repo-relative.

### 5) Run refci

From the refci root, run with the repo path:

```bash
refci -e .env ./repos/<repo-path>
```

`.env` file format:
- one variable per line: `KEY=value`
- optional `export` prefix: `export KEY=value`
- blank lines and lines starting with `#` are ignored
- surrounding single or double quotes around values are supported

Example:

```dotenv
# runtime secrets
API_TOKEN=abc123
export AWS_REGION=us-east-1
GREETING="hello world"
```

Accepted repo target forms (path form recommended):
- `./repos/owner--repo`
- `/abs/path/to/repos/owner--repo`
- `owner--repo`

Worker lifecycle:
- `refci` starts the poll loop and job worker in-process.
- if `refci` exits, job polling/execution stops.

For daemon-style usage, run `refci` in `tmux`:

```bash
tmux new -d -s refci 'cd /path/to/refci-root && refci -e .env ./repos/<repo-path>'
tmux attach -t refci
```

### 6) Runtime loop

Per interval (default `3s`):
1. `git fetch --prune origin` on mirror repo
2. load `.refci/conf.yml` from mirror `HEAD`
3. list branch heads
4. compare latest branch SHA with latest recorded job SHA
5. if changed (and path filter matches), queue run

Queued run behavior:
- create/reset branch worktree to target SHA
- run `bash <script>` in that worktree
- write stdout/stderr log under `logs/...`
- update `jobs` row in sqlite

If fetch/config/poll fails, refci keeps running, shows the error in the TUI, and retries on the next interval.

### 7) TUI

Single logs page:
- `UP/DOWN`: select job
- `ENTER`: open log detail
- `R`: rerun when the latest attempt for that job/branch is failed
- `C`: cancel selected running/pending job
- `ESC` or `ENTER` (detail): back
- `CTRL+C`: quit
