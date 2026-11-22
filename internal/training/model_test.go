package training

import (
	"testing"
	"time"
)

func day(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestPickFallsBackToGermanThenEnglishThenAny(t *testing.T) {
	cases := []struct {
		text Text
		lang string
		want string
	}{
		{Text{"de": "Atmen", "en": "Breathing"}, "en", "Breathing"},
		{Text{"de": "Atmen", "en": "Breathing"}, "pt", "Atmen"},
		{Text{"en": "Breathing", "pt": "Respirar"}, "fr", "Breathing"},
		{Text{"pt": "Respirar", "it": "Respirare"}, "fr", "Respirare"},
		{Text{"de": "", "en": "Breathing"}, "de", "Breathing"},
		{nil, "de", ""},
	}
	for _, c := range cases {
		if got := c.text.Pick(c.lang); got != c.want {
			t.Errorf("%v.Pick(%q) = %q, want %q", c.text, c.lang, got, c.want)
		}
	}
}

func TestComputeStats(t *testing.T) {
	days := func(ss ...string) []Date {
		out := make([]Date, len(ss))
		for i, s := range ss {
			out[i] = day(s)
		}
		return out
	}
	cases := []struct {
		name      string
		completed []Date
		today     string
		want      Stats
	}{
		{"nothing yet", nil, "2026-11-22", Stats{}},
		{"done today", days("2026-11-22"), "2026-11-22", Stats{Streak: 1, TotalDays: 1, DoneToday: true}},
		{"before breakfast keeps yesterday's streak", days("2026-11-20", "2026-11-21"), "2026-11-22", Stats{Streak: 2, TotalDays: 2}},
		{"a gap breaks the streak", days("2026-11-20", "2026-11-21"), "2026-11-23", Stats{Streak: 0, TotalDays: 2}},
		{"duplicates count once", days("2026-11-21", "2026-11-21", "2026-11-22"), "2026-11-22", Stats{Streak: 2, TotalDays: 2, DoneToday: true}},
		{"across a month end", days("2026-10-31", "2026-11-01"), "2026-11-01", Stats{Streak: 2, TotalDays: 2, DoneToday: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ComputeStats(c.completed, day(c.today)); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestHashCoversStepsButNotLibraryTexts(t *testing.T) {
	spec := PlanSpec{Key: "p", Names: Text{"de": "P"}, Blocks: []BlockSpec{{
		Names: Text{"de": "B"},
		Steps: []StepSpec{{Exercise: "squat", Seconds: 60}},
	}}}
	same := spec
	same.CreatedBy = "someone else" // not part of the content
	if spec.Hash() != same.Hash() {
		t.Fatal("hash must ignore who created the plan")
	}
	longer := PlanSpec{Key: "p", Names: spec.Names, Blocks: []BlockSpec{{
		Names: spec.Blocks[0].Names,
		Steps: []StepSpec{{Exercise: "squat", Seconds: 90}},
	}}}
	if spec.Hash() == longer.Hash() {
		t.Fatal("hash must change when a step changes")
	}
}

func TestLocalizeUsesOverridesAndVariants(t *testing.T) {
	pv := PlanVersion{
		ID: 7, PlanKey: "p", Version: 2, Names: Text{"de": "Plan", "en": "Plan EN"},
		Exercises: map[string]Exercise{
			"push-up": {Key: "push-up", Kind: KindExercise, Names: Text{"de": "Liegestütze", "en": "Push-ups"},
				Cues: Text{"en": "Core tight"}, Variants: map[string]Text{"knees": {"en": "on your knees"}}},
			"wall-sit": {Key: "wall-sit", Kind: KindExercise, Names: Text{"en": "Wall sit"}},
			"pause":    {Key: "pause", Kind: KindRest, Names: Text{"en": "Break"}},
		},
		Blocks: []BlockSpec{
			{Names: Text{"en": "Push"}, Steps: []StepSpec{{Exercise: "push-up", Seconds: 40, Variant: "knees"}}},
			{Names: Text{"en": "Wall"}, Steps: []StepSpec{
				{Exercise: "wall-sit", Seconds: 25, Names: Text{"en": "Wall sit 1 of 2"}, Cues: Text{"en": "Breathe"}},
				{Exercise: "pause", Seconds: 10},
			}},
		},
	}
	plan, blocks := pv.Localize("en")
	if plan != (SessionPlan{Key: "p", Name: "Plan EN", Version: 2, VersionID: 7, TotalSeconds: 75}) {
		t.Fatalf("plan: %+v", plan)
	}
	if got := blocks[0].Steps[0]; got != (Step{Kind: KindExercise, Name: "Push-ups (on your knees)", Cue: "Core tight", Seconds: 40}) {
		t.Fatalf("variant step: %+v", got)
	}
	if got := blocks[1].Steps[0]; got.Name != "Wall sit 1 of 2" || got.Cue != "Breathe" {
		t.Fatalf("override step: %+v", got)
	}
	if blocks[1].Seconds != 35 || blocks[1].Steps[1].Kind != KindRest {
		t.Fatalf("block 2: %+v", blocks[1])
	}
}

func TestDate(t *testing.T) {
	if _, err := ParseDate("22.11.2026"); err == nil {
		t.Fatal("want an error for a non-ISO date")
	}
	berlin, _ := time.LoadLocation("Europe/Berlin")
	// 23:30 UTC on the 21st is already the 22nd in Berlin.
	got := DateOf(time.Date(2026, 11, 21, 23, 30, 0, 0, time.UTC).In(berlin))
	if got != day("2026-11-22") || got.String() != "2026-11-22" {
		t.Fatalf("DateOf: %s", got)
	}
	if !day("2026-11-21").Before(got) || got.AddDays(-1) != day("2026-11-21") {
		t.Fatal("Before / AddDays")
	}
}
