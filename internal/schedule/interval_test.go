package schedule

import (
	"testing"
	"time"
)

func TestParseIntervalSpecValid(t *testing.T) {
	cases := []struct {
		spec string
		want time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"24h", 24 * time.Hour},
		{"90s", 90 * time.Second},
		{"1m30s", 90 * time.Second},
		{"2h", 2 * time.Hour},
	}
	for _, c := range cases {
		got, err := parseIntervalSpec(c.spec)
		if err != nil {
			t.Errorf("parseIntervalSpec(%q): unexpected error: %v", c.spec, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseIntervalSpec(%q) = %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestParseIntervalSpecRejectsNonPositive(t *testing.T) {
	// Zero and negative intervals cannot advance NextFire and must be rejected.
	for _, spec := range []string{"", "abc", "0s", "0m", "-5m", "0h", "1.5x"} {
		if d, err := parseIntervalSpec(spec); err == nil {
			t.Errorf("parseIntervalSpec(%q) = %v, want error", spec, d)
		}
	}
}

func TestNextInterval(t *testing.T) {
	base := time.Date(2026, time.August, 19, 9, 0, 0, 0, time.UTC)
	if got := nextInterval(base, 30*time.Minute); !got.Equal(base.Add(30 * time.Minute)) {
		t.Errorf("nextInterval = %v, want %v", got, base.Add(30*time.Minute))
	}
	if got := nextInterval(base, 24*time.Hour); !got.Equal(base.Add(24 * time.Hour)) {
		t.Errorf("nextInterval = %v, want %v", got, base.Add(24*time.Hour))
	}
}
