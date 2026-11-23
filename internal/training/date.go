package training

import (
	"fmt"
	"time"
)

const dateLayout = "2006-01-02"

// Date is a calendar day without a time or time zone, such as a person's local
// "today". The zero Date is not a valid day; check it with IsZero.
// Dates are comparable with == and can be used as map keys.
type Date struct {
	t time.Time // always midnight UTC
}

// NewDate returns the given day. Out-of-range values are normalised like time.Date does.
func NewDate(year int, month time.Month, day int) Date {
	return Date{time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// DateOf returns the calendar day of t in t's own location.
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return NewDate(y, m, d)
}

// ParseDate parses a day in the form YYYY-MM-DD.
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return Date{}, fmt.Errorf("date %q: want YYYY-MM-DD", s)
	}
	return Date{t}, nil
}

// String formats the day as YYYY-MM-DD.
func (d Date) String() string { return d.t.Format(dateLayout) }

func (d Date) IsZero() bool           { return d.t.IsZero() }
func (d Date) AddDays(n int) Date     { return Date{d.t.AddDate(0, 0, n)} }
func (d Date) Before(other Date) bool { return d.t.Before(other.t) }
func (d Date) After(other Date) bool  { return d.t.After(other.t) }
func (d Date) Year() int              { return d.t.Year() }
func (d Date) Month() time.Month      { return d.t.Month() }
func (d Date) Day() int               { return d.t.Day() }
func (d Date) Time(loc *time.Location) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
}
