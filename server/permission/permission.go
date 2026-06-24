// Package permission centralizes the task authorization rules.
//
// The model treats the creator and the assignee as co-owners who can modify,
// change status, reassign, add subtasks/reminders and comment. Only the
// creator may delete. Personal tasks (no ChannelID) are visible only to
// creator and assignee; channel-scoped tasks are additionally visible (and
// commentable) to any member of the home channel or a card channel. There is
// no "channel admin" actor for tasks — Mattermost channel admins are treated
// like ordinary channel members.
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

// CanUserManageTask reports whether userID may perform write actions on a task
// other than delete: modify fields, change status, assign/reassign, set/clear
// reminder, and add subtasks. Both the creator and the current assignee count
// as co-owners for these actions.
func CanUserManageTask(userID string, task *model.Task) bool {
	if userID == "" || task == nil {
		return false
	}
	return userID == task.CreatorID || userID == task.AssigneeID
}

// CanUserDeleteTask reports whether userID may hard-delete the task. Only the
// creator may delete; the assignee and any channel member (including channel
// admins) may not. This keeps final authority with the task's owner.
func CanUserDeleteTask(userID string, task *model.Task) bool {
	if userID == "" || task == nil {
		return false
	}
	return userID == task.CreatorID
}

// CanUserViewTask reports whether userID may view the task. The creator and
// assignee can always view. For channel-scoped tasks every channel member may
// view; a member of ANY channel that holds one of the task's card posts
// (channel card, DM card, OR a shared card in another channel) may also view —
// consistent with "if you can read the card thread, you can see the task".
// cardChannelIDs is the set of channel ids holding the task's card posts,
// resolved by the caller (REST/command layer) from task_posts; the union of
// task.ChannelID + cardChannelIDs is checked for membership. This keeps the
// package free of pluginapi (the caller does the post→channel resolution).
func CanUserViewTask(userID string, task *model.Task, cardChannelIDs []string, channels ChannelMembershipChecker) bool {
	if userID == "" || task == nil {
		return false
	}
	if userID == task.CreatorID || userID == task.AssigneeID {
		return true
	}
	// Personal tasks are private to creator + assignee.
	if task.ChannelID == "" && len(cardChannelIDs) == 0 {
		return false
	}
	if channels == nil {
		return false
	}
	// Channel-scoped tasks are visible to any member of the home channel OR any
	// channel holding a card post (channel card / DM card / shared card).
	if task.ChannelID != "" && channels.IsChannelMember(userID, task.ChannelID) {
		return true
	}
	for _, cid := range cardChannelIDs {
		if cid != "" && channels.IsChannelMember(userID, cid) {
			return true
		}
	}
	return false
}

// CanUserListTask reports whether userID may list/read a task and its subtasks,
// comments, and events. It follows the view rule: anyone who can view the task
// may read its details.
func CanUserListTask(userID string, task *model.Task, cardChannelIDs []string, channels ChannelMembershipChecker) bool {
	return CanUserViewTask(userID, task, cardChannelIDs, channels)
}

// CanUserCommentTask reports whether userID may comment on the task. Commenting
// follows the view rule: anyone who can view the task may comment on it.
func CanUserCommentTask(userID string, task *model.Task, cardChannelIDs []string, channels ChannelMembershipChecker) bool {
	return CanUserViewTask(userID, task, cardChannelIDs, channels)
}
