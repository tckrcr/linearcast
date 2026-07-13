package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

// Execer is the context-aware subset of *sql.DB / *sql.Tx used by functions
// that can run either standalone or inside a caller-managed transaction.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// WithTx runs fn inside a transaction, rolling back automatically when fn
// returns an error or panics. Callers pass the provided tx to db functions that
// accept Execer.
func WithTx(ctx context.Context, conn *sql.DB, fn func(tx Execer) error) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// WithImmediateTx runs fn inside a SQLite BEGIN IMMEDIATE transaction, rolling
// back automatically when fn returns an error or panics. Callers pass the
// provided tx to db functions that accept Execer.
func WithImmediateTx(ctx context.Context, conn *sql.DB, fn func(tx Execer) error) error {
	sqlConn, err := conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer sqlConn.Close()
	if _, err := sqlConn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = sqlConn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(sqlConn); err != nil {
		return err
	}
	if _, err := sqlConn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// OpenReadOnly opens the SQLite database read-only with WAL.
func OpenReadOnly(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)", url.PathEscape(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// OpenReadWrite opens the SQLite database for writers (ingest, scheduler).
// Writer processes intentionally use one connection: SQLite allows only one
// writer at a time, and the busy timeout is the coordination policy for the
// small amount of concurrent work done by schedulers/admin/packagers.
func OpenReadWrite(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)", url.PathEscape(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
