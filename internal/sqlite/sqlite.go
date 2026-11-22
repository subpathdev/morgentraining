// Package sqlite stores Morgentraining data in a single SQLite file and
// implements training.Repository.
//
// Run exactly one process per database file. Texts per language are stored as
// JSON objects; see schema.sql.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO

	"github.com/subpathdev/morgentraining/internal/training"
)

//go:embed schema.sql
var schemaV1 string

// DB is an open database: the storage the domain needs plus what operations need.
// The implementation is unexported; Open returns it as a DB.
type DB interface {
	training.Repository

	// Backup writes a consistent copy of the database to target, which must not exist yet.
	Backup(ctx context.Context, target string) error
	Close() error
}

type db struct {
	repo
	sql *sql.DB
}

var _ DB = (*db)(nil)

// Open opens (or creates) the database file and brings the schema up to date.
func Open(path string) (DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer anyway, and this avoids "database is locked".
	conn.SetMaxOpenConns(1)
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &db{repo: repo{q: conn, conn: conn}, sql: conn}, nil
}

func (d *db) Close() error { return d.sql.Close() }

func (d *db) Backup(ctx context.Context, target string) error {
	_, err := d.sql.ExecContext(ctx, "VACUUM INTO ?", target)
	return err
}

func migrate(conn *sql.DB) error {
	var v int
	if err := conn.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= 1 {
		return nil
	}
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaV1); err != nil {
		return fmt.Errorf("apply schema v1: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
		return err
	}
	return tx.Commit()
}

// querier is what *sql.DB and *sql.Tx have in common.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// repo runs the queries, either on the database or inside a transaction.
type repo struct {
	q    querier
	conn *sql.DB // nil inside a transaction
}

func (r *repo) Atomic(ctx context.Context, fn func(training.Repository) error) error {
	if r.conn == nil { // already in a transaction: join it
		return fn(r)
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(&repo{q: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", what, training.ErrNotFound)
	}
	return err
}

func (r *repo) exists(ctx context.Context, query string, args ...any) (bool, error) {
	var ok bool
	err := r.q.QueryRowContext(ctx, "SELECT EXISTS ("+query+")", args...).Scan(&ok)
	return ok, err
}
