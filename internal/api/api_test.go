package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"

	"github.com/subpathdev/morgentraining/internal/store"
	"github.com/subpathdev/morgentraining/web"
)

// server starts the real handler, wrapped so that every API response is checked
// against the OpenAPI spec. A response that doesn't match the spec fails the test.
func server(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, f := range []string{"plan-morgen-5.yaml", "people.yaml"} {
		data, _ := os.ReadFile(filepath.Join("..", "..", "seed", f))
		if _, err := st.Import(context.Background(), data, "2026-11-01"); err != nil {
			t.Fatal(err)
		}
	}
	h, err := (&Server{Store: st, Web: web.Files, Loc: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(checkResponses(t, h))
	t.Cleanup(srv.Close)
	return srv
}

func checkResponses(t *testing.T, next http.Handler) http.Handler {
	spec, err := LoadSpec()
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

func TestRoundTrip(t *testing.T) {
	srv := server(t)

	var people []Person
	call(t, "GET", srv.URL+"/api/people", "", 200, &people)
	if len(people) != 1 || people[0].Key != "jan" {
		t.Fatalf("people: %+v", people)
	}

	var sess Session
	call(t, "GET", srv.URL+"/api/people/jan/today?date=2026-11-22", "", 200, &sess)
	if sess.Plan.Name != "5 Minuten morgens" || len(sess.Blocks) != 5 || sess.Blocks[3].Steps[1].Kind != Rest {
		t.Fatalf("session: %+v", sess.Plan)
	}

	var st Stats
	body := `{"planVersionId":` + itoa(sess.Plan.VersionId) + `,"date":"2026-11-22","startedAt":"2026-11-22T06:30:00.000Z","completed":true,"skippedSteps":[2]}`
	call(t, "POST", srv.URL+"/api/people/jan/rounds", body, 200, &st)
	if st.Streak != 1 || !st.DoneToday {
		t.Fatalf("round: %+v", st)
	}
	call(t, "GET", srv.URL+"/healthz", "", 200, nil)
}

func TestRequestsAreValidatedAgainstSpec(t *testing.T) {
	srv := server(t)
	cases := []struct {
		name, method, path, body string
		code                     int
	}{
		{"unknown person", "GET", "/api/people/nobody/today", "", 404},
		{"bad date", "GET", "/api/people/jan/today?date=yesterday", "", 400},
		{"bad person key", "GET", "/api/people/Not%20Valid/today", "", 400},
		{"unknown plan version", "POST", "/api/people/jan/rounds", `{"planVersionId":999,"date":"2026-11-22","completed":true}`, 400},
		{"unknown field", "POST", "/api/people/jan/rounds", `{"planVersionId":1,"date":"2026-11-22","completed":true,"evil":1}`, 400},
		{"missing completed", "POST", "/api/people/jan/rounds", `{"planVersionId":1,"date":"2026-11-22"}`, 400},
		{"negative skipped step", "POST", "/api/people/jan/rounds", `{"planVersionId":1,"date":"2026-11-22","completed":true,"skippedSteps":[-1]}`, 400},
		{"not JSON", "POST", "/api/people/jan/rounds", `hello`, 400},
		{"unknown path", "GET", "/api/nothing", "", 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var e Error
			call(t, c.method, srv.URL+c.path, c.body, c.code, &e)
			if e.Error == "" {
				t.Fatal("want a JSON error message")
			}
		})
	}
}

func TestSpecAndPageAreServed(t *testing.T) {
	srv := server(t)
	for path, want := range map[string]string{"/openapi.yaml": "openapi: 3.0.3", "/": "sunbox"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(b), want) {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if strings.Contains(string(b), "googleapis") {
			t.Fatalf("%s must not load anything from outside the home network", path)
		}
	}
}

func call(t *testing.T, method, url, body string, code int, v any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != code {
		t.Fatalf("%s %s: want %d, got %d: %s", method, url, code, resp.StatusCode, b)
	}
	if v != nil {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("decode %s: %v", b, err)
		}
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
