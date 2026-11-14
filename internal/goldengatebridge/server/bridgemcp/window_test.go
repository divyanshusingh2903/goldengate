package bridgemcp

import (
	"strings"
	"testing"
	"time"
)

func TestResolveWindow(t *testing.T) {
	now := time.Date(2026, 11, 14, 15, 30, 0, 0, time.UTC)
	day := 24 * time.Hour
	date := func(s string) time.Time {
		d, err := time.Parse(time.DateOnly, s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	cases := []struct {
		name              string
		days              int
		since, until      string
		wantSince         time.Time
		wantUntil         time.Time
		wantStoreUntil    bool
		wantLabel         string
		wantErrContaining string
	}{
		{name: "default is the last 5 days", wantSince: now.Add(-5 * day), wantUntil: now, wantLabel: "the last 5 days"},
		{name: "days widens the range", days: 30, wantSince: now.Add(-30 * day), wantUntil: now, wantLabel: "the last 30 days"},
		{name: "one day reads naturally", days: 1, wantSince: now.Add(-day), wantUntil: now, wantLabel: "the last day"},
		{name: "days is capped at 365", days: 4000, wantSince: now.Add(-365 * day), wantUntil: now, wantLabel: "the last 365 days"},
		{name: "since alone runs to now", since: "2026-11-01", wantSince: date("2026-11-01"), wantUntil: now, wantLabel: "2026-11-01 to 2026-11-14"},
		{name: "since takes precedence over days", days: 2, since: "2026-11-01", wantSince: date("2026-11-01"), wantUntil: now, wantLabel: "2026-11-01 to 2026-11-14"},
		{name: "a bare until date includes that whole day", since: "2026-10-01", until: "2026-10-31",
			wantSince: date("2026-10-01"), wantUntil: date("2026-11-01"), wantStoreUntil: true, wantLabel: "2026-10-01 to 2026-10-31"},
		{name: "until alone counts days back from it", until: "2026-10-31",
			wantSince: date("2026-11-01").Add(-5 * day), wantUntil: date("2026-11-01"), wantStoreUntil: true, wantLabel: "2026-10-27 to 2026-10-31"},
		{name: "until with days", days: 10, until: "2026-10-31",
			wantSince: date("2026-11-01").Add(-10 * day), wantUntil: date("2026-11-01"), wantStoreUntil: true, wantLabel: "2026-10-22 to 2026-10-31"},
		{name: "RFC 3339 bounds are exact", since: "2026-11-10T08:00:00Z", until: "2026-11-12T20:00:00Z",
			wantSince: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC), wantUntil: time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC), wantStoreUntil: true, wantLabel: "2026-11-10 to 2026-11-12"},
		{name: "negative days", days: -3, wantErrContaining: "days must be positive"},
		{name: "unparseable since", since: "last week", wantErrContaining: "since \"last week\" is not a date"},
		{name: "unparseable until", until: "11/30/2026", wantErrContaining: "until \"11/30/2026\" is not a date"},
		{name: "since after until", since: "2026-11-10", until: "2026-11-01", wantErrContaining: "since must be before until"},
		{name: "since in the future", since: "2026-12-01", wantErrContaining: "since must be before until"},
		{name: "range longer than a year", since: "2025-01-01", until: "2026-11-01", wantErrContaining: "more than 365 days"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, err := resolveWindow(now, c.days, c.since, c.until)
			if c.wantErrContaining != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrContaining) {
					t.Fatalf("want error containing %q, got %v", c.wantErrContaining, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !w.Since.Equal(c.wantSince) || !w.Until.Equal(c.wantUntil) {
				t.Errorf("range = %v .. %v, want %v .. %v", w.Since, w.Until, c.wantSince, c.wantUntil)
			}
			if got := !w.storeUntil().IsZero(); got != c.wantStoreUntil {
				t.Errorf("store upper bound sent = %v, want %v", got, c.wantStoreUntil)
			}
			if w.label != c.wantLabel {
				t.Errorf("label = %q, want %q", w.label, c.wantLabel)
			}
		})
	}
}
