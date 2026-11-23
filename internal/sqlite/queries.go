package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/subpathdev/morgentraining/internal/training"
)

// Every method maps between training types and the row types in rows.go;
// no domain type is written to or read from the database directly.

// ---- people ----

func (r *repo) People(ctx context.Context) ([]training.Person, error) {
	rows, err := r.q.QueryContext(ctx, "SELECT key, name, language FROM person ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []training.Person{}
	for rows.Next() {
		var row personRow
		if err := rows.Scan(&row.Key, &row.Name, &row.Language); err != nil {
			return nil, err
		}
		out = append(out, row.toDomain())
	}
	return out, rows.Err()
}

func (r *repo) Person(ctx context.Context, key string) (training.Person, error) {
	row := personRow{Key: key}
	err := r.q.QueryRowContext(ctx, "SELECT name, language FROM person WHERE key = ?", key).Scan(&row.Name, &row.Language)
	if err != nil {
		return training.Person{}, notFound(err, "person "+key)
	}
	return row.toDomain(), nil
}

func (r *repo) SavePerson(ctx context.Context, p training.Person) error {
	row := personRowFrom(p)
	_, err := r.q.ExecContext(ctx, `INSERT INTO person (key, name, language) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET name = excluded.name, language = excluded.language`,
		row.Key, row.Name, row.Language)
	return err
}

// ---- exercises ----

func (r *repo) HasExercise(ctx context.Context, key string) (bool, error) {
	return r.exists(ctx, "SELECT 1 FROM exercise WHERE key = ?", key)
}

func (r *repo) SaveExercise(ctx context.Context, e training.Exercise) error {
	row := exerciseRowFrom(e)
	_, err := r.q.ExecContext(ctx, `INSERT INTO exercise (key, kind, names, cues, variants) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET kind = excluded.kind, names = excluded.names, cues = excluded.cues, variants = excluded.variants`,
		row.Key, row.Kind, row.Names, row.Cues, row.Variants)
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
	if err != nil {
		return training.VersionRef{}, notFound(err, "plan "+planKey)
	}
	return v, nil
}

func (r *repo) CreateVersion(ctx context.Context, spec training.PlanSpec, version int, hash string, createdAt time.Time) (int64, error) {
	if _, err := r.q.ExecContext(ctx, "INSERT INTO plan (key, created_by) VALUES (?, ?) ON CONFLICT (key) DO NOTHING",
		spec.Key, spec.CreatedBy); err != nil {
		return 0, err
	}
	var id int64
	err := r.q.QueryRowContext(ctx, `INSERT INTO plan_version (plan_id, version, names, created_at, hash)
		VALUES ((SELECT id FROM plan WHERE key = ?), ?, ?, ?, ?) RETURNING id`,
		spec.Key, version, textFrom(spec.Names), timeColumn{createdAt}, hash).Scan(&id)
	if err != nil {
		return 0, err
	}
	for bi, b := range spec.Blocks {
		block := blockRowFrom(bi, b)
		var blockID int64
		err = r.q.QueryRowContext(ctx, "INSERT INTO block (plan_version_id, position, names, summaries) VALUES (?, ?, ?, ?) RETURNING id",
			id, block.Position, block.Names, block.Summaries).Scan(&blockID)
		if err != nil {
			return 0, err
		}
		for si, s := range b.Steps {
			step := stepRowFrom(si, s)
			_, err = r.q.ExecContext(ctx, `INSERT INTO step (block_id, position, exercise_id, duration_sec, variant, names, cues)
				VALUES (?, ?, (SELECT id FROM exercise WHERE key = ?), ?, ?, ?, ?)`,
				blockID, step.Position, step.Exercise, step.DurationSec, step.Variant, step.Names, step.Cues)
			if err != nil {
				return 0, fmt.Errorf("block %d step %d: %w", bi+1, si+1, err)
			}
		}
	}
	return id, nil
}

func (r *repo) CurrentVersions(ctx context.Context) ([]training.PlanVersion, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT pv.id, p.key, pv.version, pv.names
		FROM plan p
		JOIN plan_version pv ON pv.plan_id = p.id
		WHERE pv.version = (SELECT MAX(version) FROM plan_version WHERE plan_id = p.id)
		ORDER BY p.key`)
	if err != nil {
		return nil, err
	}
	var heads []versionRow
	for rows.Next() {
		var v versionRow
		if err := rows.Scan(&v.ID, &v.PlanKey, &v.Version, &v.Names); err != nil {
			rows.Close()
			return nil, err
		}
		heads = append(heads, v)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// The rows are read before loading each version: one connection, no nested queries.
	out := make([]training.PlanVersion, 0, len(heads))
	for _, v := range heads {
		pv, err := r.load(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, pv)
	}
	return out, nil
}

func (r *repo) CurrentVersion(ctx context.Context, planKey string) (training.PlanVersion, error) {
	var v versionRow
	err := r.q.QueryRowContext(ctx, `
		SELECT pv.id, p.key, pv.version, pv.names
		FROM plan p
		JOIN plan_version pv ON pv.plan_id = p.id
		WHERE p.key = ?
		ORDER BY pv.version DESC LIMIT 1`, planKey).
		Scan(&v.ID, &v.PlanKey, &v.Version, &v.Names)
	if err != nil {
		return training.PlanVersion{}, notFound(err, "plan "+planKey)
	}
	return r.load(ctx, v)
}

// AssignedVersion follows progression 'latest': the newest version of the assigned plan.
func (r *repo) AssignedVersion(ctx context.Context, personKey string, day training.Date) (training.PlanVersion, error) {
	var v versionRow
	err := r.q.QueryRowContext(ctx, `
		SELECT pv.id, p.key, pv.version, pv.names
		FROM assignment a
		JOIN person pe ON pe.id = a.person_id
		JOIN plan p ON p.id = a.plan_id
		JOIN plan_version pv ON pv.plan_id = p.id
		WHERE pe.key = ? AND a.valid_from <= ? AND (a.valid_to IS NULL OR a.valid_to >= ?)
		ORDER BY a.valid_from DESC, pv.version DESC LIMIT 1`, personKey, dateColumn{day}, dateColumn{day}).
		Scan(&v.ID, &v.PlanKey, &v.Version, &v.Names)
	if err != nil {
		return training.PlanVersion{}, notFound(err, "plan of "+personKey)
	}
	return r.load(ctx, v)
}

// load reads the blocks, steps and exercises of a plan version.
func (r *repo) load(ctx context.Context, v versionRow) (training.PlanVersion, error) {
	blocks, steps, err := r.blocks(ctx, v.ID)
	if err != nil {
		return training.PlanVersion{}, fmt.Errorf("plan version %d: %w", v.ID, err)
	}
	exercises, err := r.exercisesOf(ctx, v.ID)
	if err != nil {
		return training.PlanVersion{}, fmt.Errorf("plan version %d: %w", v.ID, err)
	}
	return planVersionFrom(v, blocks, steps, exercises), nil
}

// blocks returns the blocks of a plan version in order, and for each block its steps in order.
func (r *repo) blocks(ctx context.Context, versionID int64) ([]blockRow, [][]stepRow, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT b.position, b.names, b.summaries, s.position, e.key, s.duration_sec, s.variant, s.names, s.cues
		FROM block b
		JOIN step s ON s.block_id = b.id
		JOIN exercise e ON e.id = s.exercise_id
		WHERE b.plan_version_id = ?
		ORDER BY b.position, s.position`, versionID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var blocks []blockRow
	var steps [][]stepRow
	for rows.Next() {
		var b blockRow
		var s stepRow
		if err := rows.Scan(&b.Position, &b.Names, &b.Summaries, &s.Position, &s.Exercise, &s.DurationSec, &s.Variant, &s.Names, &s.Cues); err != nil {
			return nil, nil, err
		}
		if len(blocks) == 0 || blocks[len(blocks)-1].Position != b.Position {
			blocks = append(blocks, b)
			steps = append(steps, nil)
		}
		steps[len(steps)-1] = append(steps[len(steps)-1], s)
	}
	return blocks, steps, rows.Err()
}

func (r *repo) exercisesOf(ctx context.Context, versionID int64) ([]exerciseRow, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT DISTINCT e.key, e.kind, e.names, e.cues, e.variants
		FROM exercise e
		JOIN step s ON s.exercise_id = e.id
		JOIN block b ON b.id = s.block_id
		WHERE b.plan_version_id = ?`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []exerciseRow
	for rows.Next() {
		var e exerciseRow
		if err := rows.Scan(&e.Key, &e.Kind, &e.Names, &e.Cues, &e.Variants); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- assignments ----

func (r *repo) OpenAssignment(ctx context.Context, personKey string) (training.Assignment, error) {
	var row assignmentRow
	err := r.q.QueryRowContext(ctx, `
		SELECT a.id, p.key, a.valid_from
		FROM assignment a
		JOIN person pe ON pe.id = a.person_id
		JOIN plan p ON p.id = a.plan_id
		WHERE pe.key = ? AND a.valid_to IS NULL
		ORDER BY a.valid_from DESC LIMIT 1`, personKey).Scan(&row.ID, &row.PlanKey, &row.ValidFrom)
	if err != nil {
		return training.Assignment{}, notFound(err, "assignment of "+personKey)
	}
	return row.toDomain(), nil
}

func (r *repo) SetAssignmentPlan(ctx context.Context, id int64, planKey string) error {
	_, err := r.q.ExecContext(ctx, "UPDATE assignment SET plan_id = (SELECT id FROM plan WHERE key = ?) WHERE id = ?", planKey, id)
	return err
}

func (r *repo) EndAssignment(ctx context.Context, id int64, lastDay training.Date) error {
	_, err := r.q.ExecContext(ctx, "UPDATE assignment SET valid_to = ? WHERE id = ?", dateColumn{lastDay}, id)
	return err
}

func (r *repo) StartAssignment(ctx context.Context, personKey, planKey string, from training.Date) error {
	_, err := r.q.ExecContext(ctx, `INSERT INTO assignment (person_id, plan_id, valid_from)
		VALUES ((SELECT id FROM person WHERE key = ?), (SELECT id FROM plan WHERE key = ?), ?)`,
		personKey, planKey, dateColumn{from})
	return err
}

// ---- rounds ----

func (r *repo) AddRound(ctx context.Context, personKey string, rd training.Round) error {
	row := roundRowFrom(rd)
	_, err := r.q.ExecContext(ctx, `INSERT INTO round (person_id, plan_version_id, date, started_at, recorded_at, completed, skipped_steps)
		VALUES ((SELECT id FROM person WHERE key = ?), ?, ?, ?, ?, ?, ?)`,
		personKey, row.PlanVersionID, row.Date, row.StartedAt, row.RecordedAt, row.Completed, row.SkippedSteps)
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
		var d dateColumn
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("round of %s: %w", personKey, err)
		}
		out = append(out, d.Date)
	}
	return out, rows.Err()
}
