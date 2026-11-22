package api

import (
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

	"github.com/subpathdev/morgentraining/internal/store"
	"github.com/subpathdev/morgentraining/web"
)

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
	h := (&Server{Store: st, Web: web.Files, Loc: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestRoundTrip(t *testing.T) {
	srv := server(t)

	var people []store.Person
	get(t, srv.URL+"/api/people", 200, &people)
	if len(people) != 1 || people[0].Key != "jan" {
		t.Fatalf("people: %+v", people)
	}

	var sess store.Session
	get(t, srv.URL+"/api/people/jan/today?date=2026-11-22", 200, &sess)
	if sess.Plan.Name != "5 Minuten morgens" || len(sess.Blocks) != 5 {
		t.Fatalf("session: %+v", sess.Plan)
	}

	body := `{"planVersionId":` + itoa(sess.Plan.VersionID) + `,"date":"2026-11-22","completed":true,"skippedSteps":[]}`
	resp, err := http.Post(srv.URL+"/api/people/jan/rounds", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var st store.Stats
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if resp.StatusCode != 200 || st.Streak != 1 || !st.DoneToday {
		t.Fatalf("round: %d %+v", resp.StatusCode, st)
	}
}

func TestErrors(t *testing.T) {
	srv := server(t)
	get(t, srv.URL+"/api/people/nobody/today", 404, nil)
	get(t, srv.URL+"/api/people/jan/today?date=yesterday", 400, nil)
	resp, _ := http.Post(srv.URL+"/api/people/jan/rounds", "application/json", strings.NewReader(`{"planVersionId":999,"date":"2026-11-22"}`))
	if resp.StatusCode != 400 {
		t.Fatalf("unknown version: want 400, got %d", resp.StatusCode)
	}
	resp, _ = http.Post(srv.URL+"/api/people/jan/rounds", "application/json", strings.NewReader(`{"evil":1}`))
	if resp.StatusCode != 400 {
		t.Fatalf("unknown field: want 400, got %d", resp.StatusCode)
	}
}

func TestPageIsServed(t *testing.T) {
	srv := server(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "sunbox") {
		t.Fatalf("page: %d", resp.StatusCode)
	}
	if strings.Contains(string(b), "googleapis") {
		t.Fatal("page must not load anything from outside the home network")
	}
}

func get(t *testing.T, url string, code int, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != code {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: want %d, got %d: %s", url, code, resp.StatusCode, b)
	}
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
