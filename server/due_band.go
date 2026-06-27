// dueBand and its thresholds are shared between the Go server (card color +
// notification emoji prefix) and the webapp TS helper
// (webapp/src/components/shared/due_band.ts). Both implementations MUST return
// the same band for the same input — keep thresholds in sync (change
// due-color-and-scheduled-notify, design D1).

package main

import (
	"time"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

// Band thresholds in hours. A task is:
//   - muted   when dueAt is more than warningHours (72h) away, OR terminal, OR has no due
//   - warning when dueAt is between dangerHours (24h) and warningHours (72h) away
//   - danger  when dueAt is less than dangerHours (24h) away (includes overdue: delta < 0)
const (
	dangerHours  = 24
	warningHours = 72
	msPerHour    = int64(time.Hour) / int64(time.Millisecond)
	dangerMs     = dangerHours * msPerHour
	warningMs    = warningHours * msPerHour
)

// Band identifies a due-date proximity bucket. It is the contract every due
// surface (card color, webapp CSS class, DM emoji prefix) keys off.
type Band string

const (
	BandMuted  Band = "muted"
	BandWarn   Band = "warning"
	BandDanger Band = "danger"
)

// dueBand returns the proximity band for a task's due date. dueMs == 0 means
// "no due date" → muted. Terminal status (done/cancelled) → muted regardless of
// due. delta = dueAt - now; danger when delta < dangerMs, warning when delta <=
// warningMs, otherwise muted. Overdue (delta < 0) falls under danger.
func dueBand(dueMs, nowMs int64, status string) Band {
	if dueMs == 0 {
		return BandMuted
	}
	if status == taskmodel.StatusDone || status == taskmodel.StatusCancelled {
		return BandMuted
	}
	delta := dueMs - nowMs
	switch {
	case delta < dangerMs:
		return BandDanger
	case delta <= warningMs:
		return BandWarn
	default:
		return BandMuted
	}
}

// bandHex maps a band to the hex color used by the channel-card attachment
// (host-side render, no dark-mode vars). Empty for muted — caller falls back to
// status color.
var bandHex = map[Band]string{
	BandDanger: "#e01e5a", // --task-danger
	BandWarn:   "#cf8900", // --task-warning
}
