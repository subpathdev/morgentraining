package httpapi

import (
	"maps"

	"github.com/subpathdev/morgentraining/internal/httpapi/oapi"
	"github.com/subpathdev/morgentraining/internal/training"
)

// Mapping between the training domain and the wire format in the spec.
// Domain types never leave this package as JSON.

func toPerson(p training.Person) oapi.Person {
	return oapi.Person{Key: p.Key, Name: p.Name, Language: p.Language}
}

func toStats(st training.Stats) oapi.Stats {
	return oapi.Stats{Streak: st.Streak, TotalDays: st.TotalDays, DoneToday: st.DoneToday}
}

func toSession(s training.Session) oapi.Session {
	out := oapi.Session{
		Person: toPerson(s.Person),
		Plan: oapi.SessionPlan{
			Key:          s.Plan.Key,
			Name:         s.Plan.Name,
			Version:      s.Plan.Version,
			VersionId:    s.Plan.VersionID,
			TotalSeconds: s.Plan.TotalSeconds,
		},
		Stats:  toStats(s.Stats),
		Blocks: make([]oapi.Block, len(s.Blocks)),
	}
	for i, b := range s.Blocks {
		steps := make([]oapi.Step, len(b.Steps))
		for j, st := range b.Steps {
			steps[j] = oapi.Step{Kind: oapi.StepKind(st.Kind), Name: st.Name, Cue: st.Cue, Seconds: st.Seconds}
		}
		out.Blocks[i] = oapi.Block{Name: b.Name, Summary: b.Summary, Seconds: b.Seconds, Steps: steps}
	}
	return out
}

func fromPersonInput(key string, in oapi.PersonInput) training.PersonImport {
	return training.PersonImport{
		Person: training.Person{Key: key, Name: in.Name, Language: in.Language},
		Plan:   in.Plan,
	}
}

func fromRoundInput(in oapi.RoundInput) training.RoundInput {
	out := training.RoundInput{
		PlanVersionID: in.PlanVersionId,
		Date:          training.DateOf(in.Date.Time),
		Completed:     in.Completed,
	}
	if in.StartedAt != nil {
		out.StartedAt = *in.StartedAt
	}
	if in.SkippedSteps != nil {
		out.SkippedSteps = *in.SkippedSteps
	}
	return out
}

func toPlanSummary(pv training.PlanVersion) oapi.PlanSummary {
	return oapi.PlanSummary{Key: pv.PlanKey, Names: toText(pv.Names), Version: pv.Version, TotalSeconds: pv.TotalSeconds()}
}

// toPlanDetail returns the version in the form it is edited in: the PUT body,
// with the exercises it uses in the order the steps first use them.
func toPlanDetail(pv training.PlanVersion) oapi.PlanDetail {
	plan := oapi.PlanInput{Names: toText(pv.Names), Blocks: make([]oapi.BlockInput, len(pv.Blocks))}
	var exercises []oapi.ExerciseInput
	seen := map[string]bool{}
	for i, b := range pv.Blocks {
		block := oapi.BlockInput{Names: toText(b.Names), Summary: optText(b.Summaries), Steps: make([]oapi.StepInput, len(b.Steps))}
		for j, s := range b.Steps {
			step := oapi.StepInput{Exercise: s.Exercise, Seconds: s.Seconds, Names: optText(s.Names), Cues: optText(s.Cues)}
			if s.Variant != "" {
				step.Variant = &s.Variant
			}
			block.Steps[j] = step
			if ex, ok := pv.Exercises[s.Exercise]; ok && !seen[s.Exercise] {
				seen[s.Exercise] = true
				exercises = append(exercises, toExerciseInput(ex))
			}
		}
		plan.Blocks[i] = block
	}
	if len(exercises) > 0 {
		plan.Exercises = &exercises
	}
	return oapi.PlanDetail{Key: pv.PlanKey, Version: pv.Version, TotalSeconds: pv.TotalSeconds(), Plan: plan}
}

func toExerciseInput(e training.Exercise) oapi.ExerciseInput {
	kind := oapi.ExerciseInputKind(e.Kind)
	out := oapi.ExerciseInput{Key: e.Key, Kind: &kind, Names: toText(e.Names), Cues: optText(e.Cues)}
	if len(e.Variants) > 0 {
		variants := make(map[string]oapi.Text, len(e.Variants))
		for k, v := range e.Variants {
			variants[k] = toText(v)
		}
		out.Variants = &variants
	}
	return out
}

func fromPlanInput(key string, in oapi.PlanInput) training.PlanImport {
	imp := training.PlanImport{Plan: training.PlanSpec{Key: key, Names: fromText(in.Names), Blocks: make([]training.BlockSpec, len(in.Blocks))}}
	if in.CreatedBy != nil {
		imp.Plan.CreatedBy = *in.CreatedBy
	}
	if in.Exercises != nil {
		for _, e := range *in.Exercises {
			ex := training.Exercise{Key: e.Key, Kind: training.KindExercise, Names: fromText(e.Names), Cues: fromOptText(e.Cues)}
			if e.Kind != nil {
				ex.Kind = training.StepKind(*e.Kind)
			}
			if e.Variants != nil {
				ex.Variants = make(map[string]training.Text, len(*e.Variants))
				for k, v := range *e.Variants {
					ex.Variants[k] = fromText(v)
				}
			}
			imp.Exercises = append(imp.Exercises, ex)
		}
	}
	for i, b := range in.Blocks {
		block := training.BlockSpec{Names: fromText(b.Names), Summaries: fromOptText(b.Summary), Steps: make([]training.StepSpec, len(b.Steps))}
		for j, s := range b.Steps {
			step := training.StepSpec{Exercise: s.Exercise, Seconds: s.Seconds, Names: fromOptText(s.Names), Cues: fromOptText(s.Cues)}
			if s.Variant != nil {
				step.Variant = *s.Variant
			}
			block.Steps[j] = step
		}
		imp.Plan.Blocks[i] = block
	}
	return imp
}

func toText(t training.Text) oapi.Text {
	if t == nil {
		return oapi.Text{}
	}
	return oapi.Text(maps.Clone(t))
}

// optText leaves out an empty text, so a client sees only what the author wrote.
func optText(t training.Text) *oapi.Text {
	if len(t) == 0 {
		return nil
	}
	out := toText(t)
	return &out
}

func fromText(t oapi.Text) training.Text { return training.Text(maps.Clone(t)) }

func fromOptText(t *oapi.Text) training.Text {
	if t == nil {
		return nil
	}
	return fromText(*t)
}
