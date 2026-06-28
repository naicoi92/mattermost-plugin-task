package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestNextRunAt8hGMT7 pins the scheduled-notify fire time (08:00 Asia/Bangkok).
// The job sleeps until this instant each loop, so the boundary must be exact.
func TestNextRunAt8hGMT7(t *testing.T) {
	cases := []struct {
		name string
		now  string // RFC3339 in GMT+7 for readability
		want string // next 08:00 GMT+7, RFC3339
	}{
		{
			name: "before 8h today → 8h today",
			now:  "2026-06-27T07:00:00+07:00",
			want: "2026-06-27T08:00:00+07:00",
		},
		{
			name: "exactly 8h → tomorrow 8h (strictly after)",
			now:  "2026-06-27T08:00:00+07:00",
			want: "2026-06-28T08:00:00+07:00",
		},
		{
			name: "after 8h today → tomorrow 8h",
			now:  "2026-06-27T10:00:00+07:00",
			want: "2026-06-28T08:00:00+07:00",
		},
		{
			name: "late night → tomorrow 8h",
			now:  "2026-06-27T23:30:00+07:00",
			want: "2026-06-28T08:00:00+07:00",
		},
		{
			name: "UTC midnight = 7h GMT+7 → 8h today GMT+7",
			now:  "2026-06-27T00:00:00+00:00",
			want: "2026-06-27T08:00:00+07:00",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, c.now)
			assert.NoError(t, err)
			want, err := time.Parse(time.RFC3339, c.want)
			assert.NoError(t, err)
			got := nextRunAt8hGMT7(now)
			assert.True(t, got.Equal(want),
				"nextRunAt8hGMT7(%s) = %s, want %s", c.now, got.Format(time.RFC3339), c.want)
		})
	}
}

// TestGmt7ZoneIsBangkok asserts the location loaded is UTC+7 (either the real
// Asia/Bangkok zone or the FixedZone fallback on hosts without tzdata).
func TestGmt7ZoneIsBangkok(t *testing.T) {
	_, offset := time.Date(2026, 6, 27, 12, 0, 0, 0, gmt7).Zone()
	assert.Equal(t, 7*3600, offset, "gmt7 must be UTC+7")
}
