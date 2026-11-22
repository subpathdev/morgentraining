package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/subpathdev/morgentraining/internal/training"
)

func open(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestReopenKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	ctx := context.Background()
	db := open(t, path)
	if err := db.SavePerson(ctx, training.Person{Key: "jan", Name: "Jan", Language: "de"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if p, err := open(t, path).Person(ctx, "jan"); err != nil || p.Name != "Jan" {
		t.Fatalf("after reopen: %+v %v", p, err)
	}
}

func TestMissingRowsAreNotFound(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "test.db"))
	ctx := context.Background()
	if _, err := db.Person(ctx, "nobody"); !errors.Is(err, training.ErrNotFound) {
		t.Fatalf("person: %v", err)
	}
	if _, err := db.LatestVersion(ctx, "none"); !errors.Is(err, training.ErrNotFound) {
		t.Fatalf("latest version: %v", err)
	}
	if _, err := db.AssignedVersion(ctx, "nobody", training.NewDate(2026, 11, 22)); !errors.Is(err, training.ErrNotFound) {
		t.Fatalf("assigned version: %v", err)
	}
}

func TestAtomicRollsBackOnError(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "test.db"))
	ctx := context.Background()
	boom := errors.New("boom")
	err := db.Atomic(ctx, func(r training.Repository) error {
		if err := r.SavePerson(ctx, training.Person{Key: "jan", Name: "Jan", Language: "de"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}
	if people, _ := db.People(ctx); len(people) != 0 {
		t.Fatalf("rolled-back write is visible: %+v", people)
	}
}

func TestBackupIsAConsistentCopy(t *testing.T) {
	dir := t.TempDir()
	db := open(t, filepath.Join(dir, "test.db"))
	ctx := context.Background()
	if err := db.SavePerson(ctx, training.Person{Key: "jan", Name: "Jan", Language: "de"}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "backup.db")
	if err := db.Backup(ctx, target); err != nil {
		t.Fatal(err)
	}
	if people, err := open(t, target).People(ctx); err != nil || len(people) != 1 {
		t.Fatalf("backup: %+v %v", people, err)
	}
}
