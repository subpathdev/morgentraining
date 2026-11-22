// Package store keeps people, versioned workout plans and finished rounds in SQLite.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaV1 string

const DateLayout = "2006-01-02"

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database file and applies the schema.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite has a single writer anyway, and this avoids "database is locked".
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v >= 1 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaV1); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := tx.Exec("PRAGMA user_version = 1"); err != nil {
		return err
	}
	return tx.Commit()
}

// Backup writes a consistent copy of the database to path (must not exist yet).
func (s *Store) Backup(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

// Empty reports whether no plan has been imported yet.
func (s *Store) Empty(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM plan").Scan(&n)
	return n == 0, err
}

// ImportResult describes what an import changed.
type ImportResult struct {
	Kind    string `json:"kind"`              // plan | people
	Plan    string `json:"plan,omitempty"`    // plan key
	Version int    `json:"version,omitempty"` // plan version now current
	Created bool   `json:"created"`           // false if the plan content was unchanged
	People  int    `json:"people,omitempty"`  // people added or updated
}

// Import reads a plan or people YAML file. today is the date new assignments start.
func (s *Store) Import(ctx context.Context, data []byte, today string) (ImportResult, error) {
	kind, err := fileKind(data)
	if err != nil {
		return ImportResult{}, err
	}
	if kind == "plan" {
		var p PlanFile
		if err := yaml.Unmarshal(data, &p); err != nil {
			return ImportResult{}, fmt.Errorf("parse plan: %w", err)
		}
		return s.ImportPlan(ctx, p)
	}
	var pf PeopleFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return ImportResult{}, fmt.Errorf("parse people: %w", err)
	}
	return s.ImportPeople(ctx, pf, today)
}

func toJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, _ := json.Marshal(v)
	if string(b) == "null" {
		return "{}"
	}
	return string(b)
}

// ImportPlan upserts the plan's exercises and saves a new plan version if the content changed.
func (s *Store) ImportPlan(ctx context.Context, p PlanFile) (ImportResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer tx.Rollback()

	known := func(key string) bool {
		var id int64
		return tx.QueryRowContext(ctx, "SELECT id FROM exercise WHERE key = ?", key).Scan(&id) == nil
	}
	if err := p.validate(known); err != nil {
		return ImportResult{}, err
	}

	for _, e := range p.Exercises {
		kind := e.Kind
		if kind == "" {
			kind = "exercise"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO exercise (key, kind, names, cues, variants) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET kind = excluded.kind, names = excluded.names, cues = excluded.cues, variants = excluded.variants`,
			e.Key, kind, toJSON(e.Names), toJSON(e.Cues), toJSON(e.Variants))
		if err != nil {
			return ImportResult{}, fmt.Errorf("save exercise %s: %w", e.Key, err)
		}
	}

	// The hash covers what a round depends on: names, blocks and steps (not the exercise library texts).
	sum := sha256.Sum256([]byte(toJSON(struct {
		Names  Text
		Blocks []BlockDef
	}{p.Names, p.Blocks})))
	hash := hex.EncodeToString(sum[:])

	var planID int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM plan WHERE key = ?", p.Plan).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := tx.ExecContext(ctx, "INSERT INTO plan (key, created_by) VALUES (?, ?)", p.Plan, p.CreatedBy)
		if err != nil {
			return ImportResult{}, err
		}
		planID, _ = res.LastInsertId()
	} else if err != nil {
		return ImportResult{}, err
	}

	var lastVersion int
	var lastHash string
	err = tx.QueryRowContext(ctx, "SELECT version, hash FROM plan_version WHERE plan_id = ? ORDER BY version DESC LIMIT 1", planID).Scan(&lastVersion, &lastHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ImportResult{}, err
	}
	if lastHash == hash {
		// Exercise texts may still have changed, so commit those.
		return ImportResult{Kind: "plan", Plan: p.Plan, Version: lastVersion, Created: false}, tx.Commit()
	}

	version := lastVersion + 1
	res, err := tx.ExecContext(ctx, "INSERT INTO plan_version (plan_id, version, names, created_at, hash) VALUES (?, ?, ?, ?, ?)",
		planID, version, toJSON(p.Names), time.Now().UTC().Format(time.RFC3339), hash)
	if err != nil {
		return ImportResult{}, err
	}
	pvID, _ := res.LastInsertId()
	for bi, b := range p.Blocks {
		res, err := tx.ExecContext(ctx, "INSERT INTO block (plan_version_id, position, names, summaries) VALUES (?, ?, ?, ?)",
			pvID, bi, toJSON(b.Names), toJSON(b.Summary))
		if err != nil {
			return ImportResult{}, err
		}
		blockID, _ := res.LastInsertId()
		for si, st := range b.Steps {
			_, err := tx.ExecContext(ctx, `INSERT INTO step (block_id, position, exercise_id, duration_sec, variant, names, cues)
				VALUES (?, ?, (SELECT id FROM exercise WHERE key = ?), ?, ?, ?, ?)`,
				blockID, si, st.Exercise, st.Seconds, st.Variant, toJSON(st.Names), toJSON(st.Cues))
			if err != nil {
				return ImportResult{}, fmt.Errorf("save step: %w", err)
			}
		}
	}
	return ImportResult{Kind: "plan", Plan: p.Plan, Version: version, Created: true}, tx.Commit()
}

// ImportPeople adds or updates people. A person whose plan changes follows the new plan from today.
func (s *Store) ImportPeople(ctx context.Context, pf PeopleFile, today string) (ImportResult, error) {
	day, err := time.Parse(DateLayout, today)
	if err != nil {
		return ImportResult{}, fmt.Errorf("today: %w", err)
	}
	yesterday := day.AddDate(0, 0, -1).Format(DateLayout)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ImportResult{}, err
	}
	defer tx.Rollback()
	for _, p := range pf.People {
		if !validKey(p.Key) || p.Name == "" {
			return ImportResult{}, fmt.Errorf("person %q: needs a valid key and a name", p.Key)
		}
		lang := p.Language
		if lang == "" {
			lang = "de"
		}
		var personID int64
		err := tx.QueryRowContext(ctx, `INSERT INTO person (key, name, language) VALUES (?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET name = excluded.name, language = excluded.language RETURNING id`,
			p.Key, p.Name, lang).Scan(&personID)
		if err != nil {
			return ImportResult{}, err
		}
		if p.Plan == "" {
			continue
		}
		var planID int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM plan WHERE key = ?", p.Plan).Scan(&planID); err != nil {
			return ImportResult{}, fmt.Errorf("person %s: unknown plan %q", p.Key, p.Plan)
		}
		var aID, aPlan int64
		var aFrom string
		err = tx.QueryRowContext(ctx, "SELECT id, plan_id, valid_from FROM assignment WHERE person_id = ? AND valid_to IS NULL ORDER BY valid_from DESC LIMIT 1",
			personID).Scan(&aID, &aPlan, &aFrom)
		switch {
		case err == nil && aPlan == planID:
			continue // already on this plan
		case err == nil && aFrom >= today:
			_, err = tx.ExecContext(ctx, "UPDATE assignment SET plan_id = ? WHERE id = ?", planID, aID)
		case err == nil:
			if _, err = tx.ExecContext(ctx, "UPDATE assignment SET valid_to = ? WHERE id = ?", yesterday, aID); err == nil {
				_, err = tx.ExecContext(ctx, "INSERT INTO assignment (person_id, plan_id, valid_from) VALUES (?, ?, ?)", personID, planID, today)
			}
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, "INSERT INTO assignment (person_id, plan_id, valid_from) VALUES (?, ?, ?)", personID, planID, today)
		}
		if err != nil {
			return ImportResult{}, err
		}
	}
	return ImportResult{Kind: "people", People: len(pf.People)}, tx.Commit()
}

type Person struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Language string `json:"language"`
}

func (s *Store) People(ctx context.Context) ([]Person, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, name, language FROM person ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.Key, &p.Name, &p.Language); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Session is everything the page needs for one person's round today, already in their language.
type Session struct {
	Person Person      `json:"person"`
	Plan   SessionPlan `json:"plan"`
	Blocks []Block     `json:"blocks"`
	Stats  Stats       `json:"stats"`
}

type SessionPlan struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Version      int    `json:"version"`
	VersionID    int64  `json:"versionId"`
	TotalSeconds int    `json:"totalSeconds"`
}

type Block struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Seconds int    `json:"seconds"`
	Steps   []Step `json:"steps"`
}

type Step struct {
	Kind    string `json:"kind"` // exercise | rest | breath
	Name    string `json:"name"`
	Cue     string `json:"cue"`
	Seconds int    `json:"seconds"`
}

type Stats struct {
	Streak    int  `json:"streak"`
	TotalDays int  `json:"totalDays"`
	DoneToday bool `json:"doneToday"`
}

func (s *Store) person(ctx context.Context, key string) (int64, Person, error) {
	var id int64
	p := Person{Key: key}
	err := s.db.QueryRowContext(ctx, "SELECT id, name, language FROM person WHERE key = ?", key).Scan(&id, &p.Name, &p.Language)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, p, ErrNotFound
	}
	return id, p, err
}

// Today returns the plan version the person follows on the given date, plus their stats.
func (s *Store) Today(ctx context.Context, personKey, today string) (*Session, error) {
	personID, person, err := s.person(ctx, personKey)
	if err != nil {
		return nil, err
	}
	sess := &Session{Person: person}
	var namesJSON string
	// progression 'latest': the newest version of the assigned plan.
	err = s.db.QueryRowContext(ctx, `
		SELECT p.key, pv.id, pv.version, pv.names
		FROM assignment a
		JOIN plan p ON p.id = a.plan_id
		JOIN plan_version pv ON pv.plan_id = p.id
		WHERE a.person_id = ? AND a.valid_from <= ? AND (a.valid_to IS NULL OR a.valid_to >= ?)
		ORDER BY a.valid_from DESC, pv.version DESC LIMIT 1`, personID, today, today).
		Scan(&sess.Plan.Key, &sess.Plan.VersionID, &sess.Plan.Version, &namesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s has no plan for %s: %w", personKey, today, ErrNotFound)
	} else if err != nil {
		return nil, err
	}
	lang := person.Language
	sess.Plan.Name = parseText(namesJSON).Pick(lang)

	rows, err := s.db.QueryContext(ctx, `
		SELECT b.position, b.names, b.summaries, e.kind, e.names, e.cues, e.variants, s.variant, s.names, s.cues, s.duration_sec
		FROM block b
		JOIN step s ON s.block_id = b.id
		JOIN exercise e ON e.id = s.exercise_id
		WHERE b.plan_version_id = ?
		ORDER BY b.position, s.position`, sess.Plan.VersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lastPos := -1
	for rows.Next() {
		var pos, secs int
		var bNames, bSum, kind, eNames, eCues, eVariants, variant, sNames, sCues string
		if err := rows.Scan(&pos, &bNames, &bSum, &kind, &eNames, &eCues, &eVariants, &variant, &sNames, &sCues, &secs); err != nil {
			return nil, err
		}
		if pos != lastPos {
			sess.Blocks = append(sess.Blocks, Block{Name: parseText(bNames).Pick(lang), Summary: parseText(bSum).Pick(lang)})
			lastPos = pos
		}
		name := parseText(sNames).Pick(lang)
		if name == "" {
			name = parseText(eNames).Pick(lang)
			if variant != "" {
				var vs map[string]Text
				_ = json.Unmarshal([]byte(eVariants), &vs)
				if v := vs[variant].Pick(lang); v != "" {
					name += " (" + v + ")"
				}
			}
		}
		cue := parseText(sCues).Pick(lang)
		if cue == "" {
			cue = parseText(eCues).Pick(lang)
		}
		b := &sess.Blocks[len(sess.Blocks)-1]
		b.Steps = append(b.Steps, Step{Kind: kind, Name: name, Cue: cue, Seconds: secs})
		b.Seconds += secs
		sess.Plan.TotalSeconds += secs
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sess.Stats, err = s.stats(ctx, personID, today)
	return sess, err
}

func parseText(s string) Text {
	var t Text
	_ = json.Unmarshal([]byte(s), &t)
	return t
}

// RoundInput is what the page sends after a round.
type RoundInput struct {
	PlanVersionID int64  `json:"planVersionId"`
	Date          string `json:"date"`      // the person's local date, YYYY-MM-DD
	StartedAt     string `json:"startedAt"` // RFC 3339, optional
	Completed     bool   `json:"completed"`
	SkippedSteps  []int  `json:"skippedSteps"`
}

// AddRound records a round and returns the person's updated stats.
func (s *Store) AddRound(ctx context.Context, personKey string, in RoundInput) (Stats, error) {
	personID, _, err := s.person(ctx, personKey)
	if err != nil {
		return Stats{}, err
	}
	if _, err := time.Parse(DateLayout, in.Date); err != nil {
		return Stats{}, fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalid)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM plan_version WHERE id = ?", in.PlanVersionID).Scan(&n); err != nil || n == 0 {
		return Stats{}, fmt.Errorf("%w: unknown plan version %d", ErrInvalid, in.PlanVersionID)
	}
	skipped, _ := json.Marshal(in.SkippedSteps)
	if in.SkippedSteps == nil {
		skipped = []byte("[]")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO round (person_id, plan_version_id, date, started_at, recorded_at, completed, skipped_steps)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, personID, in.PlanVersionID, in.Date, in.StartedAt, time.Now().UTC().Format(time.RFC3339), in.Completed, string(skipped))
	if err != nil {
		return Stats{}, err
	}
	return s.stats(ctx, personID, in.Date)
}

// stats counts days with a completed round. The streak runs back from today,
// or from yesterday if today has no round yet (so the streak isn't "lost" before breakfast).
func (s *Store) stats(ctx context.Context, personID int64, today string) (Stats, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT date FROM round WHERE person_id = ? AND completed = 1", personID)
	if err != nil {
		return Stats{}, err
	}
	defer rows.Close()
	days := map[string]bool{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return Stats{}, err
		}
		days[d] = true
	}
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}
	st := Stats{TotalDays: len(days), DoneToday: days[today]}
	d, err := time.Parse(DateLayout, today)
	if err != nil {
		return st, err
	}
	if !st.DoneToday {
		d = d.AddDate(0, 0, -1)
	}
	for days[d.Format(DateLayout)] {
		st.Streak++
		d = d.AddDate(0, 0, -1)
	}
	return st, nil
}
