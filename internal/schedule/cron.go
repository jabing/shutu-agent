package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronScanHorizon bounds the minute-by-minute scan in cronExpr.next so an
// expression can never loop forever. The rarest supported occurrence is Feb 29
// in a leap year (gap between fires ≤ 4 years; with day-of-week OR semantics
// the gap is never larger), so 8 years is a generous bound for any expression
// that passed parseCronSpec's never-matches check.
const cronScanHorizon = 8 * 365 * 24 * 60 // minutes

// cronExpr is a parsed 5-field cron expression (minute hour day month
// weekday). It is a zero-dependency hand-rolled scheduler: next() matches
// minute by minute, which keeps the day-of-month / day-of-week OR semantics
// (standard cron) simple and correct.
type cronExpr struct {
	minute  bitset // 0-59
	hour    bitset // 0-23
	day     bitset // 1-31
	month   bitset // 1-12
	weekday bitset // 0-6, Sunday = 0 (matches Go's time.Weekday)

	dayRestricted     bool // day field is not "*"
	weekdayRestricted bool // weekday field is not "*"
}

// bitset is a compact set of small integers (every field value is < 64).
type bitset uint64

func (b bitset) has(i int) bool {
	return i >= 0 && i < 64 && (b>>uint(i))&1 == 1
}

// cronField describes one field's value range and how to parse its text.
type cronField struct {
	name string
	min  int
	max  int
}

// parse turns one field's text ("*", "a-b", "*/n", "a-b/n", comma lists) into
// a bitset plus whether the field is restricted (not "*").
func (f cronField) parse(text string) (bitset, bool, error) {
	var out bitset
	for _, part := range strings.Split(text, ",") {
		if part == "" {
			return 0, false, fmt.Errorf("cron: empty value in %s field", f.name)
		}
		set, err := f.parseItem(part)
		if err != nil {
			return 0, false, err
		}
		out |= set
	}
	return out, text != "*", nil
}

func (f cronField) parseItem(item string) (bitset, error) {
	base, stepStr := item, ""
	if i := strings.IndexByte(item, '/'); i >= 0 {
		base, stepStr = item[:i], item[i+1:]
	}
	step := 1
	if stepStr != "" {
		n, err := strconv.Atoi(stepStr)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("cron: invalid step %q in %s field", stepStr, f.name)
		}
		step = n
	}
	if base == "*" {
		return f.rangeSet(f.min, f.max, step)
	}
	if i := strings.IndexByte(base, '-'); i >= 0 {
		lo, err1 := strconv.Atoi(base[:i])
		hi, err2 := strconv.Atoi(base[i+1:])
		if err1 != nil || err2 != nil {
			return 0, fmt.Errorf("cron: invalid range %q in %s field", base, f.name)
		}
		return f.rangeSet(lo, hi, step)
	}
	n, err := strconv.Atoi(base)
	if err != nil {
		return 0, fmt.Errorf("cron: invalid value %q in %s field", base, f.name)
	}
	return f.rangeSet(n, n, step)
}

// rangeSet builds the bitset for lo..hi stepping by step, validating bounds.
func (f cronField) rangeSet(lo, hi, step int) (bitset, error) {
	if lo < f.min || hi > f.max || lo > hi {
		return 0, fmt.Errorf("cron: %d-%d out of range for %s (valid %d-%d)", lo, hi, f.name, f.min, f.max)
	}
	var out bitset
	for v := lo; v <= hi; v += step {
		out |= 1 << uint(v)
	}
	return out, nil
}

// parseCronSpec parses and validates a 5-field cron expression, rejecting
// malformed text and expressions that can never occur on any real calendar
// date (e.g. "0 0 31 2 *").
func parseCronSpec(spec string) (*cronExpr, error) {
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf("%w: cron: expected 5 fields (minute hour day month weekday), got %d", ErrInvalidSpec, len(fields))
	}
	e := &cronExpr{}
	defs := []struct {
		name string
		min  int
		max  int
		text string
		set  *bitset
		rest *bool // nil: the restricted flag is unused for this field
	}{
		{"minute", 0, 59, fields[0], &e.minute, nil},
		{"hour", 0, 23, fields[1], &e.hour, nil},
		{"day", 1, 31, fields[2], &e.day, &e.dayRestricted},
		{"month", 1, 12, fields[3], &e.month, nil},
		{"weekday", 0, 6, fields[4], &e.weekday, &e.weekdayRestricted},
	}
	for _, d := range defs {
		f := cronField{name: d.name, min: d.min, max: d.max}
		set, restricted, err := f.parse(d.text)
		if err != nil {
			return nil, fmt.Errorf("%w: cron: %v", ErrInvalidSpec, err)
		}
		*d.set = set
		if d.rest != nil {
			*d.rest = restricted
		}
	}
	if e.neverMatches() {
		return nil, fmt.Errorf("%w: cron: %q can never occur on any calendar date", ErrInvalidSpec, spec)
	}
	return e, nil
}

// neverMatches reports whether the expression can never occur on a real
// calendar date. With day/month constraints and day-of-week OR semantics, this
// is only possible when the day-of-week field is unrestricted and no (day,
// month) pair in the expression is a real calendar date: a restricted
// day-of-week always matches at least weekly and a day-1..31 with an
// unrestricted month always matches monthly, so neither can be the sole
// blocker. The leap day is handled explicitly.
func (e *cronExpr) neverMatches() bool {
	if !e.dayRestricted || e.weekdayRestricted {
		return false
	}
	for m := 1; m <= 12; m++ {
		if !e.month.has(m) {
			continue
		}
		dim := 31
		switch m {
		case 4, 6, 9, 11:
			dim = 30
		case 2:
			dim = 28
		}
		for d := 1; d <= dim; d++ {
			if e.day.has(d) {
				return false
			}
		}
	}
	return !(e.month.has(2) && e.day.has(29))
}

// matches reports whether t satisfies the expression, applying standard cron
// OR semantics to restricted day-of-month and day-of-week fields.
func (e *cronExpr) matches(t time.Time) bool {
	if !e.month.has(int(t.Month())) {
		return false
	}
	if !e.minute.has(t.Minute()) {
		return false
	}
	if !e.hour.has(t.Hour()) {
		return false
	}
	domOK := e.day.has(t.Day())
	dowOK := e.weekday.has(int(t.Weekday()))
	switch {
	case e.dayRestricted && e.weekdayRestricted:
		return domOK || dowOK
	case e.dayRestricted:
		return domOK
	case e.weekdayRestricted:
		return dowOK
	default:
		return true
	}
}

// next returns the first occurrence strictly after after that matches the
// expression, scanning minute by minute. The scan is bounded by
// cronScanHorizon so an expression can never loop forever.
func (e *cronExpr) next(after time.Time) (time.Time, error) {
	cur := after.Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < cronScanHorizon; i++ {
		if e.matches(cur) {
			return cur, nil
		}
		cur = cur.Add(time.Minute)
	}
	return time.Time{}, fmt.Errorf("cron: no next occurrence within %d minutes", cronScanHorizon)
}
