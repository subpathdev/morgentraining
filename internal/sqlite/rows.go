package sqlite

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/subpathdev/morgentraining/internal/training"
)

// The types in this file are the storage format. They mirror the tables in
// schema.sql, not the domain, and are mapped to and from training types here.
// Changing a domain type therefore never changes what is written to disk.

// ---- column types ----

// textColumn stores one string per language as a JSON object, e.g. {"de":"Aufwärmen"}.
type textColumn map[string]string

func (t textColumn) Value() (driver.Value, error) { return jsonValue(map[string]string(t), "{}") }
func (t *textColumn) Scan(src any) error          { return scanJSON(src, (*map[string]string)(t)) }

func textFrom(t training.Text) textColumn { return textColumn(maps.Clone(t)) }
func (t textColumn) toDomain() training.Text {
	if len(t) == 0 {
		return nil
	}
	return training.Text(maps.Clone(t))
}

// variantsColumn stores exercise variants as {"knees":{"de":"auf den Knien"}}.
type variantsColumn map[string]textColumn

func (v variantsColumn) Value() (driver.Value, error) {
	return jsonValue(map[string]textColumn(v), "{}")
}
func (v *variantsColumn) Scan(src any) error { return scanJSON(src, (*map[string]textColumn)(v)) }

// intsColumn stores a list of positions as a JSON array.
type intsColumn []int

func (c intsColumn) Value() (driver.Value, error) { return jsonValue([]int(c), "[]") }
func (c *intsColumn) Scan(src any) error          { return scanJSON(src, (*[]int)(c)) }

// dateColumn stores a day as TEXT in the form YYYY-MM-DD, so dates compare as strings.
type dateColumn struct{ training.Date }

func (d dateColumn) Value() (driver.Value, error) { return d.String(), nil }
func (d *dateColumn) Scan(src any) error {
	s, err := text(src)
	if err != nil {
		return err
	}
	d.Date, err = training.ParseDate(s)
	return err
}

// timeColumn stores an instant as RFC 3339 TEXT in UTC; the zero time is stored as ”.
type timeColumn struct{ time.Time }

func (t timeColumn) Value() (driver.Value, error) {
	if t.IsZero() {
		return "", nil
	}
	return t.UTC().Format(time.RFC3339), nil
}

func (t *timeColumn) Scan(src any) error {
	s, err := text(src)
	if err != nil || s == "" {
		t.Time = time.Time{}
		return err
	}
	t.Time, err = time.Parse(time.RFC3339, s)
	return err
}

func jsonValue[T any](v T, empty string) (driver.Value, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return empty, nil
	}
	return string(b), nil
}

func scanJSON(src, dst any) error {
	s, err := text(src)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(s), dst); err != nil {
		return fmt.Errorf("corrupt JSON column %q: %w", s, err)
	}
	return nil
}

func text(src any) (string, error) {
	switch v := src.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	}
	return "", fmt.Errorf("expected a TEXT column, got %T", src)
}

// ---- rows ----

type personRow struct {
	Key, Name, Language string
}

func personRowFrom(p training.Person) personRow {
	return personRow{Key: p.Key, Name: p.Name, Language: p.Language}
}

func (r personRow) toDomain() training.Person {
	return training.Person{Key: r.Key, Name: r.Name, Language: r.Language}
}

type exerciseRow struct {
	Key, Kind   string
	Names, Cues textColumn
	Variants    variantsColumn
}

func exerciseRowFrom(e training.Exercise) exerciseRow {
	r := exerciseRow{Key: e.Key, Kind: string(e.Kind), Names: textFrom(e.Names), Cues: textFrom(e.Cues)}
	if e.Variants != nil {
		r.Variants = make(variantsColumn, len(e.Variants))
		for k, v := range e.Variants {
			r.Variants[k] = textFrom(v)
		}
	}
	return r
}

func (r exerciseRow) toDomain() training.Exercise {
	e := training.Exercise{Key: r.Key, Kind: training.StepKind(r.Kind), Names: r.Names.toDomain(), Cues: r.Cues.toDomain()}
	if len(r.Variants) > 0 {
		e.Variants = make(map[string]training.Text, len(r.Variants))
		for k, v := range r.Variants {
			e.Variants[k] = v.toDomain()
		}
	}
	return e
}

// versionRow is a plan_version joined with its plan's key.
type versionRow struct {
	ID      int64
	PlanKey string
	Version int
	Names   textColumn
}

type blockRow struct {
	Position         int
	Names, Summaries textColumn
}

func blockRowFrom(position int, b training.BlockSpec) blockRow {
	return blockRow{Position: position, Names: textFrom(b.Names), Summaries: textFrom(b.Summaries)}
}

type stepRow struct {
	Position    int
	Exercise    string // exercise key, resolved to exercise_id in SQL
	DurationSec int
	Variant     string
	Names, Cues textColumn
}

func stepRowFrom(position int, s training.StepSpec) stepRow {
	return stepRow{Position: position, Exercise: s.Exercise, DurationSec: s.Seconds, Variant: s.Variant,
		Names: textFrom(s.Names), Cues: textFrom(s.Cues)}
}

func (r stepRow) toDomain() training.StepSpec {
	return training.StepSpec{Exercise: r.Exercise, Seconds: r.DurationSec, Variant: r.Variant,
		Names: r.Names.toDomain(), Cues: r.Cues.toDomain()}
}

// planVersionFrom assembles the domain value from the rows of one plan version.
// steps[i] belongs to blocks[i]; blocks are in position order.
func planVersionFrom(v versionRow, blocks []blockRow, steps [][]stepRow, exercises []exerciseRow) training.PlanVersion {
	pv := training.PlanVersion{
		ID: v.ID, PlanKey: v.PlanKey, Version: v.Version, Names: v.Names.toDomain(),
		Blocks:    make([]training.BlockSpec, len(blocks)),
		Exercises: make(map[string]training.Exercise, len(exercises)),
	}
	for i, b := range blocks {
		block := training.BlockSpec{Names: b.Names.toDomain(), Summaries: b.Summaries.toDomain(), Steps: make([]training.StepSpec, len(steps[i]))}
		for j, s := range steps[i] {
			block.Steps[j] = s.toDomain()
		}
		pv.Blocks[i] = block
	}
	for _, e := range exercises {
		pv.Exercises[e.Key] = e.toDomain()
	}
	return pv
}

type assignmentRow struct {
	ID        int64
	PlanKey   string
	ValidFrom dateColumn
}

func (r assignmentRow) toDomain() training.Assignment {
	return training.Assignment{ID: r.ID, PlanKey: r.PlanKey, From: r.ValidFrom.Date}
}

type roundRow struct {
	PlanVersionID         int64
	Date                  dateColumn
	StartedAt, RecordedAt timeColumn
	Completed             bool
	SkippedSteps          intsColumn
}

func roundRowFrom(r training.Round) roundRow {
	skipped := intsColumn(r.SkippedSteps)
	if skipped == nil {
		skipped = intsColumn{}
	}
	return roundRow{
		PlanVersionID: r.PlanVersionID,
		Date:          dateColumn{r.Date},
		StartedAt:     timeColumn{r.StartedAt},
		RecordedAt:    timeColumn{r.RecordedAt},
		Completed:     r.Completed,
		SkippedSteps:  skipped,
	}
}
