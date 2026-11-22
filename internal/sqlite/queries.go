package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/subpathdev/morgentraining/internal/training"
)

// ---- people ----

func (r *repo) People(ctx context.Context) ([]training.Person, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT key, name, language FROM person ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []training.Person{}
	for rows.Next() {
		var p training.Person
		if err := rows.Scan(&p.Key, &p.Name, &p.Language); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *repo) Person(ctx context.Context, key string) (training.Person, error) {
	p := training.Person{Key: key}
	err := r.q.QueryRowContext(ctx, "SELECT name, language FROM person WHERE key = ?", key).Scan(&p.Name, &p.Language)
	return p, notFound(err, "person "+key)
}

func (r *repo) SavePerson(ctx context.Context, p training.Person) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO person (key, name, language) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET name = excluded.name, language = excluded.language`,
		p.Key, p.Name, p.Language)
	return err
}

// ---- exercises ----

func (r *repo) HasExercise(ctx context.Context, key string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM exercise WHERE key = ?", key)
}

func (r *repo) SaveExercise(ctx context.Context, e training.Exercise) error {
	names, cues, variants, err := encode3(e.Names, e.Cues, e.Variants)
	if err != nil {
		return err
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO exercise (key, kind, names, cues, variants) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET kind = excluded.kind, names = excluded.names, cues = excluded.cues, variants = excluded.variants`,
		e.Key, string(e.Kind), names, cues, variants)
	return err
}

// ---- plans ----

func (r *repo) AnyPlan(ctx context.Context) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM plan")
}

func (r *repo) HasPlan(ctx context.Context, key string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM plan WHERE key = ?", key)
}

func (r *repo) HasVersion(ctx context.Context, id int64) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM plan_version WHERE id = ?", id)
}

func (r *repo) LatestVersion(ctx context.Context, planKey string) (training.VersionRef, error) {
	var v training.VersionRef
	err := r.q.QueryRowContext(ctx, `SELECT pv.version, pv.hash FROM plan_version pv JOIN plan p ON p.id = pv.plan_id
		WHERE p.key = ? ORDER BY pv.version DESC LIMIT 1`, planKey).Scan(&v.Version, &v.Hash)
	return v, notFound(err, "plan "+planKey)
}

func (r *repo) CreateVersion(ctx context.Context, spec training.PlanSpec, version int, hash string, createdAt time.Time) (int64, error) {
	if _, err := r.q.ExecContext(ctx, "INSERT INTO plan (key, created_by) VALUES (?, ?) ON CONFLICT (key) DO NOTHING",
		spec.Key, spec.CreatedBy); err != nil {
		return 0, err
	}
	names, err := encode(spec.Names)
	if err != nil {
		return 0, err
	}
	var id int64
	err = r.q.QueryRowContext(ctx, `INSERT INTO plan_version (plan_id, version, names, created_at, hash)
		VALUES ((SELECT id FROM plan WHERE key = ?), ?, ?, ?, ?) RETURNING id`,
		spec.Key, version, names, createdAt.UTC().Format(time.RFC3339), hash).Scan(&id)
	if err != nil {
		return 0, err
	}
	for bi, b := range spec.Blocks {
		bNames, bSums, err := encode2(b.Names, b.Summaries)
		if err != nil {
			return 0, err
		}
		var blockID int64
		err = r.q.QueryRowContext(ctx, "INSERT INTO block (plan_version_id, position, names, summaries) VALUES (?, ?, ?, ?) RETURNING id",
			id, bi, bNames, bSums).Scan(&blockID)
		if err != nil {
			return 0, err
		}
		for si, s := range b.Steps {
			sNames, sCues, err := encode2(s.Names, s.Cues)
			if err != nil {
				return 0, err
			}
			_, err = r.q.ExecContext(ctx, `INSERT INTO step (block_id, position, exercise_id, duration_sec, variant, names, cues)
				VALUES (?, ?, (SELECT id FROM exercise WHERE key = ?), ?, ?, ?, ?)`,
				blockID, si, s.Exercise, s.Seconds, s.Variant, sNames, sCues)
			if err != nil {
				return 0, fmt.Errorf("block %d step %d: %w", bi+1, si+1, err)
			}
		}
	}
	return id, nil
}

// AssignedVersion follows progression 'latest': the newest version of the assigned plan.
func (r *repo) AssignedVersion(ctx context.Context, personKey string, day training.Date) (training.PlanVersion, error) {
	pv := training.PlanVersion{Exercises: map[string]training.Exercise{}}
	var names string
	err := r.q.QueryRowContext(ctx, `
		SELECT p.key, pv.id, pv.version, pv.names
		FROM assignment a
		JOIN person pe ON pe.id = a.person_id
		JOIN plan p ON p.id = a.plan_id
		JOIN plan_version pv ON pv.plan_id = p.id
		WHERE pe.key = ? AND a.valid_from <= ? AND (a.valid_to IS NULL OR a.valid_to >= ?)
		ORDER BY a.valid_from DESC, pv.version DESC LIMIT 1`, personKey, day.String(), day.String()).
		Scan(&pv.PlanKey, &pv.ID, &pv.Version, &names)
	if err != nil {
		return pv, notFound(err, "plan of "+personKey)
	}
	if err := decode(names, &pv.Names); err != nil {
		return pv, err
	}
	if err := r.loadBlocks(ctx, &pv); err != nil {
		return pv, fmt.Errorf("plan version %d: %w", pv.ID, err)
	}
	if err := r.loadExercises(ctx, &pv); err != nil {
		return pv, fmt.Errorf("plan version %d: %w", pv.ID, err)
	}
	return pv, nil
}

func (r *repo) loadBlocks(ctx context.Context, pv *training.PlanVersion) error {
	rows, err := r.q.QueryContext(ctx, `
		SELECT b.position, b.names, b.summaries, e.key, s.duration_sec, s.variant, s.names, s.cues
		FROM block b
		JOIN step s ON s.block_id = b.id
		JOIN exercise e ON e.id = s.exercise_id
		WHERE b.plan_version_id = ?
		ORDER BY b.position, s.position`, pv.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	last := -1
	for rows.Next() {
		var pos int
		var bNames, bSums, sNames, sCues string
		var s training.StepSpec
		if err := rows.Scan(&pos, &bNames, &bSums, &s.Exercise, &s.Seconds, &s.Variant, &sNames, &sCues); err != nil {
			return err
		}
		if pos != last {
			var b training.BlockSpec
			if err := decode2(bNames, &b.Names, bSums, &b.Summaries); err != nil {
				return err
			}
			pv.Blocks = append(pv.Blocks, b)
			last = pos
		}
		if err := decode2(sNames, &s.Names, sCues, &s.Cues); err != nil {
			return err
		}
		b := &pv.Blocks[len(pv.Blocks)-1]
		b.Steps = append(b.Steps, s)
	}
	return rows.Err()
}

func (r *repo) loadExercises(ctx context.Context, pv *training.PlanVersion) error {
	rows, err := r.q.QueryContext(ctx, `
		SELECT DISTINCT e.key, e.kind, e.names, e.cues, e.variants
		FROM exercise e
		JOIN step s ON s.exercise_id = e.id
		JOIN block b ON b.id = s.block_id
		WHERE b.plan_version_id = ?`, pv.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e training.Exercise
		var kind, names, cues, variants string
		if err := rows.Scan(&e.Key, &kind, &names, &cues, &variants); err != nil {
			return err
		}
		e.Kind = training.StepKind(kind)
		if err := decode2(names, &e.Names, cues, &e.Cues); err != nil {
			return err
		}
		if err := decode(variants, &e.Variants); err != nil {
			return err
		}
		pv.Exercises[e.Key] = e
	}
	return rows.Err()
}

// ---- assignments ----

func (r *repo) OpenAssignment(ctx context.Context, personKey string) (training.Assignment, error) {
	var a training.Assignment
	var from string
	err := r.q.QueryRowContext(ctx, `
		SELECT a.id, p.key, a.valid_from
		FROM assignment a
		JOIN person pe ON pe.id = a.person_id
		JOIN plan p ON p.id = a.plan_id
		WHERE pe.key = ? AND a.valid_to IS NULL
		ORDER BY a.valid_from DESC LIMIT 1`, personKey).Scan(&a.ID, &a.PlanKey, &from)
	if err != nil {
		return a, notFound(err, "assignment of "+personKey)
	}
	a.From, err = training.ParseDate(from)
	return a, err
}

func (r *repo) SetAssignmentPlan(ctx context.Context, id int64, planKey string) error {
	_, err := r.q.ExecContext(ctx, "UPDATE assignment SET plan_id = (SELECT id FROM plan WHERE key = ?) WHERE id = ?", planKey, id)
	return err
}

func (r *repo) EndAssignment(ctx context.Context, id int64, lastDay training.Date) error {
	_, err := r.q.ExecContext(ctx, "UPDATE assignment SET valid_to = ? WHERE id = ?", lastDay.String(), id)
	return err
}

func (r *repo) StartAssignment(ctx context.Context, personKey, planKey string, from training.Date) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO assignment (person_id, plan_id, valid_from)
		VALUES ((SELECT id FROM person WHERE key = ?), (SELECT id FROM plan WHERE key = ?), ?)`,
		personKey, planKey, from.String())
	return err
}

// ---- rounds ----

func (r *repo) AddRound(ctx context.Context, personKey string, rd training.Round) error {
	skipped := rd.SkippedSteps
	if skipped == nil {
		skipped = []int{}
	}
	skippedJSON, err := encode(skipped)
	if err != nil {
		return err
	}
	started := ""
	if !rd.StartedAt.IsZero() {
		started = rd.StartedAt.UTC().Format(time.RFC3339)
	}
	_, err = r.q.ExecContext(ctx, `INSERT INTO round (person_id, plan_version_id, date, started_at, recorded_at, completed, skipped_steps)
		VALUES ((SELECT id FROM person WHERE key = ?), ?, ?, ?, ?, ?, ?)`,
		personKey, rd.PlanVersionID, rd.Date.String(), started, rd.RecordedAt.UTC().Format(time.RFC3339), rd.Completed, skippedJSON)
	return err
}

func (r *repo) CompletedDays(ctx context.Context, personKey string) ([]training.Date, error) {
	rows, err := r.q.QueryContext(ctx, `SELECT DISTINCT r.date FROM round r JOIN person p ON p.id = r.person_id
		WHERE p.key = ? AND r.completed = 1`, personKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []training.Date
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		d, err := training.ParseDate(s)
		if err != nil {
			return nil, fmt.Errorf("round of %s: %w", personKey, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- JSON columns ----

// encode stores v as JSON; nil maps become {} to match the column defaults.
func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return "{}", nil
	}
	return string(b), nil
}

func encode2(a, b any) (string, string, error) {
	ea, err := encode(a)
	if err != nil {
		return "", "", err
	}
	eb, err := encode(b)
	return ea, eb, err
}

func encode3(a, b, c any) (string, string, string, error) {
	ea, eb, err := encode2(a, b)
	if err != nil {
		return "", "", "", err
	}
	ec, err := encode(c)
	return ea, eb, ec, err
}

func decode(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fmt.Errorf("corrupt JSON column %q: %w", s, err)
	}
	return nil
}

func decode2(a string, va any, b string, vb any) error {
	if err := decode(a, va); err != nil {
		return err
	}
	return decode(b, vb)
}
