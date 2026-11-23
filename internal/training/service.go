package training

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PlanCatalog reads saved plans. Methods return ErrNotFound (possibly wrapped)
// when the plan doesn't exist.
type PlanCatalog interface {
	// CurrentVersions returns the newest version of every plan, ordered by plan key.
	CurrentVersions(ctx context.Context) ([]PlanVersion, error)
	// CurrentVersion returns the newest version of one plan.
	CurrentVersion(ctx context.Context, planKey string) (PlanVersion, error)
}

// Repository is the storage the Service needs. Methods return ErrNotFound
// (possibly wrapped) when the thing asked for doesn't exist.
type Repository interface {
	PlanCatalog

	// Atomic runs fn in one transaction: all of fn's writes are kept, or none.
	// The Repository passed to fn must be used inside fn instead of the outer one.
	Atomic(ctx context.Context, fn func(Repository) error) error

	AnyPlan(ctx context.Context) (bool, error)

	People(ctx context.Context) ([]Person, error)
	Person(ctx context.Context, key string) (Person, error)
	SavePerson(ctx context.Context, p Person) error

	HasExercise(ctx context.Context, key string) (bool, error)
	SaveExercise(ctx context.Context, e Exercise) error

	HasPlan(ctx context.Context, key string) (bool, error)
	LatestVersion(ctx context.Context, planKey string) (VersionRef, error)
	// CreateVersion saves spec as the given version (creating the plan on first use) and returns its ID.
	CreateVersion(ctx context.Context, spec PlanSpec, version int, hash string, createdAt time.Time) (int64, error)
	HasVersion(ctx context.Context, id int64) (bool, error)
	// AssignedVersion returns the newest version of the plan the person follows on day.
	AssignedVersion(ctx context.Context, personKey string, day Date) (PlanVersion, error)

	OpenAssignment(ctx context.Context, personKey string) (Assignment, error)
	SetAssignmentPlan(ctx context.Context, id int64, planKey string) error
	EndAssignment(ctx context.Context, id int64, lastDay Date) error
	StartAssignment(ctx context.Context, personKey, planKey string, from Date) error

	AddRound(ctx context.Context, personKey string, r Round) error
	// CompletedDays returns the days with at least one completed round, in any order.
	CompletedDays(ctx context.Context, personKey string) ([]Date, error)
}

// Service holds the use cases of Morgentraining.
type Service struct {
	repo Repository
	loc  *time.Location
	now  func() time.Time
}

// Option configures a Service.
type Option func(*Service)

// WithClock replaces time.Now, e.g. in tests.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// NewService returns a Service that stores in repo. loc decides which day "today" is.
func NewService(repo Repository, loc *time.Location, opts ...Option) *Service {
	s := &Service{repo: repo, loc: loc, now: time.Now}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Today is the current day in the service's time zone.
func (s *Service) Today() Date { return DateOf(s.now().In(s.loc)) }

// HasPlans reports whether any plan was imported yet.
func (s *Service) HasPlans(ctx context.Context) (bool, error) { return s.repo.AnyPlan(ctx) }

func (s *Service) People(ctx context.Context) ([]Person, error) { return s.repo.People(ctx) }

// Session returns the plan the person follows on day, in their language, and their stats.
func (s *Service) Session(ctx context.Context, personKey string, day Date) (Session, error) {
	p, err := s.repo.Person(ctx, personKey)
	if err != nil {
		return Session{}, err
	}
	pv, err := s.repo.AssignedVersion(ctx, personKey, day)
	if errors.Is(err, ErrNotFound) {
		return Session{}, fmt.Errorf("%s has no plan on %s: %w", personKey, day, ErrNotFound)
	} else if err != nil {
		return Session{}, err
	}
	days, err := s.repo.CompletedDays(ctx, personKey)
	if err != nil {
		return Session{}, err
	}
	plan, blocks := pv.Localize(p.Language)
	return Session{Person: p, Plan: plan, Blocks: blocks, Stats: ComputeStats(days, day)}, nil
}

// Plans returns the newest version of every plan, ordered by key.
func (s *Service) Plans(ctx context.Context) ([]PlanVersion, error) {
	return s.repo.CurrentVersions(ctx)
}

// Plan returns the newest version of a plan, with the exercises it uses.
func (s *Service) Plan(ctx context.Context, key string) (PlanVersion, error) {
	return s.repo.CurrentVersion(ctx, key)
}

// RoundInput is a round as reported by the page.
type RoundInput struct {
	PlanVersionID int64
	Date          Date
	StartedAt     time.Time // zero if unknown
	Completed     bool
	SkippedSteps  []int
}

// RecordRound saves a round and returns the person's stats as of the round's day.
func (s *Service) RecordRound(ctx context.Context, personKey string, in RoundInput) (Stats, error) {
	var problems []string
	if in.Date.IsZero() {
		problems = append(problems, "round needs a date")
	}
	for _, i := range in.SkippedSteps {
		if i < 0 {
			problems = append(problems, fmt.Sprintf("skipped step %d: positions start at 0", i))
		}
	}
	if err := invalid(problems...); err != nil {
		return Stats{}, err
	}
	var st Stats
	err := s.repo.Atomic(ctx, func(r Repository) error {
		if _, err := r.Person(ctx, personKey); err != nil {
			return err
		}
		ok, err := r.HasVersion(ctx, in.PlanVersionID)
		if err != nil {
			return err
		}
		if !ok {
			return invalid(fmt.Sprintf("unknown plan version %d", in.PlanVersionID))
		}
		err = r.AddRound(ctx, personKey, Round{
			PlanVersionID: in.PlanVersionID,
			Date:          in.Date,
			StartedAt:     in.StartedAt,
			RecordedAt:    s.now().UTC(),
			Completed:     in.Completed,
			SkippedSteps:  in.SkippedSteps,
		})
		if err != nil {
			return err
		}
		days, err := r.CompletedDays(ctx, personKey)
		st = ComputeStats(days, in.Date)
		return err
	})
	return st, err
}

// PlanImport is a plan together with the exercises it adds or updates in the library.
type PlanImport struct {
	Plan      PlanSpec
	Exercises []Exercise
}

// PlanResult says what ImportPlan saved.
type PlanResult struct {
	PlanKey string
	Version int  // the plan's newest version after the import
	Created bool // false if the content was unchanged
}

// ImportPlan saves the exercises and, if the plan content changed, a new plan version.
func (s *Service) ImportPlan(ctx context.Context, imp PlanImport) (PlanResult, error) {
	var res PlanResult
	err := s.repo.Atomic(ctx, func(r Repository) error {
		if err := validatePlan(ctx, r, imp); err != nil {
			return err
		}
		for _, e := range imp.Exercises {
			if err := r.SaveExercise(ctx, e); err != nil {
				return fmt.Errorf("save exercise %s: %w", e.Key, err)
			}
		}
		hash := imp.Plan.Hash()
		latest, err := r.LatestVersion(ctx, imp.Plan.Key)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if latest.Hash == hash {
			res = PlanResult{PlanKey: imp.Plan.Key, Version: latest.Version}
			return nil // exercise texts may still have changed; those are kept
		}
		version := latest.Version + 1
		if _, err := r.CreateVersion(ctx, imp.Plan, version, hash, s.now().UTC()); err != nil {
			return fmt.Errorf("save plan %s version %d: %w", imp.Plan.Key, version, err)
		}
		res = PlanResult{PlanKey: imp.Plan.Key, Version: version, Created: true}
		return nil
	})
	return res, err
}

func validatePlan(ctx context.Context, r Repository, imp PlanImport) error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	p := imp.Plan
	if !ValidKey(p.Key) {
		add("plan key %q: use lowercase letters, digits, - or _", p.Key)
	}
	if p.Names.Pick("") == "" {
		add("plan needs a name")
	}
	defined := map[string]bool{}
	for i, e := range imp.Exercises {
		if !ValidKey(e.Key) {
			add("exercise %d: key %q is not valid", i+1, e.Key)
		}
		if !e.Kind.Valid() {
			add("exercise %s: kind %q must be exercise, rest or breath", e.Key, e.Kind)
		}
		if e.Names.Pick("") == "" {
			add("exercise %s: needs a name", e.Key)
		}
		defined[e.Key] = true
	}
	if len(p.Blocks) == 0 {
		add("plan needs at least one block")
	}
	known := map[string]bool{}
	for i, b := range p.Blocks {
		if b.Names.Pick("") == "" {
			add("block %d: needs a name", i+1)
		}
		if len(b.Steps) == 0 {
			add("block %d: needs at least one step", i+1)
		}
		for j, st := range b.Steps {
			if st.Seconds <= 0 || st.Seconds > 3600 {
				add("block %d step %d: seconds must be 1-3600", i+1, j+1)
			}
			if defined[st.Exercise] {
				continue
			}
			ok, seen := known[st.Exercise]
			if !seen {
				var err error
				if ok, err = r.HasExercise(ctx, st.Exercise); err != nil {
					return err
				}
				known[st.Exercise] = ok
			}
			if !ok {
				add("block %d step %d: unknown exercise %q", i+1, j+1, st.Exercise)
			}
		}
	}
	return invalid(problems...)
}

// PersonImport adds or updates a person. A non-empty Plan is the plan key they follow from today.
type PersonImport struct {
	Person Person
	Plan   string
}

// ImportPeople saves people and their plans. A person whose plan changes follows
// the new plan from today; earlier days keep the old plan.
func (s *Service) ImportPeople(ctx context.Context, people []PersonImport) error {
	var problems []string
	for _, p := range people {
		problems = append(problems, p.Person.problems()...)
	}
	if err := invalid(problems...); err != nil {
		return err
	}
	today := s.Today()
	return s.repo.Atomic(ctx, func(r Repository) error {
		for _, p := range people {
			if err := savePerson(ctx, r, p, today); err != nil {
				return err
			}
		}
		return nil
	})
}

// SavePerson adds one person or updates them, with the same rules as ImportPeople,
// and reports whether the person is new.
func (s *Service) SavePerson(ctx context.Context, p PersonImport) (created bool, err error) {
	if err := invalid(p.Person.problems()...); err != nil {
		return false, err
	}
	today := s.Today()
	err = s.repo.Atomic(ctx, func(r Repository) error {
		_, err := r.Person(ctx, p.Person.Key)
		switch {
		case errors.Is(err, ErrNotFound):
			created = true
		case err != nil:
			return err
		}
		return savePerson(ctx, r, p, today)
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// savePerson saves the person and, if a plan is given, has them follow it from today.
func savePerson(ctx context.Context, r Repository, p PersonImport, today Date) error {
	if err := r.SavePerson(ctx, p.Person); err != nil {
		return err
	}
	if p.Plan == "" {
		return nil
	}
	if ok, err := r.HasPlan(ctx, p.Plan); err != nil {
		return err
	} else if !ok {
		return invalid(fmt.Sprintf("person %s: unknown plan %q", p.Person.Key, p.Plan))
	}
	if err := assign(ctx, r, p.Person.Key, p.Plan, today); err != nil {
		return fmt.Errorf("person %s: %w", p.Person.Key, err)
	}
	return nil
}

func assign(ctx context.Context, r Repository, personKey, planKey string, today Date) error {
	cur, err := r.OpenAssignment(ctx, personKey)
	switch {
	case errors.Is(err, ErrNotFound):
		return r.StartAssignment(ctx, personKey, planKey, today)
	case err != nil:
		return err
	case cur.PlanKey == planKey:
		return nil
	case !cur.From.Before(today):
		// The current assignment starts today or later, so no day has used it yet.
		return r.SetAssignmentPlan(ctx, cur.ID, planKey)
	default:
		if err := r.EndAssignment(ctx, cur.ID, today.AddDays(-1)); err != nil {
			return err
		}
		return r.StartAssignment(ctx, personKey, planKey, today)
	}
}
