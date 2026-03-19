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
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS jobs (
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
		);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_repo_name_branch_status_start
		 ON jobs(repo, name, branch, status, start_at DESC);`,
	}

	for _, stmt := range stmts {
		if _, err := r.db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}

	if err := r.ensureJobsColumn("commit_author", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}

	return nil
}

func (r SQLiteRepo) ensureJobsColumn(name, spec string) error {
	rows, err := r.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		return fmt.Errorf("list jobs columns: %w", err)
	}
	defer rows.Close()

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
			return fmt.Errorf("scan jobs column: %w", err)
		}
		if strings.EqualFold(strings.TrimSpace(columnName), strings.TrimSpace(name)) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate jobs columns: %w", err)
	}

	alter := fmt.Sprintf(`ALTER TABLE jobs ADD COLUMN %s %s`, name, spec)
	if _, err := r.db.Exec(alter); err != nil {
		return fmt.Errorf("add jobs.%s: %w", name, err)
	}
	return nil
}

func (r SQLiteRepo) LatestJobByNameBranch(repo, name, branch string) (Job, error) {
	var (
		j       Job
		startAt string
		endAt   sql.NullString
	)
	err := r.db.QueryRow(
		`SELECT repo, name, branch, sha, commit_author, start_at, end_at, status, msg
		 FROM jobs
		 WHERE repo = ? AND name = ? AND branch = ?
		 ORDER BY start_at DESC
		 LIMIT 1`,
		repo, name, branch,
	).Scan(&j.Repo, &j.Name, &j.Branch, &j.SHA, &j.CommitAuthor, &startAt, &endAt, &j.Status, &j.Msg)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, nil
		}
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

func (r SQLiteRepo) CreateJob(repo, name, branch, sha, commitAuthor string) error {
	now := formatStoredTime(time.Now().UTC())
	_, err := r.db.Exec(
		`INSERT INTO jobs (repo, name, branch, sha, commit_author, start_at, status, msg)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '')
		 ON CONFLICT(repo, name, branch, sha) DO UPDATE SET
		     commit_author = excluded.commit_author,
		     start_at = excluded.start_at,
		     end_at = NULL,
		     status = excluded.status,
		     msg = excluded.msg`,
		repo, name, branch, sha, strings.TrimSpace(commitAuthor), now, StatusPending,
	)
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (r SQLiteRepo) UpdateJob(repo, name, branch, sha, status, msg string) error {
	now := formatStoredTime(time.Now().UTC())
	_, err := r.db.Exec(
		`UPDATE jobs
		 SET status = ?,
		     msg = ?,
		     end_at = CASE
		                WHEN ? IN (?, ?, ?) THEN ?
		                ELSE end_at
		              END
		 WHERE repo = ? AND name = ? AND branch = ? AND sha = ?`,
		status,
		msg,
		status, StatusFinished, StatusFailed, StatusCanceled,
		now,
		repo, name, branch, sha,
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

	query := `SELECT repo, name, branch, sha, commit_author, start_at, end_at, status, msg FROM jobs`
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
		var (
			j       Job
			startAt string
			endAt   sql.NullString
		)
		if err := rows.Scan(&j.Repo, &j.Name, &j.Branch, &j.SHA, &j.CommitAuthor, &startAt, &endAt, &j.Status, &j.Msg); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		j.Start, err = parseStoredTime(startAt)
		if err != nil {
			return nil, fmt.Errorf("parse job start: %w", err)
		}
		if endAt.Valid && strings.TrimSpace(endAt.String) != "" {
			j.End, err = parseStoredTime(endAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse job end: %w", err)
			}
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
