package importer

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/subpathdev/morgentraining/internal/planfile"
	"github.com/subpathdev/morgentraining/internal/training"
)

// fakeStore records the calls in order.
type fakeStore struct {
	hasPlans bool
	result   training.PlanResult
	err      error
	calls    []string
}

func (f *fakeStore) HasPlans(context.Context) (bool, error) { return f.hasPlans, nil }

func (f *fakeStore) ImportPlan(_ context.Context, imp training.PlanImport) (training.PlanResult, error) {
	f.calls = append(f.calls, "plan "+imp.Plan.Key)
	return f.result, f.err
}

func (f *fakeStore) ImportPeople(_ context.Context, people []training.PersonImport) error {
	f.calls = append(f.calls, "people")
	return f.err
}

const (
	planYAML   = "plan: p\nnames: {de: P}\nblocks:\n  - names: {de: B}\n    steps: [{exercise: x, seconds: 5}]\n"
	peopleYAML = "people:\n  - {key: jan, name: Jan, plan: p}\n"
)

func TestFileReportsWhatChanged(t *testing.T) {
	store := &fakeStore{result: training.PlanResult{PlanKey: "p", Version: 2, Created: true}}
	im := New(store, nil)
	ctx := context.Background()

	res, err := im.File(ctx, []byte(planYAML))
	if err != nil || res.Kind != planfile.KindPlan || res.String() != "plan p saved as version 2" {
		t.Fatalf("plan: %+v %q %v", res, res, err)
	}
	store.result.Created = false
	if res, _ := im.File(ctx, []byte(planYAML)); res.String() != "plan p unchanged (version 2)" {
		t.Fatalf("unchanged plan: %q", res)
	}
	if res, err := im.File(ctx, []byte(peopleYAML)); err != nil || res.String() != "1 people saved" {
		t.Fatalf("people: %q %v", res, err)
	}
}

func TestFilePassesErrorsOn(t *testing.T) {
	boom := errors.New("boom")
	im := New(&fakeStore{err: boom}, nil)
	if _, err := im.File(context.Background(), []byte(planYAML)); !errors.Is(err, boom) {
		t.Fatalf("store error: %v", err)
	}
	if _, err := im.File(context.Background(), []byte("neither: 1\n")); err == nil {
		t.Fatal("want a parse error")
	}
}

func TestSeedImportsPlansBeforePeople(t *testing.T) {
	store := &fakeStore{}
	// "a-people.yaml" sorts before "z-plan.yaml": the order must come from the content.
	files := fstest.MapFS{"a-people.yaml": {Data: []byte(peopleYAML)}, "z-plan.yaml": {Data: []byte(planYAML)}, "notes.txt": {}}
	if err := New(store, nil).SeedIfEmpty(context.Background(), files); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 2 || store.calls[0] != "plan p" || store.calls[1] != "people" {
		t.Fatalf("calls: %v", store.calls)
	}
}

func TestSeedDoesNothingWhenPlansExist(t *testing.T) {
	store := &fakeStore{hasPlans: true}
	if err := New(store, nil).SeedIfEmpty(context.Background(), fstest.MapFS{"plan.yaml": {Data: []byte(planYAML)}}); err != nil {
		t.Fatal(err)
	}
	if len(store.calls) != 0 {
		t.Fatalf("calls: %v", store.calls)
	}
}

func TestSeedWithABrokenFileImportsNothing(t *testing.T) {
	store := &fakeStore{}
	files := fstest.MapFS{"a-plan.yaml": {Data: []byte(planYAML)}, "b-broken.yaml": {Data: []byte("plan: [")}}
	if err := New(store, nil).SeedIfEmpty(context.Background(), files); err == nil {
		t.Fatal("want an error")
	}
	if len(store.calls) != 0 {
		t.Fatalf("nothing may be imported before all files parse: %v", store.calls)
	}
}
