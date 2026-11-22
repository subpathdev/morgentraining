package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestStorageFormat pins what is written to disk, so a change in a domain type
// can't silently change the stored format.
func TestStorageFormat(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "test.db"))
	ctx := context.Background()
	err := db.SaveExercise(ctx, training.Exercise{
		Key: "push-up", Kind: training.KindExercise,
		Names:    training.Text{"en": "Push-ups", "de": "Liegestütze"},
		Variants: map[string]training.Text{"knees": {"de": "auf den Knien"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SavePerson(ctx, training.Person{Key: "jan", Name: "Jan", Language: "de"}); err != nil {
		t.Fatal(err)
	}
	spec := training.PlanSpec{Key: "p", Names: training.Text{"de": "P"}, Blocks: []training.BlockSpec{{
		Names: training.Text{"de": "B"}, Steps: []training.StepSpec{{Exercise: "push-up", Seconds: 30, Variant: "knees"}},
	}}}
	id, err := db.CreateVersion(ctx, spec, 1, "h", time.Date(2026, 11, 22, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	err = db.AddRound(ctx, "jan", training.Round{
		PlanVersionID: id, Date: training.NewDate(2026, 11, 22),
		StartedAt:  time.Date(2026, 11, 22, 7, 30, 0, 0, time.FixedZone("CET", 3600)),
		RecordedAt: time.Date(2026, 11, 22, 6, 35, 0, 0, time.UTC), Completed: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var names, cues, variants, created, stepNames, date, started, recorded, skipped string
	db.sql.QueryRow("SELECT names, cues, variants FROM exercise").Scan(&names, &cues, &variants)
	db.sql.QueryRow("SELECT created_at FROM plan_version").Scan(&created)
	db.sql.QueryRow("SELECT names FROM step").Scan(&stepNames)
	db.sql.QueryRow("SELECT date, started_at, recorded_at, skipped_steps FROM round").Scan(&date, &started, &recorded, &skipped)
	for _, c := range []struct{ column, got, want string }{
		{"exercise.names", names, `{"de":"Liegestütze","en":"Push-ups"}`},
		{"exercise.cues", cues, `{}`},
		{"exercise.variants", variants, `{"knees":{"de":"auf den Knien"}}`},
		{"plan_version.created_at", created, `2026-11-22T06:00:00Z`},
		{"step.names", stepNames, `{}`},
		{"round.date", date, `2026-11-22`},
		{"round.started_at", started, `2026-11-22T06:30:00Z`},
		{"round.recorded_at", recorded, `2026-11-22T06:35:00Z`},
		{"round.skipped_steps", skipped, `[]`},
	} {
		if c.got != c.want {
			t.Errorf("%s stored as %s, want %s", c.column, c.got, c.want)
		}
	}

	pv, err := db.AssignedVersion(ctx, "jan", training.NewDate(2026, 11, 22))
	if err == nil {
		t.Fatalf("jan has no assignment yet, got %+v", pv)
	}
}

func TestCorruptJSONIsAnError(t *testing.T) {
	db := open(t, filepath.Join(t.TempDir(), "test.db"))
	ctx := context.Background()
	if err := db.SavePerson(ctx, training.Person{Key: "jan", Name: "Jan", Language: "de"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveExercise(ctx, training.Exercise{Key: "x", Kind: training.KindExercise, Names: training.Text{"de": "X"}}); err != nil {
		t.Fatal(err)
	}
	spec := training.PlanSpec{Key: "p", Names: training.Text{"de": "P"}, Blocks: []training.BlockSpec{{
		Names: training.Text{"de": "B"}, Steps: []training.StepSpec{{Exercise: "x", Seconds: 5}},
	}}}
	if _, err := db.CreateVersion(ctx, spec, 1, "h", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.StartAssignment(ctx, "jan", "p", training.NewDate(2026, 11, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`UPDATE exercise SET names = '{broken'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssignedVersion(ctx, "jan", training.NewDate(2026, 11, 22)); err == nil || !strings.Contains(err.Error(), "corrupt JSON") {
		t.Fatalf("want a corrupt JSON error, got %v", err)
	}
}
