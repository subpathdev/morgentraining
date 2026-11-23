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

	gotKey   string
	gotDay   training.Date
	gotRound training.RoundInput
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
func server(t *testing.T, svc Trainer) *httptest.Server {
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
	if got.Plan.VersionId != 9 || got.Blocks[0].Steps[1].Kind != oapi.Rest || got.Blocks[0].Steps[1].Cue != "Beine ausschütteln" || got.Stats.Streak != 3 {
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

func TestDomainErrorsBecomeStatusCodes(t *testing.T) {
	round := `{"planVersionId":1,"date":"2026-11-22","completed":true}`
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

func TestNewNeedsATrainerAndFiles(t *testing.T) {
	if _, err := New(nil, Options{Static: page}); err == nil {
		t.Fatal("want an error without a Trainer")
	}
	if _, err := New(&fakeTrainer{}, Options{}); err == nil {
		t.Fatal("want an error without static files")
	}
}
