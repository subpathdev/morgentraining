package main

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

// importFile imports one plan or people file and describes what changed.
func importFile(ctx context.Context, svc *training.Service, data []byte) (string, error) {
	f, err := planfile.Parse(data)
	if err != nil {
		return "", err
	}
	return importParsed(ctx, svc, f)
}

func importParsed(ctx context.Context, svc *training.Service, f planfile.File) (string, error) {
	switch f.Kind {
	case planfile.KindPlan:
		res, err := svc.ImportPlan(ctx, f.Plan)
		if err != nil {
			return "", err
		}
		if !res.Created {
			return fmt.Sprintf("plan %s unchanged (version %d)", res.PlanKey, res.Version), nil
		}
		return fmt.Sprintf("plan %s saved as version %d", res.PlanKey, res.Version), nil
	case planfile.KindPeople:
		if err := svc.ImportPeople(ctx, f.People); err != nil {
			return "", err
		}
		return fmt.Sprintf("%d people saved", len(f.People)), nil
	}
	return "", fmt.Errorf("unexpected file kind %s", f.Kind)
}

// seedIfEmpty imports every YAML file in files into a database without plans.
// Plans go first, because people refer to them.
func seedIfEmpty(ctx context.Context, svc *training.Service, files fs.FS, log *slog.Logger) error {
	has, err := svc.HasPlans(ctx)
	if err != nil || has {
		return err
	}
	names, err := fs.Glob(files, "*.yaml")
	if err != nil {
		return err
	}
	type parsed struct {
		name string
		file planfile.File
	}
	all := make([]parsed, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		f, err := planfile.Parse(data)
		if err != nil {
			return fmt.Errorf("seed %s: %w", name, err)
		}
		all = append(all, parsed{name, f})
	}
	slices.SortStableFunc(all, func(a, b parsed) int { return cmp.Compare(a.file.Kind, b.file.Kind) })
	for _, p := range all {
		msg, err := importParsed(ctx, svc, p.file)
		if err != nil {
			return fmt.Errorf("seed %s: %w", p.name, err)
		}
		log.Info("seeded", "file", p.name, "result", msg)
	}
	return nil
}
