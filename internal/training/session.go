package training

// Session is everything the page needs for one person's round, in their language.
type Session struct {
	Person Person
	Plan   SessionPlan
	Blocks []Block
	Stats  Stats
}

type SessionPlan struct {
	Key          string
	Name         string
	Version      int
	VersionID    int64
	TotalSeconds int
}

type Block struct {
	Name    string
	Summary string
	Seconds int
	Steps   []Step
}

type Step struct {
	Kind    StepKind
	Name    string
	Cue     string
	Seconds int
}

// Localize resolves the version's texts for lang. A step's own name and cue
// win over the exercise's; a variant is added to the exercise name in brackets.
func (pv PlanVersion) Localize(lang string) (SessionPlan, []Block) {
	plan := SessionPlan{Key: pv.PlanKey, Name: pv.Names.Pick(lang), Version: pv.Version, VersionID: pv.ID}
	blocks := make([]Block, len(pv.Blocks))
	for i, b := range pv.Blocks {
		out := Block{Name: b.Names.Pick(lang), Summary: b.Summaries.Pick(lang), Steps: make([]Step, len(b.Steps))}
		for j, s := range b.Steps {
			ex := pv.Exercises[s.Exercise]
			name := s.Names.Pick(lang)
			if name == "" {
				name = ex.Names.Pick(lang)
				if v := ex.Variants[s.Variant].Pick(lang); s.Variant != "" && v != "" {
					name += " (" + v + ")"
				}
			}
			cue := s.Cues.Pick(lang)
			if cue == "" {
				cue = ex.Cues.Pick(lang)
			}
			out.Steps[j] = Step{Kind: ex.Kind, Name: name, Cue: cue, Seconds: s.Seconds}
			out.Seconds += s.Seconds
		}
		blocks[i] = out
		plan.TotalSeconds += out.Seconds
	}
	return plan, blocks
}
