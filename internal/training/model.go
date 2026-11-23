package training

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// DefaultLanguage is used for people without a language and as the first fallback for texts.
const DefaultLanguage = "de"

// Text holds one string per language, e.g. {"de": "Aufwärmen", "en": "Warm-up"}.
type Text map[string]string

// Pick returns the text for lang, falling back to German, English, then any
// language in alphabetical order. It returns "" for an empty Text.
func (t Text) Pick(lang string) string {
	for _, l := range []string{lang, DefaultLanguage, "en"} {
		if v := t[l]; v != "" {
			return v
		}
	}
	langs := make([]string, 0, len(t))
	for l, v := range t {
		if v != "" {
			langs = append(langs, l)
		}
	}
	if len(langs) == 0 {
		return ""
	}
	slices.Sort(langs)
	return t[langs[0]]
}

// StepKind says how the page shows a step.
type StepKind string

const (
	KindExercise StepKind = "exercise"
	KindRest     StepKind = "rest"   // a short break inside a block
	KindBreath   StepKind = "breath" // shows breathe in / breathe out
)

func (k StepKind) Valid() bool {
	switch k {
	case KindExercise, KindRest, KindBreath:
		return true
	}
	return false
}

// Person is someone who trains. Key identifies them; Language picks their texts.
type Person struct {
	Key      string
	Name     string
	Language string
}

func (p Person) problems() []string {
	var out []string
	if !ValidKey(p.Key) {
		out = append(out, fmt.Sprintf("person key %q: use lowercase letters, digits, - or _", p.Key))
	}
	if p.Name == "" {
		out = append(out, fmt.Sprintf("person %s: needs a name", p.Key))
	}
	if p.Language == "" {
		out = append(out, fmt.Sprintf("person %s: needs a language", p.Key))
	}
	return out
}

// Exercise is an entry in the exercise library shared by all plans.
type Exercise struct {
	Key      string
	Kind     StepKind
	Names    Text
	Cues     Text
	Variants map[string]Text // e.g. "knees": {"de": "auf den Knien"}
}

// PlanSpec is the content of a plan as an author writes it. Saving a PlanSpec
// whose Hash differs from the newest version creates a new PlanVersion.
type PlanSpec struct {
	Key       string
	Names     Text
	CreatedBy string
	Blocks    []BlockSpec
}

// BlockSpec is one item in the exercise list and one segment of the sun ring.
type BlockSpec struct {
	Names     Text
	Summaries Text
	Steps     []StepSpec
}

// StepSpec is a timed step that uses an exercise from the library.
type StepSpec struct {
	Exercise string // exercise key
	Seconds  int
	Variant  string // optional key into Exercise.Variants
	Names    Text   // optional override of the exercise name
	Cues     Text   // optional override of the exercise cue
}

// Hash identifies the content a round depends on: the plan names, blocks and
// steps, but not the exercise library texts.
//
// The JSON shape below is stored with every plan version. Changing it makes the
// next import of every unchanged plan create a new version.
func (p PlanSpec) Hash() string {
	type step struct {
		Exercise string `json:"exercise"`
		Seconds  int    `json:"seconds"`
		Variant  string `json:"variant,omitempty"`
		Names    Text   `json:"names,omitempty"`
		Cues     Text   `json:"cues,omitempty"`
	}
	type block struct {
		Names   Text   `json:"names"`
		Summary Text   `json:"summary,omitempty"`
		Steps   []step `json:"steps"`
	}
	content := struct {
		Names  Text
		Blocks []block
	}{Names: p.Names, Blocks: make([]block, len(p.Blocks))}
	for i, b := range p.Blocks {
		content.Blocks[i] = block{Names: b.Names, Summary: b.Summaries, Steps: make([]step, len(b.Steps))}
		for j, s := range b.Steps {
			content.Blocks[i].Steps[j] = step(s)
		}
	}
	data, err := json.Marshal(content)
	if err != nil {
		panic(err) // only maps of strings, ints and slices: cannot fail
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PlanVersion is a saved, immutable version of a plan together with the
// exercises its steps use.
type PlanVersion struct {
	ID        int64 // global ID; rounds refer to it
	PlanKey   string
	Version   int // 1, 2, … within the plan
	Names     Text
	Blocks    []BlockSpec
	Exercises map[string]Exercise
}

// TotalSeconds is the sum of all steps.
func (pv PlanVersion) TotalSeconds() int {
	total := 0
	for _, b := range pv.Blocks {
		for _, s := range b.Steps {
			total += s.Seconds
		}
	}
	return total
}

// VersionRef identifies the newest version of a plan and its content hash.
type VersionRef struct {
	Version int
	Hash    string
}

// Assignment says which plan a person follows from a day on.
type Assignment struct {
	ID      int64
	PlanKey string
	From    Date
}

// Round is one training session a person did (or started).
type Round struct {
	PlanVersionID int64
	Date          Date      // the person's local day
	StartedAt     time.Time // zero if unknown
	RecordedAt    time.Time
	Completed     bool
	SkippedSteps  []int // positions counted across all blocks from 0
}

// Stats summarises a person's completed rounds.
type Stats struct {
	Streak    int // days in a row with a completed round
	TotalDays int // days with at least one completed round
	DoneToday bool
}

// ComputeStats counts the days with a completed round. The streak runs back from
// today, or from yesterday if today has no round yet, so it isn't lost before breakfast.
func ComputeStats(completed []Date, today Date) Stats {
	days := make(map[Date]bool, len(completed))
	for _, d := range completed {
		days[d] = true
	}
	st := Stats{TotalDays: len(days), DoneToday: days[today]}
	d := today
	if !st.DoneToday {
		d = d.AddDays(-1)
	}
	for days[d] {
		st.Streak++
		d = d.AddDays(-1)
	}
	return st
}
