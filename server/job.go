package main

import (
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/notification"
)

// reminderGracePeriod is the window after the due time during which a reminder
// can still fire. Beyond it the reminder is treated as missed and dropped
// (fires at most once), so the scheduler doesn't spam a backlog after downtime.
const reminderGracePeriod = 5 * time.Minute

// runReminderJob is the cluster-scheduled entry point that fires due task
// reminders. It is invoked once per minute on a single cluster node (via
// cluster.Schedule in plugin.go) and only scans the small idx:reminder: index
// rather than every task, so cost scales with the number of pending reminders.
func (p *Plugin) runReminderJob() {
	if p.taskService == nil {
		return
	}
	now := time.Now().UnixMilli()
	due, err := p.taskService.FireReadyReminders(now, reminderGracePeriod)
	if err != nil {
		p.API.LogError("Reminder job failed to scan index", "error", err)
		return
	}

	for _, r := range due {
		if err := p.fireReminderDM(r); err != nil {
			p.API.LogError("Reminder job failed to fire DM",
				"task_id", r.TaskID, "assignee_id", r.AssigneeID, "error", err)
			continue
		}
		// Mark fired only after a successful DM send so a transient failure
		// retries on the next tick (within the grace window). Takes the
		// reminder id (SQL reminders have their own id) plus the task id for
		// the audit-event append.
		if err := p.taskService.MarkReminderFired(r.ReminderID, r.TaskID); err != nil {
			p.API.LogError("Reminder job failed to mark fired",
				"task_id", r.TaskID, "error", err)
		}

		// Real-time: the reminder fired flag changed; clients can surface the
		// "due soon" indicator (#32). Best-effort: a missing task is skipped.
		if t, _ := p.taskService.Get(r.TaskID); t != nil {
			p.broadcastTaskUpdated(t, []string{"reminder_fired"})
		}
	}
}

// runOverdueJob is the daily-scheduled entry point that notifies the creator and
// assignee of past-due, non-terminal tasks. It runs once per UTC day at most per
// task: a task whose last_overdue_sent_at already falls within the current UTC
// day is skipped, so a scheduler restart mid-day never double-DMs. Like the
// reminder job it is best-effort — a delivery failure is logged and retried
// naturally the next day (change notification-overdue-and-context, design D8).
// gmt7 is the Asia/Bangkok timezone (UTC+7) used by the scheduled notify job.
// Falls back to a fixed zone if the host lacks tzdata (some minimal images).
var gmt7 = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		return time.FixedZone("ICT", 7*3600)
	}
	return loc
}()

// nextRunAt8hGMT7 returns the next 08:00 Asia/Bangkok instant strictly after
// now. If now is before 08:00 today, that's today; otherwise it's tomorrow.
// The scheduled notify job (overdue + due-soon) fires once at this time each
// day (change due-color-and-scheduled-notify, design D5).
func nextRunAt8hGMT7(now time.Time) time.Time {
	g := now.In(gmt7)
	next := time.Date(g.Year(), g.Month(), g.Day(), 8, 0, 0, 0, gmt7)
	if !next.After(g) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// runScheduledNotifyJob fires the daily overdue + due-soon notifications at
// 08:00 GMT+7. It scans both sets in one run and dedupes each per GMT+7 day via
// the last_*_sent_at columns (change due-color-and-scheduled-notify, design D5/D6).
func (p *Plugin) runScheduledNotifyJob() {
	if p.taskService == nil || p.notifier == nil {
		return
	}
	now := time.Now()
	nowMs := now.UnixMilli()
	// Start-of-day in GMT+7: dedupe boundary so a task notified today (in GMT+7
	// terms) is skipped even after a mid-day plugin restart.
	g := now.In(gmt7)
	startOfTodayGMT7 := time.Date(g.Year(), g.Month(), g.Day(), 0, 0, 0, 0, gmt7).UnixMilli()
	const day = 24 * 60 * 60 * 1000 // ms

	// 1) Overdue: due_at < now, non-terminal → DM creator + assignee.
	overdue, err := p.taskService.ListOverdueTasks(nowMs)
	if err != nil {
		p.API.LogError("Scheduled notify: overdue scan failed", "error", err)
	} else {
		for _, t := range overdue {
			if t.LastOverdueSentAt != nil && *t.LastOverdueSentAt >= startOfTodayGMT7 {
				continue
			}
			p.notifier.NotifyOverdue(notification.TaskSummary{
				ID: t.ID, Summary: t.Summary, Status: t.Status,
				DueAt: t.DueAt, IsAllDay: t.IsAllDay,
			}, nowMs, t.CreatorID, t.AssigneeID)
			if err := p.taskService.MarkOverdueSent(t.ID); err != nil {
				p.API.LogError("Scheduled notify: mark overdue sent failed",
					"task_id", t.ID, "error", err)
			}
		}
	}

	// 2) Due-soon: now <= due_at < now+24h, non-terminal → DM assignee only.
	dueSoon, err := p.taskService.ListDueSoonTasks(nowMs, nowMs+day)
	if err != nil {
		p.API.LogError("Scheduled notify: due-soon scan failed", "error", err)
	} else {
		for _, t := range dueSoon {
			if t.LastDueSoonSentAt != nil && *t.LastDueSoonSentAt >= startOfTodayGMT7 {
				continue
			}
			p.notifier.NotifyDueSoon(t.AssigneeID, notification.TaskSummary{
				ID: t.ID, Summary: t.Summary, Status: t.Status,
				DueAt: t.DueAt, IsAllDay: t.IsAllDay,
			})
			if err := p.taskService.MarkDueSoonSent(t.ID); err != nil {
				p.API.LogError("Scheduled notify: mark due-soon sent failed",
					"task_id", t.ID, "error", err)
			}
		}
	}
}

// runOverdueJob is kept as a thin alias for backward compatibility with any
// caller wiring that still references the old name during the rollout; the real
// work moved to runScheduledNotifyJob.
func (p *Plugin) runOverdueJob() { p.runScheduledNotifyJob() }

// fireReminderDM sends the reminder notification DM to the assignee and returns
// an error when delivery failed (so the caller does NOT mark the reminder fired
// and retries on the next tick). Prefers the localized notifier; falls back to a
// plain DM when the notifier isn't initialized (e.g. activation races).
func (p *Plugin) fireReminderDM(r taskmodel.DueReminder) error {
	if p.notifier != nil {
		t, _ := p.taskService.Get(r.TaskID)
		summary := notification.TaskSummary{ID: r.TaskID}
		if t != nil {
			summary.Summary = t.Summary
			summary.Status = t.Status
			summary.DueAt = t.DueAt
			summary.IsAllDay = t.IsAllDay
		}
		// Propagate the delivery error so the caller can retry instead of
		// marking the reminder fired (which would silently lose it).
		return p.notifier.NotifyReminder(r.AssigneeID, summary)
	}
	// Fallback: plain DM when the notifier isn't ready.
	channel, err := p.API.GetDirectChannel(r.AssigneeID, p.botUserID)
	if err != nil || channel == nil {
		return errors.Wrapf(err, "failed to open DM with %s", r.AssigneeID)
	}
	post := &model.Post{
		UserId:    p.botUserID,
		ChannelId: channel.Id,
		Message:   "🔔 You have a task due soon.",
		Type:      model.PostTypeDefault,
	}
	if _, err := p.API.CreatePost(post); err != nil {
		return errors.Wrap(err, "failed to create reminder DM")
	}
	return nil
}
