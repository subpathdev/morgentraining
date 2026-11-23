package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"

	"github.com/subpathdev/morgentraining/internal/httpapi/oapi"
	"github.com/subpathdev/morgentraining/internal/training"
)

// fakeTrainer returns canned answers and records what the handlers passed in.
type fakeTrainer struct {
	today   training.Date
	people  []training.Person
	session training.Session
	stats   training.Stats
	err     error

	created bool
	plans   []training.PlanVersion
	saved   training.PlanResult
	gotPlan training.PlanImport

	gotKey    string
	gotDay    training.Date
	gotRound  training.RoundInput
	gotPerson training.PersonImport
}

func (f *fakeTrainer) Today() training.Date { return f.today }

func (f *fakeTrainer) People(context.Context) ([]training.Person, error) { return f.people, f.err }

func (f *fakeTrainer) Session(_ context.Context, key string, day training.Date) (training.Session, error) {
	f.gotKey, f.gotDay = key, day
	return f.session, f.err
}

func (f *fakeTrainer) RecordRound(_ context.Context, key string, in training.RoundInput) (training.Stats, error) {
	f.gotKey, f.gotRound = key, in
	return f.stats, f.err
}

func (f *fakeTrainer) SavePerson(_ context.Context, p training.PersonImport) (bool, error) {
	f.gotKey, f.gotPerson = p.Person.Key, p
	return f.created, f.err
}

func (f *fakeTrainer) Plans(context.Context) ([]training.PlanVersion, error) { return f.plans, f.err }

func (f *fakeTrainer) Plan(_ context.Context, key string) (training.PlanVersion, error) {
	f.gotKey = key
	if f.err != nil {
		return training.PlanVersion{}, f.err
	}
	return f.plans[0], nil
}

func (f *fakeTrainer) ImportPlan(_ context.Context, imp training.PlanImport) (training.PlanResult, error) {
	f.gotKey, f.gotPlan = imp.Plan.Key, imp
	return f.saved, f.err
}

// samplePlan has a block with an exercise, an in-block rest, a step with its own name, and a variant.
func samplePlan() training.PlanVersion {
	return training.PlanVersion{
		ID: 9, PlanKey: "morgen-5", Version: 2, Names: training.Text{"de": "5 Minuten morgens"},
		Blocks: []training.BlockSpec{
			{Names: training.Text{"de": "Wandsitzen"}, Summaries: training.Text{"de": "2 × 25 s"}, Steps: []training.StepSpec{
				{Exercise: "wall-sit", Seconds: 25, Names: training.Text{"de": "Wandsitzen 1 von 2"}},
				{Exercise: "short-break", Seconds: 10},
				{Exercise: "wall-sit", Seconds: 25},
			}},
			{Names: training.Text{"de": "Liegestütze"}, Steps: []training.StepSpec{{Exercise: "push-up", Seconds: 60, Variant: "knees"}}},
		},
		Exercises: map[string]training.Exercise{
			"wall-sit":    {Key: "wall-sit", Kind: training.KindExercise, Names: training.Text{"de": "Wandsitzen"}, Cues: training.Text{"de": "Rücken an die Wand"}},
			"short-break": {Key: "short-break", Kind: training.KindRest, Names: training.Text{"de": "Kurze Pause"}},
			"push-up":     {Key: "push-up", Kind: training.KindExercise, Names: training.Text{"de": "Liegestütze"}, Variants: map[string]training.Text{"knees": {"de": "auf den Knien"}}},
		},
	}
}

var page = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=sunbox></div>")}}

func sampleSession() training.Session {
	return training.Session{
		Person: training.Person{Key: "jan", Name: "Jan", Language: "de"},
		Plan:   training.SessionPlan{Key: "morgen-5", Name: "5 Minuten morgens", Version: 2, VersionID: 9, TotalSeconds: 35},
		Blocks: []training.Block{{Name: "Wandsitzen", Seconds: 35, Steps: []training.Step{
			{Kind: training.KindExercise, Name: "Wandsitzen 1 von 2", Seconds: 25},
			{Kind: training.KindRest, Name: "Kurze Pause", Cue: "Beine ausschütteln", Seconds: 10},
		}}},
		Stats: training.Stats{Streak: 3, TotalDays: 10, DoneToday: true},
	}
}

// server starts the handler, wrapped so that every API response is checked
// against the OpenAPI spec. A response that doesn't match the spec fails the test.
func server(t *testing.T, svc Domain) *httptest.Server {
	t.Helper()
	h, err := New(svc, Options{Static: page})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(checkResponses(t, h))
	t.Cleanup(srv.Close)
	return srv
}

func checkResponses(t *testing.T, next http.Handler) http.Handler {
	spec, err := loadSpec()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)
		if route, params, err := router.FindRoute(r); err == nil {
			in := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route},
				Status:                 rec.Code,
				Header:                 rec.Header(),
				Options:                &openapi3filter.Options{IncludeResponseStatus: true},
			}
			in.SetBodyBytes(rec.Body.Bytes())
			if err := openapi3filter.ValidateResponse(r.Context(), in); err != nil {
				t.Errorf("%s %s: response %d does not match the spec: %v\nbody: %s", r.Method, r.URL, rec.Code, err, rec.Body.String())
			}
		}
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
	})
}

func call(t *testing.T, method, url, body string, wantCode int, v any) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != wantCode {
		t.Fatalf("%s %s: want %d, got %d: %s", method, url, wantCode, resp.StatusCode, b)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("decode %s: %v", b, err)
		}
	}
}

func TestListPeople(t *testing.T) {
	fake := &fakeTrainer{people: []training.Person{{Key: "jan", Name: "Jan", Language: "de"}, {Key: "mariana", Name: "Mariana", Language: "pt"}}}
	var got []oapi.Person
	call(t, "GET", server(t, fake).URL+"/api/people", "", 200, &got)
	if len(got) != 2 || got[1] != (oapi.Person{Key: "mariana", Name: "Mariana", Language: "pt"}) {
		t.Fatalf("people: %+v", got)
	}
}

func TestTodayMapsTheSession(t *testing.T) {
	fake := &fakeTrainer{today: training.NewDate(2026, 11, 22), session: sampleSession()}
	srv := server(t, fake)

	var got oapi.Session
	call(t, "GET", srv.URL+"/api/people/jan/today", "", 200, &got)
	if fake.gotKey != "jan" || fake.gotDay != fake.today {
		t.Fatalf("without a date the service's today must be used, got %s %s", fake.gotKey, fake.gotDay)
	}
	if got.Plan.VersionId != 9 || got.Blocks[0].Steps[1].Kind != oapi.StepKindRest || got.Blocks[0].Steps[1].Cue != "Beine ausschütteln" || got.Stats.Streak != 3 {
		t.Fatalf("session: %+v", got)
	}

	call(t, "GET", srv.URL+"/api/people/jan/today?date=2026-01-31", "", 200, &got)
	if fake.gotDay != training.NewDate(2026, 1, 31) {
		t.Fatalf("explicit date: %s", fake.gotDay)
	}
}

func TestAddRoundMapsTheInput(t *testing.T) {
	fake := &fakeTrainer{stats: training.Stats{Streak: 1, TotalDays: 1, DoneToday: true}}
	body := `{"planVersionId":9,"date":"2026-11-22","startedAt":"2026-11-22T06:30:00Z","completed":true,"skippedSteps":[2]}`
	var got oapi.Stats
	call(t, "POST", server(t, fake).URL+"/api/people/jan/rounds", body, 200, &got)
	want := training.RoundInput{
		PlanVersionID: 9,
		Date:          training.NewDate(2026, 11, 22),
		StartedAt:     time.Date(2026, 11, 22, 6, 30, 0, 0, time.UTC),
		Completed:     true,
		SkippedSteps:  []int{2},
	}
	r := fake.gotRound
	if fake.gotKey != "jan" || r.PlanVersionID != want.PlanVersionID || r.Date != want.Date || !r.StartedAt.Equal(want.StartedAt) ||
		!r.Completed || len(r.SkippedSteps) != 1 || r.SkippedSteps[0] != 2 {
		t.Fatalf("round: %+v", r)
	}
	if got != (oapi.Stats{Streak: 1, TotalDays: 1, DoneToday: true}) {
		t.Fatalf("stats: %+v", got)
	}
}

func TestPutPersonMapsTheInput(t *testing.T) {
	for _, c := range []struct {
		created bool
		code    int
	}{{true, 201}, {false, 200}} {
		fake := &fakeTrainer{created: c.created}
		var got oapi.Person
		call(t, "PUT", server(t, fake).URL+"/api/people/mariana", `{"name":"Mariana","language":"en","plan":"morgen-5"}`, c.code, &got)
		want := training.PersonImport{Person: training.Person{Key: "mariana", Name: "Mariana", Language: "en"}, Plan: "morgen-5"}
		if fake.gotPerson != want {
			t.Fatalf("person passed to the service: %+v", fake.gotPerson)
		}
		if got != (oapi.Person{Key: "mariana", Name: "Mariana", Language: "en"}) {
			t.Fatalf("response: %+v", got)
		}
	}
}

func TestListPlans(t *testing.T) {
	fake := &fakeTrainer{plans: []training.PlanVersion{samplePlan()}}
	var got []oapi.PlanSummary
	call(t, "GET", server(t, fake).URL+"/api/plans", "", 200, &got)
	if len(got) != 1 || got[0].Key != "morgen-5" || got[0].Version != 2 || got[0].TotalSeconds != 120 || got[0].Names["de"] != "5 Minuten morgens" {
		t.Fatalf("plans: %+v", got)
	}
}

func TestGetPlanReturnsTheEditableForm(t *testing.T) {
	fake := &fakeTrainer{plans: []training.PlanVersion{samplePlan()}}
	var got oapi.PlanDetail
	call(t, "GET", server(t, fake).URL+"/api/plans/morgen-5", "", 200, &got)
	if fake.gotKey != "morgen-5" || got.Version != 2 || got.TotalSeconds != 120 {
		t.Fatalf("detail: key %s %+v", fake.gotKey, got)
	}
	ex := *got.Plan.Exercises
	if len(ex) != 3 || ex[0].Key != "wall-sit" || ex[1].Key != "short-break" || ex[2].Key != "push-up" {
		t.Fatalf("exercises must be listed once, in order of first use: %+v", ex)
	}
	if *ex[1].Kind != oapi.ExerciseInputKindRest || (*ex[2].Variants)["knees"]["de"] != "auf den Knien" || ex[1].Cues != nil {
		t.Fatalf("exercise details: %+v", ex)
	}
	b := got.Plan.Blocks
	if (*b[0].Summary)["de"] != "2 × 25 s" || b[1].Summary != nil {
		t.Fatalf("summaries: only what the author wrote, got %+v", b)
	}
	s := b[0].Steps
	if (*s[0].Names)["de"] != "Wandsitzen 1 von 2" || s[1].Names != nil || s[1].Variant != nil || *b[1].Steps[0].Variant != "knees" {
		t.Fatalf("steps: only what the author wrote, got %+v", s)
	}
}

func TestPutPlanMapsTheInput(t *testing.T) {
	body := `{"names":{"de":"Kurz"},"createdBy":"jan",
		"exercises":[{"key":"plank","names":{"de":"Unterarmstütz"}},{"key":"pause","kind":"rest","names":{"de":"Pause"},"variants":{"long":{"de":"lang"}}}],
		"blocks":[{"names":{"de":"Halten"},"steps":[{"exercise":"plank","seconds":30,"cues":{"de":"Atmen"}},{"exercise":"pause","seconds":10,"variant":"long"}]}]}`
	for _, c := range []struct {
		saved training.PlanResult
		code  int
	}{
		{training.PlanResult{PlanKey: "kurz", Version: 1, Created: true}, 201},
		{training.PlanResult{PlanKey: "kurz", Version: 3, Created: true}, 200},
		{training.PlanResult{PlanKey: "kurz", Version: 3, Created: false}, 200},
	} {
		fake := &fakeTrainer{saved: c.saved}
		var got oapi.PlanSaved
		call(t, "PUT", server(t, fake).URL+"/api/plans/kurz", body, c.code, &got)
		if got != (oapi.PlanSaved{Key: "kurz", Version: c.saved.Version, Changed: c.saved.Created}) {
			t.Fatalf("response: %+v", got)
		}
		p := fake.gotPlan
		if p.Plan.Key != "kurz" || p.Plan.CreatedBy != "jan" || p.Plan.Names["de"] != "Kurz" || len(p.Exercises) != 2 {
			t.Fatalf("plan passed to the service: %+v", p)
		}
		if p.Exercises[0].Kind != training.KindExercise || p.Exercises[1].Kind != training.KindRest || p.Exercises[1].Variants["long"]["de"] != "lang" {
			t.Fatalf("exercises (kind defaults to exercise): %+v", p.Exercises)
		}
		st := p.Plan.Blocks[0].Steps
		if st[0].Cues["de"] != "Atmen" || st[0].Names != nil || st[1].Variant != "long" || st[1].Seconds != 10 {
			t.Fatalf("steps: %+v", st)
		}
	}
}

func TestDomainErrorsBecomeStatusCodes(t *testing.T) {
	round := `{"planVersionId":1,"date":"2026-11-22","completed":true}`
	person := `{"name":"X","language":"de","plan":"nope"}`
	plan := `{"names":{"de":"X"},"blocks":[{"names":{"de":"B"},"steps":[{"exercise":"nope","seconds":10}]}]}`
	cases := []struct {
		name         string
		err          error
		method, path string
		body         string
		code         int
		message      string
	}{
		{"unknown person", fmt.Errorf("person x: %w", training.ErrNotFound), "GET", "/api/people/x/today", "", 404, "person x: not found"},
		{"no plan", training.ErrNotFound, "POST", "/api/people/jan/rounds", round, 404, "not found"},
		{"invalid round", &training.ValidationError{Problems: []string{"unknown plan version 1"}}, "POST", "/api/people/jan/rounds", round, 400, "unknown plan version 1"},
		{"storage failure is hidden", errors.New("disk on fire"), "GET", "/api/people/jan/today", "", 500, "internal error"},
		{"storage failure on list", errors.New("disk on fire"), "GET", "/api/people", "", 500, "internal error"},
		{"unknown plan for person", &training.ValidationError{Problems: []string{`person x: unknown plan "nope"`}}, "PUT", "/api/people/x", person, 400, `unknown plan "nope"`},
		{"storage failure on save", errors.New("disk on fire"), "PUT", "/api/people/x", person, 500, "internal error"},
		{"unknown plan", fmt.Errorf("plan x: %w", training.ErrNotFound), "GET", "/api/plans/x", "", 404, "plan x: not found"},
		{"storage failure on plan", errors.New("disk on fire"), "GET", "/api/plans/x", "", 500, "internal error"},
		{"storage failure on plans", errors.New("disk on fire"), "GET", "/api/plans", "", 500, "internal error"},
		{"invalid plan", &training.ValidationError{Problems: []string{`block 1 step 1: unknown exercise "nope"`, "plan needs a name"}}, "PUT", "/api/plans/x", plan, 400, `unknown exercise "nope"; plan needs a name`},
		{"storage failure on plan save", errors.New("disk on fire"), "PUT", "/api/plans/x", plan, 500, "internal error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e oapi.Error
			call(t, c.method, server(t, &fakeTrainer{err: c.err}).URL+c.path, c.body, c.code, &e)
			if !strings.Contains(e.Error, c.message) {
				t.Fatalf("want message %q, got %q", c.message, e.Error)
			}
		})
	}
}

func TestRequestsAreValidatedAgainstSpec(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		code                     int
	}{
		{"bad date", "GET", "/api/people/jan/today?date=yesterday", "", 400},
		{"bad person key", "GET", "/api/people/Not%20Valid/today", "", 400},
		{"unknown field", "POST", "/api/people/jan/rounds", `{"planVersionId":1,"date":"2026-11-22","completed":true,"evil":1}`, 400},
		{"missing completed", "POST", "/api/people/jan/rounds", `{"planVersionId":1,"date":"2026-11-22"}`, 400},
		{"negative skipped step", "POST", "/api/people/jan/rounds", `{"planVersionId":1,"date":"2026-11-22","completed":true,"skippedSteps":[-1]}`, 400},
		{"not JSON", "POST", "/api/people/jan/rounds", `hello`, 400},
		{"too large", "POST", "/api/people/jan/rounds", `{"planVersionId":1,"date":"2026-11-22","completed":true,"skippedSteps":[` + strings.Repeat("1,", maxBody) + `1]}`, 400},
		{"unknown path", "GET", "/api/nothing", "", 404},
		{"person without plan", "PUT", "/api/people/mariana", `{"name":"Mariana","language":"en"}`, 400},
		{"person with empty name", "PUT", "/api/people/mariana", `{"name":"","language":"en","plan":"morgen-5"}`, 400},
		{"person with bad language", "PUT", "/api/people/mariana", `{"name":"Mariana","language":"English","plan":"morgen-5"}`, 400},
		{"person with unknown field", "PUT", "/api/people/mariana", `{"name":"Mariana","language":"en","plan":"morgen-5","admin":true}`, 400},
		{"person with bad key", "PUT", "/api/people/Mariana", `{"name":"Mariana","language":"en","plan":"morgen-5"}`, 400},
		{"plan without blocks", "PUT", "/api/plans/x", `{"names":{"de":"X"},"blocks":[]}`, 400},
		{"plan step of 0 seconds", "PUT", "/api/plans/x", `{"names":{"de":"X"},"blocks":[{"names":{"de":"B"},"steps":[{"exercise":"a","seconds":0}]}]}`, 400},
		{"plan with unknown field", "PUT", "/api/plans/x", `{"names":{"de":"X"},"blocks":[{"names":{"de":"B"},"steps":[{"exercise":"a","seconds":5}]}],"plan":"x"}`, 400},
		{"plan with bad exercise kind", "PUT", "/api/plans/x", `{"names":{"de":"X"},"exercises":[{"key":"a","kind":"nap","names":{"de":"A"}}],"blocks":[{"names":{"de":"B"},"steps":[{"exercise":"a","seconds":5}]}]}`, 400},
		{"plan with bad key", "PUT", "/api/plans/Morgen", `{"names":{"de":"X"},"blocks":[{"names":{"de":"B"},"steps":[{"exercise":"a","seconds":5}]}]}`, 400},
		{"text that isn't a string", "PUT", "/api/plans/x", `{"names":{"de":1},"blocks":[{"names":{"de":"B"},"steps":[{"exercise":"a","seconds":5}]}]}`, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeTrainer{}
			var e oapi.Error
			call(t, c.method, server(t, fake).URL+c.path, c.body, c.code, &e)
			if e.Error == "" {
				t.Fatal("want a JSON error message")
			}
			if fake.gotKey != "" {
				t.Fatal("an invalid request must not reach the service")
			}
		})
	}
}

func TestPageSpecAndHealthAreServed(t *testing.T) {
	srv := server(t, &fakeTrainer{})
	for path, want := range map[string]string{"/openapi.yaml": "openapi: 3.0.3", "/": "sunbox", "/healthz": "ok"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), want) {
			t.Fatalf("%s: %d %q", path, resp.StatusCode, b)
		}
	}
}

func TestNewNeedsADomainAndFiles(t *testing.T) {
	if _, err := New(nil, Options{Static: page}); err == nil {
		t.Fatal("want an error without a Domain")
	}
	if _, err := New(&fakeTrainer{}, Options{}); err == nil {
		t.Fatal("want an error without static files")
	}
}
