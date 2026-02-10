package core

import (
	"database/sql"
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
		`CREATE TABLE IF NOT EXISTS code_repos (
			repo TEXT PRIMARY KEY,
			url TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS branch_conf (
			repo TEXT NOT NULL,
			ref_pattern TEXT NOT NULL,
			script_path TEXT NOT NULL,
			PRIMARY KEY (repo, ref_pattern)
		);`,
		`CREATE TABLE IF NOT EXISTS jobs (
			repo TEXT NOT NULL,
			ref TEXT NOT NULL,
			sha TEXT NOT NULL,
			start_at TEXT NOT NULL,
			end_at TEXT,
			status TEXT NOT NULL,
			msg TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (repo, ref, sha)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_repo_ref_status_start
		 ON jobs(repo, ref, status, start_at DESC);`,
	}

	for _, stmt := range stmts {
		if _, err := r.db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

func (r SQLiteRepo) SaveBranchConf(bc BranchConf) error {
	if _, err := r.db.Exec(
		`INSERT INTO branch_conf (repo, ref_pattern, script_path) VALUES (?, ?, ?)`,
		bc.Repo, bc.RefPattern, bc.ScriptPath,
	); err != nil {
		return fmt.Errorf("save branch conf: %w", err)
	}
	return nil
}

func (r SQLiteRepo) ListBranchConf(repo string) ([]BranchConf, error) {
	rows, err := r.db.Query(
		`SELECT repo, ref_pattern, script_path FROM branch_conf WHERE repo = ? ORDER BY ref_pattern ASC`,
		repo,
	)
	if err != nil {
		return nil, fmt.Errorf("list branch conf: %w", err)
	}
	defer rows.Close()

	var out []BranchConf
	for rows.Next() {
		var bc BranchConf
		if err := rows.Scan(&bc.Repo, &bc.RefPattern, &bc.ScriptPath); err != nil {
			return nil, fmt.Errorf("scan branch conf: %w", err)
		}
		out = append(out, bc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate branch conf: %w", err)
	}
	return out, nil
}

func (r SQLiteRepo) UpdateBranchConf(bc BranchConf) error {
	res, err := r.db.Exec(
		`UPDATE branch_conf SET script_path = ? WHERE repo = ? AND ref_pattern = ?`,
		bc.ScriptPath, bc.Repo, bc.RefPattern,
	)
	if err != nil {
		return fmt.Errorf("update branch conf: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r SQLiteRepo) DeleteBranchConf(repo, refPattern string) error {
	_, err := r.db.Exec(
		`DELETE FROM branch_conf WHERE repo = ? AND ref_pattern = ?`,
		repo, refPattern,
	)
	if err != nil {
		return fmt.Errorf("delete branch conf: %w", err)
	}
	return nil
}

func (r SQLiteRepo) DeleteBranchConfByRepo(repo string) error {
	_, err := r.db.Exec(`DELETE FROM branch_conf WHERE repo = ?`, repo)
	if err != nil {
		return fmt.Errorf("delete branch conf by repo: %w", err)
	}
	return nil
}

func (r SQLiteRepo) SaveCodeRepo(repo CodeRepo) error {
	_, err := r.db.Exec(
		`INSERT INTO code_repos (repo, url)
		 VALUES (?, ?)
		 ON CONFLICT(repo) DO UPDATE SET url = excluded.url`,
		repo.Repo, repo.URL,
	)
	if err != nil {
		return fmt.Errorf("save code repo: %w", err)
	}
	return nil
}

func (r SQLiteRepo) ListCodeRepo() ([]CodeRepo, error) {
	rows, err := r.db.Query(`SELECT repo, url FROM code_repos ORDER BY repo ASC`)
	if err != nil {
		return nil, fmt.Errorf("list code repo: %w", err)
	}
	defer rows.Close()

	var out []CodeRepo
	for rows.Next() {
		var c CodeRepo
		if err := rows.Scan(&c.Repo, &c.URL); err != nil {
			return nil, fmt.Errorf("scan code repo: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate code repo: %w", err)
	}
	return out, nil
}

func (r SQLiteRepo) DeleteRepo(repo string) error {
	_, err := r.db.Exec(`DELETE FROM code_repos WHERE repo = ?`, repo)
	if err != nil {
		return fmt.Errorf("delete repo: %w", err)
	}
	return nil
}

func (r SQLiteRepo) FetchJob(repo, ref, sha string) (Job, error) {
	var (
		j       Job
		startAt string
		endAt   sql.NullString
	)
	err := r.db.QueryRow(
		`SELECT repo, ref, sha, start_at, end_at, status, msg
		 FROM jobs
		 WHERE repo = ? AND ref = ? AND sha = ?`,
		repo, ref, sha,
	).Scan(&j.Repo, &j.Ref, &j.SHA, &startAt, &endAt, &j.Status, &j.Msg)
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

func (r SQLiteRepo) CreateJob(repo, ref, sha string) error {
	now := formatStoredTime(time.Now().UTC())
	_, err := r.db.Exec(
		`INSERT INTO jobs (repo, ref, sha, start_at, status, msg)
		 VALUES (?, ?, ?, ?, ?, '')`,
		repo, ref, sha, now, StatusPending,
	)
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	return nil
}

func (r SQLiteRepo) UpdateJob(repo, ref, sha, status, msg string) error {
	now := formatStoredTime(time.Now().UTC())
	_, err := r.db.Exec(
		`UPDATE jobs
		 SET status = ?,
		     msg = ?,
		     end_at = CASE
		                WHEN ? IN (?, ?, ?) THEN ?
		                ELSE end_at
		              END
		 WHERE repo = ? AND ref = ? AND sha = ?`,
		status,
		msg,
		status, StatusFinished, StatusFailed, StatusCanceled,
		now,
		repo, ref, sha,
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
	if strings.TrimSpace(filter.Ref) != "" {
		where = append(where, "ref = ?")
		args = append(args, filter.Ref)
	}
	if strings.TrimSpace(filter.Status) != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}

	query := `SELECT repo, ref, sha, start_at, end_at, status, msg FROM jobs`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY start_at DESC"

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
		if err := rows.Scan(&j.Repo, &j.Ref, &j.SHA, &startAt, &endAt, &j.Status, &j.Msg); err != nil {
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
