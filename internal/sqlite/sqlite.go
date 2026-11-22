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

// DB is an open database. It implements training.Repository.
type DB struct {
	repo
	sql *sql.DB
}

var _ training.Repository = (*DB)(nil)

// Open opens (or creates) the database file and brings the schema up to date.
func Open(path string) (*DB, error) {
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
	return &DB{repo: repo{q: conn, conn: conn}, sql: conn}, nil
}

func (db *DB) Close() error { return db.sql.Close() }

// Backup writes a consistent copy of the database to path, which must not exist yet.
func (db *DB) Backup(ctx context.Context, path string) error {
	_, err := db.sql.ExecContext(ctx, "VACUUM INTO ?", path)
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
