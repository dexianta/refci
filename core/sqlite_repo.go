package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SQLiteRepo struct {
	db *sql.DB
}

var _ DbRepo = SQLiteRepo{}

func NewSQLiteRepo(db *sql.DB) (*SQLiteRepo, error) {
	r := &SQLiteRepo{db: db}
	if err := r.ensureSchema(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r SQLiteRepo) ensureSchema() error {
	exists, err := r.jobsTableExists()
	if err != nil {
		return err
	}
	if !exists {
		return r.createJobsTable()
	}

	cols, err := r.jobsColumns()
	if err != nil {
		return err
	}
	if !cols["commit_author"] {
		if err := r.ensureJobsColumn("commit_author", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		cols["commit_author"] = true
	}
	if !cols["run_id"] {
		if err := r.migrateLegacyJobsTable(); err != nil {
			return err
		}
		cols, err = r.jobsColumns()
		if err != nil {
			return err
		}
	}
	if !cols["log_path"] {
		if err := r.ensureJobsColumn("log_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}

	return r.createJobsIndexes()
}

func (r SQLiteRepo) jobsTableExists() (bool, error) {
	var name string
	err := r.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'jobs'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find jobs table: %w", err)
	}
	return true, nil
}

func (r SQLiteRepo) jobsColumns() (map[string]bool, error) {
	rows, err := r.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		return nil, fmt.Errorf("list jobs columns: %w", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			columnName string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return nil, fmt.Errorf("scan jobs column: %w", err)
		}
		cols[strings.ToLower(strings.TrimSpace(columnName))] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs columns: %w", err)
	}
	return cols, nil
}

func (r SQLiteRepo) createJobsTable() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS jobs (
			run_id TEXT NOT NULL PRIMARY KEY,
			repo TEXT NOT NULL,
			name TEXT NOT NULL,
			branch TEXT NOT NULL,
			sha TEXT NOT NULL,
			commit_author TEXT NOT NULL DEFAULT '',
			start_at TEXT NOT NULL,
			end_at TEXT,
			status TEXT NOT NULL,
			msg TEXT NOT NULL DEFAULT '',
			log_path TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_repo_name_branch_status_start
		 ON jobs(repo, name, branch, status, start_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_repo_name_branch_start
		 ON jobs(repo, name, branch, start_at DESC);`,
	}

	for _, stmt := range stmts {
		if _, err := r.db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

func (r SQLiteRepo) createJobsIndexes() error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_jobs_repo_name_branch_status_start
		 ON jobs(repo, name, branch, status, start_at DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_repo_name_branch_start
		 ON jobs(repo, name, branch, start_at DESC);`,
	}
	for _, stmt := range stmts {
		if _, err := r.db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure jobs index: %w", err)
		}
	}
	return nil
}

func (r SQLiteRepo) migrateLegacyJobsTable() error {
	tx, err := r.db.Begin()
	if err != nil {
		return fmt.Errorf("begin jobs migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	stmts := []string{
		`DROP TABLE IF EXISTS jobs_new;`,
		`CREATE TABLE jobs_new (
			run_id TEXT NOT NULL PRIMARY KEY,
			repo TEXT NOT NULL,
			name TEXT NOT NULL,
			branch TEXT NOT NULL,
			sha TEXT NOT NULL,
			commit_author TEXT NOT NULL DEFAULT '',
			start_at TEXT NOT NULL,
			end_at TEXT,
			status TEXT NOT NULL,
			msg TEXT NOT NULL DEFAULT '',
			log_path TEXT NOT NULL DEFAULT ''
		);`,
		`INSERT INTO jobs_new (run_id, repo, name, branch, sha, commit_author, start_at, end_at, status, msg, log_path)
		 SELECT
		     repo || char(0) || name || char(0) || branch || char(0) || sha || char(0) || start_at,
		     repo,
		     name,
		     branch,
		     sha,
		     commit_author,
		     start_at,
		     end_at,
		     status,
		     msg,
		     CASE
		         WHEN substr(trim(msg), 1, 1) = '/' THEN trim(msg)
		         ELSE ''
		     END
		 FROM jobs;`,
		`DROP TABLE jobs;`,
		`ALTER TABLE jobs_new RENAME TO jobs;`,
		`CREATE INDEX idx_jobs_repo_name_branch_status_start
		 ON jobs(repo, name, branch, status, start_at DESC);`,
		`CREATE INDEX idx_jobs_repo_name_branch_start
		 ON jobs(repo, name, branch, start_at DESC);`,
	}
	for _, stmt := range stmts {
		if _, err = tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrate jobs schema: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit jobs migration: %w", err)
	}
	return nil
}

func (r SQLiteRepo) ensureJobsColumn(name, spec string) error {
	alter := fmt.Sprintf(`ALTER TABLE jobs ADD COLUMN %s %s`, name, spec)
	if _, err := r.db.Exec(alter); err != nil {
		return fmt.Errorf("add jobs.%s: %w", name, err)
	}
	return nil
}

func (r SQLiteRepo) LatestJobByNameBranch(repo, name, branch string) (Job, error) {
	return r.queryOne(
		`SELECT run_id, repo, name, branch, sha, commit_author, log_path, start_at, end_at, status, msg
		 FROM jobs
		 WHERE repo = ? AND name = ? AND branch = ?
		 ORDER BY start_at DESC
		 LIMIT 1`,
		repo, name, branch,
	)
}

func (r SQLiteRepo) JobByRunID(runID string) (Job, error) {
	return r.queryOne(
		`SELECT run_id, repo, name, branch, sha, commit_author, log_path, start_at, end_at, status, msg
		 FROM jobs
		 WHERE run_id = ?`,
		runID,
	)
}

func (r SQLiteRepo) CreateJob(runID, repo, name, branch, sha, commitAuthor string) error {
	now := formatStoredTime(time.Now().UTC())
	_, err := r.db.Exec(
		`INSERT INTO jobs (run_id, repo, name, branch, sha, commit_author, start_at, status, msg, log_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '')`,
		runID, repo, name, branch, sha, strings.TrimSpace(commitAuthor), now, StatusPending,
	)
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (r SQLiteRepo) UpdateJob(runID, status, msg, logPath string) error {
	now := formatStoredTime(time.Now().UTC())
	_, err := r.db.Exec(
		`UPDATE jobs
		 SET status = ?,
		     msg = ?,
		     log_path = CASE
		                  WHEN trim(?) <> '' THEN ?
		                  ELSE log_path
		                END,
		     end_at = CASE
		                WHEN ? IN (?, ?, ?) THEN ?
		                ELSE end_at
		              END
		 WHERE run_id = ?`,
		status,
		msg,
		logPath, logPath,
		status, StatusFinished, StatusFailed, StatusCanceled,
		now,
		runID,
	)
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	return nil
}

func (r SQLiteRepo) ListJob(filter JobFilter) ([]Job, error) {
	var (
		where []string
		args  []any
	)

	if strings.TrimSpace(filter.Repo) != "" {
		where = append(where, "repo = ?")
		args = append(args, filter.Repo)
	}
	if strings.TrimSpace(filter.Name) != "" {
		where = append(where, "name = ?")
		args = append(args, filter.Name)
	}
	if strings.TrimSpace(filter.Branch) != "" {
		where = append(where, "branch = ?")
		args = append(args, filter.Branch)
	}
	if strings.TrimSpace(filter.Status) != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}

	query := `SELECT run_id, repo, name, branch, sha, commit_author, log_path, start_at, end_at, status, msg FROM jobs`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY start_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list job: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}

	return out, nil
}

func (r SQLiteRepo) ListJobNames(repo string) ([]string, error) {
	query := `SELECT name FROM jobs`
	args := make([]any, 0, 1)
	if strings.TrimSpace(repo) != "" {
		query += ` WHERE repo = ?`
		args = append(args, repo)
	}
	query += ` GROUP BY name ORDER BY MIN(start_at) ASC, name ASC`

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list job names: %w", err)
	}
	defer rows.Close()

	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan job name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job names: %w", err)
	}

	return names, nil
}

func (r SQLiteRepo) queryOne(query string, args ...any) (Job, error) {
	row := r.db.QueryRow(query, args...)
	j, err := scanJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, nil
		}
		return Job{}, err
	}
	return j, nil
}

type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(scanner jobScanner) (Job, error) {
	var (
		j       Job
		startAt string
		endAt   sql.NullString
	)
	err := scanner.Scan(
		&j.RunID,
		&j.Repo,
		&j.Name,
		&j.Branch,
		&j.SHA,
		&j.CommitAuthor,
		&j.LogPath,
		&startAt,
		&endAt,
		&j.Status,
		&j.Msg,
	)
	if err != nil {
		return Job{}, err
	}

	j.Start, err = parseStoredTime(startAt)
	if err != nil {
		return Job{}, fmt.Errorf("parse job start: %w", err)
	}
	if endAt.Valid && strings.TrimSpace(endAt.String) != "" {
		j.End, err = parseStoredTime(endAt.String)
		if err != nil {
			return Job{}, fmt.Errorf("parse job end: %w", err)
		}
	}
	return j, nil
}

func formatStoredTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseStoredTime(v string) (time.Time, error) {
	s := strings.TrimSpace(v)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}
