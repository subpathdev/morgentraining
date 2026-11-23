package httpapi

import (
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
