package store

import (
	"fmt"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Text holds one string per language, e.g. {"de": "Aufwärmen", "en": "Warm-up"}.
type Text map[string]string

// Pick returns the text for lang, falling back to German, English, then any language.
func (t Text) Pick(lang string) string {
	for _, l := range []string{lang, "de", "en"} {
		if v, ok := t[l]; ok && v != "" {
			return v
		}
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if t[k] != "" {
			return t[k]
		}
	}
	return ""
}

// PlanFile is the YAML format for importing a plan. Importing the same plan key
// again with different content saves a new version; identical content is a no-op.
type PlanFile struct {
	Plan      string        `yaml:"plan" json:"plan"`
	Names     Text          `yaml:"names" json:"names"`
	CreatedBy string        `yaml:"createdBy" json:"createdBy,omitempty"`
	Exercises []ExerciseDef `yaml:"exercises" json:"exercises,omitempty"`
	Blocks    []BlockDef    `yaml:"blocks" json:"blocks"`
}

type ExerciseDef struct {
	Key      string          `yaml:"key" json:"key"`
	Kind     string          `yaml:"kind" json:"kind,omitempty"` // exercise (default) | rest | breath
	Names    Text            `yaml:"names" json:"names"`
	Cues     Text            `yaml:"cues" json:"cues,omitempty"`
	Variants map[string]Text `yaml:"variants" json:"variants,omitempty"`
}

type BlockDef struct {
	Names   Text      `yaml:"names" json:"names"`
	Summary Text      `yaml:"summary" json:"summary,omitempty"`
	Steps   []StepDef `yaml:"steps" json:"steps"`
}

type StepDef struct {
	Exercise string `yaml:"exercise" json:"exercise"`
	Seconds  int    `yaml:"seconds" json:"seconds"`
	Variant  string `yaml:"variant" json:"variant,omitempty"`
	Names    Text   `yaml:"names" json:"names,omitempty"` // overrides the exercise name for this step
	Cues     Text   `yaml:"cues" json:"cues,omitempty"`   // overrides the exercise cue for this step
}

// PeopleFile is the YAML format for adding people and assigning plans.
type PeopleFile struct {
	People []PersonDef `yaml:"people"`
}

type PersonDef struct {
	Key      string `yaml:"key"`
	Name     string `yaml:"name"`
	Language string `yaml:"language"`
	Plan     string `yaml:"plan"` // plan key to follow from today; empty keeps the current one
}

// fileKind reports whether a YAML document is a plan or a people file.
func fileKind(data []byte) (string, error) {
	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		return "", fmt.Errorf("parse YAML: %w", err)
	}
	switch {
	case top["plan"] != nil:
		return "plan", nil
	case top["people"] != nil:
		return "people", nil
	}
	return "", fmt.Errorf("unknown file: expected a top-level 'plan' or 'people' key")
}

func validKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (p *PlanFile) validate(known func(key string) bool) error {
	var errs []string
	if !validKey(p.Plan) {
		errs = append(errs, fmt.Sprintf("plan key %q: use lowercase letters, digits, - or _", p.Plan))
	}
	if p.Names.Pick("") == "" {
		errs = append(errs, "plan needs a name (names: {de: ...})")
	}
	defined := map[string]bool{}
	for i, e := range p.Exercises {
		if !validKey(e.Key) {
			errs = append(errs, fmt.Sprintf("exercise %d: key %q is not valid", i+1, e.Key))
		}
		switch e.Kind {
		case "", "exercise", "rest", "breath":
		default:
			errs = append(errs, fmt.Sprintf("exercise %s: kind %q must be exercise, rest or breath", e.Key, e.Kind))
		}
		if e.Names.Pick("") == "" {
			errs = append(errs, fmt.Sprintf("exercise %s: needs a name", e.Key))
		}
		defined[e.Key] = true
	}
	if len(p.Blocks) == 0 {
		errs = append(errs, "plan needs at least one block")
	}
	for i, b := range p.Blocks {
		if b.Names.Pick("") == "" {
			errs = append(errs, fmt.Sprintf("block %d: needs a name", i+1))
		}
		if len(b.Steps) == 0 {
			errs = append(errs, fmt.Sprintf("block %d: needs at least one step", i+1))
		}
		for j, s := range b.Steps {
			if s.Seconds <= 0 || s.Seconds > 3600 {
				errs = append(errs, fmt.Sprintf("block %d step %d: seconds must be 1-3600", i+1, j+1))
			}
			if !defined[s.Exercise] && !known(s.Exercise) {
				errs = append(errs, fmt.Sprintf("block %d step %d: unknown exercise %q", i+1, j+1, s.Exercise))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid plan:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
