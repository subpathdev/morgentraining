package training_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/subpathdev/morgentraining/internal/planfile"
	"github.com/subpathdev/morgentraining/internal/sqlite"
	"github.com/subpathdev/morgentraining/internal/training"
)

// These tests run the service against a real SQLite file, because most of what
// can go wrong in a use case is in how it and the storage work together.

type clock struct{ now time.Time }

func (c *clock) Now() time.Time      { return c.now }
func (c *clock) set(day string)      { c.now = mustDay(day).Time(time.UTC).Add(6 * time.Hour) }
func mustDay(s string) training.Date { d, err := training.ParseDate(s); must(err); return d }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func newService(t *testing.T, today string) (*training.Service, *clock) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{}
	c.set(today)
	return training.NewService(db, time.UTC, training.WithClock(c.Now)), c
}

func importFile(t *testing.T, svc *training.Service, data []byte) training.PlanResult {
	t.Helper()
	f, err := planfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	switch f.Kind {
	case planfile.KindPlan:
		res, err := svc.ImportPlan(ctx, f.Plan)
		if err != nil {
			t.Fatal(err)
		}
		return res
	default:
		if err := svc.ImportPeople(ctx, f.People); err != nil {
			t.Fatal(err)
		}
		return training.PlanResult{}
	}
}

func seedFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "seed", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func seed(t *testing.T, svc *training.Service) {
	t.Helper()
	importFile(t, svc, seedFile(t, "plan-morgen-5.yaml"))
	importFile(t, svc, seedFile(t, "people.yaml"))
}

func session(t *testing.T, svc *training.Service, day string) training.Session {
	t.Helper()
	s, err := svc.Session(context.Background(), "jan", mustDay(day))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSeedPlanMatchesOriginalPage(t *testing.T) {
	svc, _ := newService(t, "2026-11-22")
	seed(t, svc)
	sess := session(t, svc, "2026-11-22")
	if sess.Plan.TotalSeconds != 300 || len(sess.Blocks) != 5 || sess.Plan.Version != 1 {
		t.Fatalf("want 5 blocks, 300 s, version 1; got %d blocks, %d s, version %d", len(sess.Blocks), sess.Plan.TotalSeconds, sess.Plan.Version)
	}
	wall := sess.Blocks[3]
	if len(wall.Steps) != 3 || wall.Steps[1].Kind != training.KindRest || wall.Steps[0].Name != "Wandsitzen 1 von 2" {
		t.Fatalf("wall sit block: %+v", wall)
	}
	if sess.Blocks[4].Steps[1].Kind != training.KindBreath {
		t.Fatalf("last step should be breathing: %+v", sess.Blocks[4])
	}
}

func TestReimportCreatesVersionOnlyOnChange(t *testing.T) {
	svc, c := newService(t, "2026-11-22")
	seed(t, svc)
	c.set("2026-11-23")
	data := seedFile(t, "plan-morgen-5.yaml")

	if res := importFile(t, svc, data); res.Created || res.Version != 1 {
		t.Fatalf("same content: want unchanged version 1, got %+v", res)
	}
	longer := strings.Replace(string(data), "{exercise: half-squat, seconds: 60}", "{exercise: half-squat, seconds: 90}", 1)
	if res := importFile(t, svc, []byte(longer)); !res.Created || res.Version != 2 {
		t.Fatalf("changed content: want version 2, got %+v", res)
	}
	if sess := session(t, svc, "2026-11-23"); sess.Plan.Version != 2 || sess.Plan.TotalSeconds != 330 {
		t.Fatalf("want version 2 with 330 s, got v%d %d s", sess.Plan.Version, sess.Plan.TotalSeconds)
	}
}

func TestRoundsKeepTheirVersionAndCountTheStreak(t *testing.T) {
	svc, _ := newService(t, "2026-11-20")
	seed(t, svc)
	ctx := context.Background()
	v1 := session(t, svc, "2026-11-20").Plan.VersionID

	record := func(day string) training.Stats {
		t.Helper()
		st, err := svc.RecordRound(ctx, "jan", training.RoundInput{PlanVersionID: v1, Date: mustDay(day), Completed: true, SkippedSteps: []int{2}})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	record("2026-11-20")
	record("2026-11-21")
	record("2026-11-21")
	if st := session(t, svc, "2026-11-22").Stats; st != (training.Stats{Streak: 2, TotalDays: 2}) {
		t.Fatalf("before today's round: %+v", st)
	}
	if st := record("2026-11-22"); st != (training.Stats{Streak: 3, TotalDays: 3, DoneToday: true}) {
		t.Fatalf("after today's round: %+v", st)
	}

	// A new plan version doesn't change which version an earlier round used:
	// recording against v1 still works after v2 exists.
	longer := strings.Replace(string(seedFile(t, "plan-morgen-5.yaml")), "seconds: 60}", "seconds: 61}", 1)
	importFile(t, svc, []byte(longer))
	if _, err := svc.RecordRound(ctx, "jan", training.RoundInput{PlanVersionID: v1, Date: mustDay("2026-11-23"), Completed: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRecordRoundRejectsBadInput(t *testing.T) {
	svc, _ := newService(t, "2026-11-22")
	seed(t, svc)
	ctx := context.Background()
	v := session(t, svc, "2026-11-22").Plan.VersionID
	cases := []struct {
		name   string
		person string
		in     training.RoundInput
		want   error
	}{
		{"unknown person", "nobody", training.RoundInput{PlanVersionID: v, Date: mustDay("2026-11-22")}, training.ErrNotFound},
		{"unknown version", "jan", training.RoundInput{PlanVersionID: 999, Date: mustDay("2026-11-22")}, training.ErrInvalid},
		{"no date", "jan", training.RoundInput{PlanVersionID: v}, training.ErrInvalid},
		{"negative step", "jan", training.RoundInput{PlanVersionID: v, Date: mustDay("2026-11-22"), SkippedSteps: []int{-1}}, training.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.RecordRound(ctx, c.person, c.in); !errors.Is(err, c.want) {
				t.Fatalf("want %v, got %v", c.want, err)
			}
		})
	}
}

func TestPersonSwitchesPlanFromImportDay(t *testing.T) {
	svc, c := newService(t, "2026-11-20")
	seed(t, svc)
	c.set("2026-11-22")
	importFile(t, svc, []byte("plan: short\nnames: {de: Kurz}\nblocks:\n  - names: {de: Nur Aufwärmen}\n    steps: [{exercise: warmup, seconds: 30}]\n"))
	importFile(t, svc, []byte("people:\n  - {key: jan, name: Jan, language: en, plan: short}\n"))

	before, after := session(t, svc, "2026-11-21"), session(t, svc, "2026-11-22")
	if before.Plan.Key != "morgen-5" || after.Plan.Key != "short" {
		t.Fatalf("plan on 21st %s, on 22nd %s", before.Plan.Key, after.Plan.Key)
	}
	if after.Blocks[0].Steps[0].Name != "Warm-up" {
		t.Fatalf("language en should pick the English name, got %q", after.Blocks[0].Steps[0].Name)
	}

	// Switching again on the same day replaces today's plan instead of adding a one-day gap.
	importFile(t, svc, []byte("people:\n  - {key: jan, name: Jan, language: en, plan: morgen-5}\n"))
	if got := session(t, svc, "2026-11-22").Plan.Key; got != "morgen-5" {
		t.Fatalf("second switch on the same day: %s", got)
	}
	if got := session(t, svc, "2026-11-21").Plan.Key; got != "morgen-5" {
		t.Fatalf("the day before: %s", got)
	}
}

func TestInvalidPlanIsRejectedWithAllProblems(t *testing.T) {
	svc, _ := newService(t, "2026-11-22")
	f, err := planfile.Parse([]byte("plan: Bad Key\nblocks:\n  - steps: [{exercise: nope, seconds: 0}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ImportPlan(context.Background(), f.Plan)
	var verr *training.ValidationError
	if !errors.As(err, &verr) || !errors.Is(err, training.ErrInvalid) {
		t.Fatalf("want a ValidationError, got %v", err)
	}
	for _, want := range []string{"plan key", "plan needs a name", "block 1: needs a name", "unknown exercise", "seconds must be"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
	if ok, _ := svc.HasPlans(context.Background()); ok {
		t.Fatal("nothing may be saved from an invalid plan")
	}
}

func TestUnknownPlanInPeopleFileSavesNothing(t *testing.T) {
	svc, _ := newService(t, "2026-11-22")
	f, err := planfile.Parse([]byte("people:\n  - {key: mariana, name: Mariana}\n  - {key: jan, name: Jan, plan: missing}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ImportPeople(context.Background(), f.People); !errors.Is(err, training.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
	if people, _ := svc.People(context.Background()); len(people) != 0 {
		t.Fatalf("the import must be all or nothing, got %+v", people)
	}
}

func TestSavePersonAddsThenUpdates(t *testing.T) {
	svc, c := newService(t, "2026-11-20")
	seed(t, svc)
	importFile(t, svc, []byte("plan: short\nnames: {de: Kurz}\nblocks:\n  - names: {de: Nur Aufwärmen}\n    steps: [{exercise: warmup, seconds: 30}]\n"))
	ctx := context.Background()

	mariana := training.PersonImport{Person: training.Person{Key: "mariana", Name: "Mariana", Language: "en"}, Plan: "morgen-5"}
	if created, err := svc.SavePerson(ctx, mariana); err != nil || !created {
		t.Fatalf("first save: created=%v err=%v", created, err)
	}
	s, err := svc.Session(ctx, "mariana", mustDay("2026-11-20"))
	if err != nil || s.Plan.Key != "morgen-5" || s.Blocks[0].Name != "Warm-up" {
		t.Fatalf("new person's session: %+v %v", s.Plan, err)
	}

	// A later save with another plan switches from that day; earlier days keep the old plan.
	c.set("2026-11-22")
	mariana.Person.Name, mariana.Plan = "Mari", "short"
	if created, err := svc.SavePerson(ctx, mariana); err != nil || created {
		t.Fatalf("second save: created=%v err=%v", created, err)
	}
	if before, after := planOn(t, svc, "mariana", "2026-11-21"), planOn(t, svc, "mariana", "2026-11-22"); before != "morgen-5" || after != "short" {
		t.Fatalf("plan on 21st %s, on 22nd %s", before, after)
	}
	people, _ := svc.People(ctx)
	if len(people) != 2 || people[1].Name != "Mari" {
		t.Fatalf("people: %+v", people)
	}
}

func TestSavePersonRejectsBadInputAndSavesNothing(t *testing.T) {
	svc, _ := newService(t, "2026-11-22")
	seed(t, svc)
	ctx := context.Background()
	for name, p := range map[string]training.PersonImport{
		"unknown plan": {Person: training.Person{Key: "x", Name: "X", Language: "de"}, Plan: "nope"},
		"no name":      {Person: training.Person{Key: "x", Language: "de"}, Plan: "morgen-5"},
		"bad key":      {Person: training.Person{Key: "X Y", Name: "X", Language: "de"}, Plan: "morgen-5"},
	} {
		if _, err := svc.SavePerson(ctx, p); !errors.Is(err, training.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if people, _ := svc.People(ctx); len(people) != 1 {
		t.Fatalf("nothing may be saved from invalid input, got %+v", people)
	}
}

func planOn(t *testing.T, svc *training.Service, key, day string) string {
	t.Helper()
	s, err := svc.Session(context.Background(), key, mustDay(day))
	if err != nil {
		t.Fatal(err)
	}
	return s.Plan.Key
}

func TestPlansListTheNewestVersionOfEach(t *testing.T) {
	svc, _ := newService(t, "2026-11-22")
	seed(t, svc)
	importFile(t, svc, []byte("plan: a-short\nnames: {de: Kurz}\nblocks:\n  - names: {de: Nur Aufwärmen}\n    steps: [{exercise: warmup, seconds: 30}]\n"))
	longer := strings.Replace(string(seedFile(t, "plan-morgen-5.yaml")), "{exercise: half-squat, seconds: 60}", "{exercise: half-squat, seconds: 90}", 1)
	importFile(t, svc, []byte(longer))

	plans, err := svc.Plans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 || plans[0].PlanKey != "a-short" || plans[1].PlanKey != "morgen-5" {
		t.Fatalf("want both plans ordered by key, got %+v", plans)
	}
	if plans[1].Version != 2 || plans[1].TotalSeconds() != 330 || plans[0].TotalSeconds() != 30 {
		t.Fatalf("newest versions: %s v%d %ds, %s v%d %ds", plans[0].PlanKey, plans[0].Version, plans[0].TotalSeconds(),
			plans[1].PlanKey, plans[1].Version, plans[1].TotalSeconds())
	}

	pv, err := svc.Plan(context.Background(), "morgen-5")
	if err != nil || pv.Version != 2 || len(pv.Blocks) != 5 || pv.Exercises["push-up"].Variants["knees"]["de"] != "auf den Knien" {
		t.Fatalf("plan: v%d %d blocks, push-up %+v, %v", pv.Version, len(pv.Blocks), pv.Exercises["push-up"], err)
	}
	if _, err := svc.Plan(context.Background(), "nope"); !errors.Is(err, training.ErrNotFound) {
		t.Fatalf("unknown plan: %v", err)
	}
}
