package main

import (
	"slices"

	mmmodel "github.com/mattermost/mattermost/server/public/model"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

// WebSocket event name. The webapp registers a handler for "task_updated"
// (index.tsx); Mattermost prepends "custom_<pluginid>_" before delivery, so the
// webapp handler key is the bare name (registerWebSocketEventHandler strips the
// prefix). See PLAN.md §Phụ lục B for the payload schema.
const wsEventTaskUpdated = "task_updated"

// broadcastTaskUpdated publishes a "task_updated" event so the desktop RHS,
// Task Detail, and Kanban reflect server changes in real time (#32).
//
// Broadcast scope (PLAN §Phụ lục B):
//   - Channel tasks (ChannelID != ""): broadcast to channel members.
//   - Personal tasks: broadcast to the creator and the assignee only.
//
// `task` may be nil for a delete: the payload then carries only task_id +
// changed_fields, and the webapp deletes the task from its cache. For a delete
// the scope still derives from the task's pre-delete fields (creator/assignee/
// channel), so the right clients learn about it; the caller passes the snapshot.
//
// `changedFields` lists which fields changed (e.g. "status", "assignee_id",
// "due", "comment"); clients use it to decide whether to refetch. It is
// informational, not authoritative — the full task (when present) is the source
// of truth.
func (p *Plugin) broadcastTaskUpdated(task *taskmodel.Task, changedFields []string) {
	if task == nil {
		return
	}

	payload := map[string]any{
		"task_id":        task.ID,
		"seq":            task.UpdatedAt,
		"updated_at":     task.UpdatedAt,
		"changed_fields": changedFields,
		"status":         task.Status,
	}

	// Include the full task object so clients can update without an extra
	// fetch. Nil-marshals to JSON null; the webapp treats a null/absent task as
	// a delete signal (it also keys off a missing task body, see index.tsx).
	payload["task"] = task

	p.publishTaskEvent(payload, task)
}

// broadcastTaskDeleted publishes a "task_updated" event signaling a task removal.
// The payload omits the task body, which the webapp interprets as a delete.
func (p *Plugin) broadcastTaskDeleted(task *taskmodel.Task) {
	if task == nil {
		return
	}
	payload := map[string]any{
		"task_id":        task.ID,
		"seq":            task.UpdatedAt,
		"updated_at":     task.UpdatedAt,
		"changed_fields": []string{"deleted"},
		"task":           nil,
	}
	p.publishTaskEvent(payload, task)
}

// publishTaskEvent fans the payload out to the correct recipients based on the
// task's scope. Personal tasks go to creator + assignee (deduplicated); channel
// tasks go to all channel members via a single channel broadcast.
func (p *Plugin) publishTaskEvent(payload map[string]any, task *taskmodel.Task) {
	if task.ChannelID != "" {
		// Channel-scoped task: one broadcast reaches every member of the channel.
		p.API.PublishWebSocketEvent(wsEventTaskUpdated, payload, &mmmodel.WebsocketBroadcast{
			ChannelId: task.ChannelID,
		})
		return
	}

	// Personal task: notify creator and assignee only. Dedupe so a creator who is
	// also the assignee gets a single event.
	recipients := make([]string, 0, 2)
	for _, uid := range []string{task.CreatorID, task.AssigneeID} {
		if uid != "" && !slices.Contains(recipients, uid) {
			recipients = append(recipients, uid)
		}
	}
	for _, uid := range recipients {
		p.API.PublishWebSocketEvent(wsEventTaskUpdated, payload, &mmmodel.WebsocketBroadcast{
			UserId: uid,
		})
	}
}
