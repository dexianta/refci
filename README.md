# nci

`nci` is a local CI runner for repos you already control.

## Why this exists

1. GitHub Actions YAML can be hard to maintain for small, script-driven workflows.
2. Bash scripts can be simple and reliable when run on a warm, persistent machine.

`nci` keeps the runtime local:
- no remote queue
- no fresh clone per run
- no container startup cost by default

## What it does today

- Mirrors a GitHub repo locally (`git clone --mirror`)
- Polls for new branch commits
- Reads job config from repo `.nci/conf.yml` at `HEAD`
- Runs matching bash scripts in persistent git worktrees
- Stores job history in sqlite
- Shows job list/logs in a TUI

## Current scope

This project currently uses:
- one sqlite table: `jobs`
- one TUI page: logs viewer

Old repo/settings screens are intentionally removed.

## Requirements

- `git`
- `bash`
- Go toolchain (for building/running)

## Commands

```bash
nci init [path]
nci clone <git-repo-url>
nci -e <env_file> [-interval 3s] <repo-target>
```

Notes:
- `repo-target` can be `owner/repo`, `owner--repo`, or a path under `repos/`.
- Run from an nci root (the directory containing `nci.db`).

## Quick start

1. Initialize root:

```bash
nci init .
```

2. Mirror a repo:

```bash
nci clone git@github.com:owner/repo.git
```

3. In that repo, commit a `.nci/conf.yml` and scripts, for example:

```yaml
main-test:
  branch_pattern: main
  path_patterns: []
  script: .nci/main.sh
```

```bash
# .nci/main.sh
#!/usr/bin/env bash
set -euo pipefail

echo "run tests"
```

4. Start runner + TUI:

```bash
nci -e .env owner/repo
```

## Job config (`.nci/conf.yml`)

Supported keys per job:
- `branch_pattern`: exact branch or prefix wildcard (`feature-*`)
- `path_patterns`: optional file filters (glob-like)
- `script`: repo-relative script path

Parser notes:
- lightweight parser (not full YAML feature-complete)
- supports either top-level job map or `actions:` wrapper

## Runtime behavior

Per poll cycle:
1. `git fetch --prune origin` on mirror repo
2. load latest `.nci/conf.yml` from mirror `HEAD`
3. list local branch heads from mirror refs
4. for each `(job_name, branch)` compare latest recorded SHA
5. if changed and path filter matches, queue run

When queueing:
- create/update branch worktree at target SHA
- execute `bash <script>` with workdir = that worktree
- capture stdout/stderr to `logs/.../*.log`
- update job status in sqlite

If a poll step fails (fetch/conf/load/queue), the app exits with an error.

## TUI

Logs page only:
- `UP/DOWN`: select job
- `ENTER`: open log detail
- `ESC` or `ENTER` (detail view): back
- `CTRL+C`: quit

## nci root layout

```text
<root>/
  nci.db
  repos/
    owner--repo/          # bare mirror
  worktrees/
    owner--repo/
      main/               # detached worktree per branch
  logs/
    owner--repo/
      <job>-<branch>-<sha12>.log
```

## Data model

`jobs` primary key:
- `(repo, name, branch, sha)`

Fields tracked:
- start/end time
- status (`pending`, `running`, `finished`, `failed`, `canceled`)
- message (log path or failure message)

## Non-goals (for now)

- Hosted runners
- Container orchestration
- Full GitHub Actions feature parity
- Full YAML compatibility
