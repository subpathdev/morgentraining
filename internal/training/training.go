// Package training is the domain of Morgentraining: people, versioned workout
// plans, rounds and streaks.
//
// It knows nothing about HTTP, YAML or SQL. Storage is reached through the
// Repository interface defined in this package; Service holds the use cases.
package training

import (
	"errors"
	"strings"
)

var (
	// ErrNotFound means a person, plan or plan version does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid means the input breaks a rule. Errors of type *ValidationError match it.
	ErrInvalid = errors.New("invalid input")
)

// ValidationError lists every problem found in an input, so a plan author can
// fix them all at once. errors.Is(err, ErrInvalid) reports true for it.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid input: " + strings.Join(e.Problems, "; ")
}

func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

func invalid(problems ...string) error {
	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: problems}
}

// ValidKey reports whether s can be used as a key for a person, plan or exercise:
// 1 to 64 characters out of a-z, 0-9, '-' and '_'.
func ValidKey(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
