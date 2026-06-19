// Package permission centralizes the co-owner authorization rules for tasks.
//
// The model (see PLAN.md section 5.4) treats the assignee as a co-owner who can
// modify, change status, reassign, add subtasks/reminders and comment. Only the
// creator (or a channel admin, for channel-scoped tasks) may delete. Personal
// tasks (no ChannelID) are visible only to creator and assignee.
//
// Every command, REST handler and dialog handler must go through these helpers
// rather than scattering ad-hoc membership checks, so the rules stay consistent
// and auditable in one place.
package permission

import "github.com/naicoi92/mattermost-plugin-task/server/model"

// ChannelAdminChecker reports whether userID is a channel admin of channelID.
// It is supplied by the caller (REST/command layer), which has access to the
// Mattermost channel membership API, keeping this package free of pluginapi
// dependencies and therefore unit-testable in isolation.
type ChannelAdminChecker interface {
	IsChannelAdmin(userID, channelID string) bool
}

// CanUserModifyTask reports whether userID may edit a task's mutable fields
// (summary/description/due/status/assignee/subtask/reminder). Both the creator
// and the current assignee count as co-owners for these actions.
func CanUserModifyTask(userID string, task *model.Task) bool {
	if userID == "" || task == nil {
		return false
	}
	return userID == task.CreatorID || userID == task.AssigneeID
}

// CanUserDeleteTask reports whether userID may hard-delete the task. Only the
// creator may always delete; for channel-scoped tasks a channel admin may also
// delete. The assignee may NOT delete (avoids total loss of control).
func CanUserDeleteTask(userID string, task *model.Task, channels ChannelAdminChecker) bool {
	if userID == "" || task == nil {
		return false
	}
	if userID == task.CreatorID {
		return true
	}
	if task.ChannelID != "" && channels != nil {
		return channels.IsChannelAdmin(userID, task.ChannelID)
	}
	return false
}

// CanUserViewTask reports whether userID may view the task. The creator and
// assignee can always view. For channel-scoped tasks every channel member may
// view; for personal tasks (ChannelID == "") nobody else may view.
func CanUserViewTask(userID string, task *model.Task, channels ChannelAdminChecker) bool {
	if userID == "" || task == nil {
		return false
	}
	if userID == task.CreatorID || userID == task.AssigneeID {
		return true
	}
	// Personal tasks are private to creator + assignee.
	if task.ChannelID == "" {
		return false
	}
	// Channel-scoped tasks are visible to any channel member. We treat channel
	// membership uniformly via the checker (which returns true for any member,
	// admin or not); admins are a subset of members.
	return channels != nil && channels.IsChannelAdmin(userID, task.ChannelID)
}

// CanUserCommentTask reports whether userID may comment on the task. Commenting
// follows the view rule: anyone who can view the task may comment on it.
func CanUserCommentTask(userID string, task *model.Task, channels ChannelAdminChecker) bool {
	return CanUserViewTask(userID, task, channels)
}
