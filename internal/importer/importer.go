// Package importer turns plan and people files into calls to the training
// service: for the import command, and to seed an empty database on first start.
package importer

import (
	"cmp"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"

	"github.com/subpathdev/morgentraining/internal/planfile"
	"github.com/subpathdev/morgentraining/internal/training"
)

// PlanStore is what the importer needs from the domain. *training.Service implements it.
type PlanStore interface {
	HasPlans(ctx context.Context) (bool, error)
	ImportPlan(ctx context.Context, imp training.PlanImport) (training.PlanResult, error)
	ImportPeople(ctx context.Context, people []training.PersonImport) error
}

var _ PlanStore = (*training.Service)(nil)

// Result says what importing one file changed.
type Result struct {
	Kind   planfile.Kind
	Plan   training.PlanResult // for planfile.KindPlan
	People int                 // for planfile.KindPeople: people added or updated
}

func (r Result) String() string {
	switch {
	case r.Kind == planfile.KindPeople:
		return fmt.Sprintf("%d people saved", r.People)
	case r.Plan.Created:
		return fmt.Sprintf("plan %s saved as version %d", r.Plan.PlanKey, r.Plan.Version)
	default:
		return fmt.Sprintf("plan %s unchanged (version %d)", r.Plan.PlanKey, r.Plan.Version)
	}
}

// Importer imports files into a PlanStore.
type Importer struct {
	store PlanStore
	log   *slog.Logger
}

// New returns an Importer. A nil logger discards logs.
func New(store PlanStore, log *slog.Logger) *Importer {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Importer{store: store, log: log}
}

// File imports one plan or people file.
func (im *Importer) File(ctx context.Context, data []byte) (Result, error) {
	f, err := planfile.Parse(data)
	if err != nil {
		return Result{}, err
	}
	return im.parsed(ctx, f)
}

func (im *Importer) parsed(ctx context.Context, f planfile.File) (Result, error) {
	switch f.Kind {
	case planfile.KindPlan:
		res, err := im.store.ImportPlan(ctx, f.Plan)
		return Result{Kind: f.Kind, Plan: res}, err
	case planfile.KindPeople:
		err := im.store.ImportPeople(ctx, f.People)
		return Result{Kind: f.Kind, People: len(f.People)}, err
	}
	return Result{}, fmt.Errorf("unexpected file kind %s", f.Kind)
}

// SeedIfEmpty imports every *.yaml file in files if no plan exists yet.
// All files are parsed first, so a broken file imports nothing; then plans go
// before people, because people refer to plans.
func (im *Importer) SeedIfEmpty(ctx context.Context, files fs.FS) error {
	has, err := im.store.HasPlans(ctx)
	if err != nil || has {
		return err
	}
	names, err := fs.Glob(files, "*.yaml")
	if err != nil {
		return err
	}
	type parsedFile struct {
		name string
		file planfile.File
	}
	all := make([]parsedFile, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		f, err := planfile.Parse(data)
		if err != nil {
			return fmt.Errorf("seed %s: %w", name, err)
		}
		all = append(all, parsedFile{name, f})
	}
	slices.SortStableFunc(all, func(a, b parsedFile) int { return cmp.Compare(a.file.Kind, b.file.Kind) })
	for _, p := range all {
		res, err := im.parsed(ctx, p.file)
		if err != nil {
			return fmt.Errorf("seed %s: %w", p.name, err)
		}
		im.log.Info("seeded", "file", p.name, "result", res.String())
	}
	return nil
}
