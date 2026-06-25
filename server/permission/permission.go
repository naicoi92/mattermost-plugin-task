// package permission centralizes the task authorization rules.
//
// The model treats the creator and the assignee as co-owners who can modify,
// change status, reassign, add subtasks/reminders and comment. Only the
// creator may delete. Read access (view/list/comment) is granted to creator,
// assignee, or any member of task.ChannelID (checked via ChannelMembershipChecker).
// There is no "channel admin" actor for tasks — Mattermost channel admins are
// treated like ordinary channel members.
//
// Every command, REST handler and card-action handler must go through these
// helpers rather than scattering ad-hoc membership checks, so the rules stay
// consistent and auditable in one place.
package permission

import "github.com/naicoi92/mattermost-plugin-task/server/model"

// ChannelMembershipChecker reports channel-level access for a user. It exposes
// plain membership, used to decide viewing/commenting on a channel task. Admin
// status is intentionally NOT part of the task permission model.
//
// It is supplied by the caller (REST/command layer), which has access to the
// Mattermost channel membership API, keeping this package free of pluginapi
// dependencies and therefore unit-testable in isolation.
type ChannelMembershipChecker interface {
	// IsChannelMember reports whether userID is any member (admin or not) of
	// channelID. Used by the view/comment/list rules.
	IsChannelMember(userID, channelID string) bool
}

// IsCreator reports whether userID is the creator of the task. One of the
// three primitives every action rule composes from.
func IsCreator(userID string, task *model.Task) bool {
	return task != nil && userID != "" && userID == task.CreatorID
}

// IsAssignee reports whether userID is the assignee of the task. One of the
// three primitives every action rule composes from.
func IsAssignee(userID string, task *model.Task) bool {
	return task != nil && userID != "" && userID == task.AssigneeID
}

// CanUserManageTask reports whether userID may perform write actions on a task
// other than delete: modify fields, change status, assign/reassign, set/clear
// reminder, and add subtasks. Both the creator and the current assignee count
// as co-owners for these actions.
func CanUserManageTask(userID string, task *model.Task) bool {
	return IsCreator(userID, task) || IsAssignee(userID, task)
}

// CanUserDeleteTask reports whether userID may hard-delete the task. Only the
// creator may delete; the assignee and any channel member (including channel
// admins) may not. This keeps final authority with the task's owner.
func CanUserDeleteTask(userID string, task *model.Task) bool {
	return IsCreator(userID, task)
}

// CanUserViewTask reports whether userID may view the task. Access is granted
// to creator, assignee, or any member of task.ChannelID (via the checker).
// One rule for every channel type (team O/P/G, DM D, self-DM).
func CanUserViewTask(userID string, task *model.Task, checker ChannelMembershipChecker) bool {
	if userID == "" || task == nil {
		return false
	}
	if IsCreator(userID, task) || IsAssignee(userID, task) {
		return true
	}
	if task.ChannelID != "" && checker != nil {
		return checker.IsChannelMember(userID, task.ChannelID)
	}
	return false
}

// CanUserListTask reports whether userID may list/read a task and its subtasks,
// comments, and events. It follows the view rule: anyone who can view the task
// may read its details.
func CanUserListTask(userID string, task *model.Task, checker ChannelMembershipChecker) bool {
	return CanUserViewTask(userID, task, checker)
}

// CanUserCommentTask reports whether userID may comment on the task. Commenting
// follows the view rule: anyone who can view the task may comment on it.
func CanUserCommentTask(userID string, task *model.Task, checker ChannelMembershipChecker) bool {
	return CanUserViewTask(userID, task, checker)
}
