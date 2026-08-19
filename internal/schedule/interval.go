package schedule

import (
	"fmt"
	"time"
)

// parseIntervalSpec parses an interval trigger spec: any time.ParseDuration
// format with a strictly positive value ("30m", "1h30m", "24h", …). Zero and
// negative intervals are rejected because they cannot advance NextFire.
func parseIntervalSpec(spec string) (time.Duration, error) {
	d, err := time.ParseDuration(spec)
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", ErrInvalidSpec, spec, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%w: %s: interval must be positive", ErrInvalidSpec, spec)
	}
	return d, nil
}

// nextInterval returns the next fire time after from for an interval d.
func nextInterval(from time.Time, d time.Duration) time.Time {
	return from.Add(d)
}
