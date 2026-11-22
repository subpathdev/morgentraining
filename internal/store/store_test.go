package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seed(t *testing.T, s *Store, today string) {
	t.Helper()
	for _, f := range []string{"plan-morgen-5.yaml", "people.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "seed", f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Import(context.Background(), data, today); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

func TestSeedPlanMatchesOriginalPage(t *testing.T) {
	s := open(t)
	seed(t, s, "2026-11-22")
	sess, err := s.Today(context.Background(), "jan", "2026-11-22")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Plan.TotalSeconds != 300 || len(sess.Blocks) != 5 || sess.Plan.Version != 1 {
		t.Fatalf("want 5 blocks, 300 s, version 1; got %d blocks, %d s, version %d", len(sess.Blocks), sess.Plan.TotalSeconds, sess.Plan.Version)
	}
	wall := sess.Blocks[3]
	if len(wall.Steps) != 3 || wall.Steps[1].Kind != "rest" || wall.Steps[0].Name != "Wandsitzen 1 von 2" {
		t.Fatalf("wall sit block: %+v", wall)
	}
	if sess.Blocks[4].Steps[1].Kind != "breath" {
		t.Fatalf("last step should be breathing: %+v", sess.Blocks[4])
	}
}

func TestReimportCreatesVersionOnlyOnChange(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s, "2026-11-22")
	data, _ := os.ReadFile(filepath.Join("..", "..", "seed", "plan-morgen-5.yaml"))

	res, err := s.Import(ctx, data, "2026-11-23")
	if err != nil || res.Created || res.Version != 1 {
		t.Fatalf("same content: want unchanged version 1, got %+v %v", res, err)
	}

	longer := strings.Replace(string(data), "{exercise: half-squat, seconds: 60}", "{exercise: half-squat, seconds: 90}", 1)
	res, err = s.Import(ctx, []byte(longer), "2026-11-23")
	if err != nil || !res.Created || res.Version != 2 {
		t.Fatalf("changed content: want version 2, got %+v %v", res, err)
	}

	sess, err := s.Today(ctx, "jan", "2026-11-23")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Plan.Version != 2 || sess.Plan.TotalSeconds != 330 {
		t.Fatalf("want version 2 with 330 s, got v%d %d s", sess.Plan.Version, sess.Plan.TotalSeconds)
	}
}

func TestRoundKeepsItsVersionAndStreak(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s, "2026-11-20")
	v1, _ := s.Today(ctx, "jan", "2026-11-20")

	for _, d := range []string{"2026-11-20", "2026-11-21", "2026-11-21"} {
		if _, err := s.AddRound(ctx, "jan", RoundInput{PlanVersionID: v1.Plan.VersionID, Date: d, Completed: true}); err != nil {
			t.Fatal(err)
		}
	}
	// Morning of the 22nd, no round yet: the streak still counts up to yesterday.
	sess, _ := s.Today(ctx, "jan", "2026-11-22")
	if sess.Stats.Streak != 2 || sess.Stats.TotalDays != 2 || sess.Stats.DoneToday {
		t.Fatalf("before today's round: %+v", sess.Stats)
	}
	st, err := s.AddRound(ctx, "jan", RoundInput{PlanVersionID: v1.Plan.VersionID, Date: "2026-11-22", Completed: true, SkippedSteps: []int{2}})
	if err != nil || st.Streak != 3 || !st.DoneToday {
		t.Fatalf("after today's round: %+v %v", st, err)
	}
	// A gap breaks the streak.
	sess, _ = s.Today(ctx, "jan", "2026-11-25")
	if sess.Stats.Streak != 0 || sess.Stats.TotalDays != 3 {
		t.Fatalf("after a gap: %+v", sess.Stats)
	}
	var stored int64
	s.db.QueryRow("SELECT plan_version_id FROM round WHERE date = '2026-11-22'").Scan(&stored)
	if stored != v1.Plan.VersionID {
		t.Fatalf("round points to version %d, want %d", stored, v1.Plan.VersionID)
	}
}

func TestPersonSwitchesPlanFromImportDay(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	seed(t, s, "2026-11-20")
	other := `plan: short
names: {de: Kurz}
blocks:
  - names: {de: Nur Aufwärmen}
    steps: [{exercise: warmup, seconds: 30}]
`
	if _, err := s.Import(ctx, []byte(other), "2026-11-22"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, []byte("people:\n  - {key: jan, name: Jan, language: en, plan: short}\n"), "2026-11-22"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Today(ctx, "jan", "2026-11-21")
	after, _ := s.Today(ctx, "jan", "2026-11-22")
	if before.Plan.Key != "morgen-5" || after.Plan.Key != "short" {
		t.Fatalf("plan on 21st %s, on 22nd %s", before.Plan.Key, after.Plan.Key)
	}
	if after.Blocks[0].Steps[0].Name != "Warm-up" {
		t.Fatalf("language en should pick the English name, got %q", after.Blocks[0].Steps[0].Name)
	}
}

func TestInvalidPlanIsRejected(t *testing.T) {
	s := open(t)
	_, err := s.Import(context.Background(), []byte("plan: Bad Key\nblocks:\n  - steps: [{exercise: nope, seconds: 0}]\n"), "2026-11-22")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"plan key", "unknown exercise", "seconds must be"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestBackup(t *testing.T) {
	s := open(t)
	seed(t, s, "2026-11-22")
	target := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	b, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if empty, _ := b.Empty(context.Background()); empty {
		t.Fatal("backup has no plans")
	}
}
