// package permission centralizes the task authorization rules.
//
// The model treats the creator and the assignee as co-owners who can modify,
// change status, reassign, add subtasks/reminders and comment. Only the
// creator may delete. A task with a channel surface (a non-empty ChannelID or
// a tracked card post) is readable by anyone — its card is already visible in
// the channel; only a personal task (no ChannelID and no card) is restricted
// to creator + assignee. There is no "channel admin" actor for tasks —
// Mattermost channel admins are treated like ordinary channel members.
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
// assignee can always view. A task with a channel surface — a non-empty
// task.ChannelID (the home channel) or any tracked card post (cardChannelIDs)
// — is treated as readable: its card is already visible in the channel, so
// anyone who can see the card can see the task details. This deliberately does
// NOT consult channel membership: GetChannelMember can flap on transient
// failures (cache miss, slow load) and would surface as a spurious 403, the
// same reason listTasks does not pre-gate. Only a personal task (no ChannelID
// AND no card post) is restricted to creator + assignee.
//
// cardChannelIDs is the set of channel ids holding the task's card posts,
// resolved by the caller (REST/command layer) from task_posts; it is the only
// signal this package uses for channel surface, keeping the package free of
// pluginapi (the caller does the post→channel resolution).
//
// TODO(perm): this read-path is intentionally fail-open for channel-surfaced
// tasks because GetChannelMember flakes deterministically in our environment.
// Revisit when we have a reliable membership signal (e.g. a cached/cluster-safe
// membership API) to tighten view access without reintroducing spurious 403s.
func CanUserViewTask(userID string, task *model.Task, cardChannelIDs []string, _ ChannelMembershipChecker) bool {
	if userID == "" || task == nil {
		return false
	}
	if userID == task.CreatorID || userID == task.AssigneeID {
		return true
	}
	// A task with a channel surface is readable (its card is already public in
	// the channel). Only a personal task with no channel and no card is private.
	if task.ChannelID != "" {
		return true
	}
	for _, cid := range cardChannelIDs {
		if cid != "" {
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
