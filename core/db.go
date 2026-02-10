package core

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type DBKind string

const (
	DBSQLite   DBKind = "sqlite"
	DBPostgres DBKind = "postgres"
)

type DBConfig struct {
	Kind DBKind

	// SQLitePath is used when Kind == DBSQLite.
	SQLitePath string

	// PostgresDSN is used when Kind == DBPostgres.
	PostgresDSN string
}

// OpenDB returns a *sql.DB for sqlite or postgres based on config.
func OpenDB(cfg DBConfig) (*sql.DB, error) {
	driverName, dsn, err := resolveDriver(cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}

	return db, nil
}

func resolveDriver(cfg DBConfig) (driverName string, dsn string, err error) {
	switch cfg.Kind {
	case DBSQLite:
		path := strings.TrimSpace(cfg.SQLitePath)
		if path == "" {
			path = "nci.db"
		}
		return "sqlite", path, nil
	case DBPostgres:
		dsn := strings.TrimSpace(cfg.PostgresDSN)
		if dsn == "" {
			return "", "", fmt.Errorf("postgres dsn is required")
		}
		return "pgx", dsn, nil
	default:
		return "", "", fmt.Errorf("unsupported db kind: %q", cfg.Kind)
	}
}
