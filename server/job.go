package main

import (
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"

	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// runJob is the generic starter-template background job placeholder, kept
// around for parity with the template's cluster.Schedule example. The real
// scheduled work is runReminderJob.
func (p *Plugin) runJob() {
	p.API.LogInfo("Job is currently running")
}

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
		// retries on the next tick (within the grace window).
		if err := p.taskService.MarkReminderFired(r.TaskID); err != nil {
			p.API.LogError("Reminder job failed to mark fired",
				"task_id", r.TaskID, "error", err)
		}
	}
}

// fireReminderDM posts a reminder direct message to the assignee from the bot.
// The full notification layer (i18n, rich card) is built in #14; this keeps the
// reminder pipeline working end-to-end in the meantime.
func (p *Plugin) fireReminderDM(r task.DueReminder) error {
	channel, err := p.API.GetDirectChannel(r.AssigneeID, p.botUserID)
	if err != nil {
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
