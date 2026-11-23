package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEndToEnd wires the real pieces together: embedded seed, SQLite, service,
// HTTP handler and page. The details are tested in each package.
func TestEndToEnd(t *testing.T) {
	ctx := context.Background()
	cfg := config{
		DBPath:   filepath.Join(t.TempDir(), "e2e.db"),
		TimeZone: "UTC",
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		Now:      func() time.Time { return time.Date(2026, 11, 22, 6, 0, 0, 0, time.UTC) },
	}
	a, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.handler(ctx); err != nil { // first start seeds
		t.Fatal(err)
	}
	h, err := a.handler(ctx) // second start: seeding is a no-op
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	var sess struct {
		Plan struct {
			VersionID    int64 `json:"versionId"`
			TotalSeconds int   `json:"totalSeconds"`
		} `json:"plan"`
	}
	get(t, srv.URL+"/api/people/jan/today", &sess)
	if sess.Plan.TotalSeconds != 300 {
		t.Fatalf("seeded plan: %+v", sess.Plan)
	}

	body := fmt.Sprintf(`{"planVersionId":%d,"date":"2026-11-22","completed":true}`, sess.Plan.VersionID)
	resp, err := http.Post(srv.URL+"/api/people/jan/rounds", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Streak    int  `json:"streak"`
		DoneToday bool `json:"doneToday"`
	}
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if resp.StatusCode != 200 || st.Streak != 1 || !st.DoneToday {
		t.Fatalf("round: %d %+v", resp.StatusCode, st)
	}

	// A person added over the API gets the seeded plan in their language.
	req, _ := http.NewRequest("PUT", srv.URL+"/api/people/mariana", strings.NewReader(`{"name":"Mariana","language":"en","plan":"morgen-5"}`))
	req.Header.Set("Content-Type", "application/json")
	put, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	put.Body.Close()
	if put.StatusCode != 201 {
		t.Fatalf("add person: %d", put.StatusCode)
	}
	var m struct {
		Blocks []struct {
			Name string `json:"name"`
		} `json:"blocks"`
	}
	get(t, srv.URL+"/api/people/mariana/today", &m)
	if len(m.Blocks) != 5 || m.Blocks[0].Name != "Warm-up" {
		t.Fatalf("new person's plan: %+v", m)
	}

	page, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if !bytes.Contains(b, []byte("sunbox")) || bytes.Contains(b, []byte("googleapis")) {
		t.Fatal("the page must be served and load nothing from outside the home network")
	}
}

func get(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestImportAndBackupCommands(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "cli.db")
	plan := filepath.Join("..", "..", "seed", "plan-morgen-5.yaml")
	var out, errOut bytes.Buffer
	ctx := context.Background()

	if err := run(ctx, []string{"import", "-db", db, plan}, &out, &errOut); err != nil {
		t.Fatal(err, errOut.String())
	}
	if err := run(ctx, []string{"import", "-db", db, plan}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "saved as version 1") || !strings.Contains(got, "unchanged (version 1)") {
		t.Fatalf("import output: %s", got)
	}
	target := filepath.Join(dir, "copy.db")
	if err := run(ctx, []string{"backup", "-db", db, target}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"nonsense"}, &out, &errOut); err != errUsage {
		t.Fatalf("unknown command: %v", err)
	}
}
