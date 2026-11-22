// Package planfile reads the YAML files plan authors write: a plan file
// (top-level key "plan") or a people file (top-level key "people").
//
// It only translates the file format into training values and applies the
// file's defaults; the domain rules are checked by training.Service.
package planfile

import (
	"bytes"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"

	"github.com/subpathdev/morgentraining/internal/training"
)

// Kind tells which kind of file was read.
type Kind int

const (
	KindPlan Kind = iota + 1
	KindPeople
)

func (k Kind) String() string {
	switch k {
	case KindPlan:
		return "plan"
	case KindPeople:
		return "people"
	}
	return "unknown"
}

// File is a parsed file. Plan is set for KindPlan, People for KindPeople.
type File struct {
	Kind   Kind
	Plan   training.PlanImport
	People []training.PersonImport
}

// Parse reads a plan or people file. Unknown fields are an error, so typos don't go unnoticed.
func Parse(data []byte) (File, error) {
	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		return File{}, fmt.Errorf("parse YAML: %w", err)
	}
	switch {
	case top["plan"] != nil:
		var p planDoc
		if err := decodeStrict(data, &p); err != nil {
			return File{}, fmt.Errorf("parse plan: %w", err)
		}
		return File{Kind: KindPlan, Plan: p.toDomain()}, nil
	case top["people"] != nil:
		var p peopleDoc
		if err := decodeStrict(data, &p); err != nil {
			return File{}, fmt.Errorf("parse people: %w", err)
		}
		return File{Kind: KindPeople, People: p.toDomain()}, nil
	}
	return File{}, errors.New("unknown file: expected a top-level 'plan' or 'people' key")
}

func decodeStrict(data []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(v)
}

type planDoc struct {
	Plan      string        `yaml:"plan"`
	Names     training.Text `yaml:"names"`
	CreatedBy string        `yaml:"createdBy"`
	Exercises []exerciseDoc `yaml:"exercises"`
	Blocks    []blockDoc    `yaml:"blocks"`
}

type exerciseDoc struct {
	Key      string                   `yaml:"key"`
	Kind     string                   `yaml:"kind"` // default: exercise
	Names    training.Text            `yaml:"names"`
	Cues     training.Text            `yaml:"cues"`
	Variants map[string]training.Text `yaml:"variants"`
}

type blockDoc struct {
	Names   training.Text `yaml:"names"`
	Summary training.Text `yaml:"summary"`
	Steps   []stepDoc     `yaml:"steps"`
}

type stepDoc struct {
	Exercise string        `yaml:"exercise"`
	Seconds  int           `yaml:"seconds"`
	Variant  string        `yaml:"variant"`
	Names    training.Text `yaml:"names"`
	Cues     training.Text `yaml:"cues"`
}

func (d planDoc) toDomain() training.PlanImport {
	imp := training.PlanImport{
		Plan: training.PlanSpec{Key: d.Plan, Names: d.Names, CreatedBy: d.CreatedBy, Blocks: make([]training.BlockSpec, len(d.Blocks))},
	}
	for _, e := range d.Exercises {
		kind := training.StepKind(e.Kind)
		if kind == "" {
			kind = training.KindExercise
		}
		imp.Exercises = append(imp.Exercises, training.Exercise{Key: e.Key, Kind: kind, Names: e.Names, Cues: e.Cues, Variants: e.Variants})
	}
	for i, b := range d.Blocks {
		block := training.BlockSpec{Names: b.Names, Summaries: b.Summary, Steps: make([]training.StepSpec, len(b.Steps))}
		for j, s := range b.Steps {
			block.Steps[j] = training.StepSpec{Exercise: s.Exercise, Seconds: s.Seconds, Variant: s.Variant, Names: s.Names, Cues: s.Cues}
		}
		imp.Plan.Blocks[i] = block
	}
	return imp
}

type peopleDoc struct {
	People []personDoc `yaml:"people"`
}

type personDoc struct {
	Key      string `yaml:"key"`
	Name     string `yaml:"name"`
	Language string `yaml:"language"` // default: training.DefaultLanguage
	Plan     string `yaml:"plan"`     // empty keeps the current plan
}

func (d peopleDoc) toDomain() []training.PersonImport {
	out := make([]training.PersonImport, len(d.People))
	for i, p := range d.People {
		lang := p.Language
		if lang == "" {
			lang = training.DefaultLanguage
		}
		out[i] = training.PersonImport{Person: training.Person{Key: p.Key, Name: p.Name, Language: lang}, Plan: p.Plan}
	}
	return out
}
