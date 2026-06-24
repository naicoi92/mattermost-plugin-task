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

// ChannelMembershipChecker reports channel-level access for a user. It separates
// the two distinct concepts the permission rules need: plain membership (used to
// decide viewing/commenting on a channel task) and admin status (used to decide
// who may delete a channel task). Keeping these as distinct methods avoids the
// ambiguity of overloading a single "IsChannelAdmin" check to mean both.
//
// It is supplied by the caller (REST/command layer), which has access to the
// Mattermost channel membership API, keeping this package free of pluginapi
// dependencies and therefore unit-testable in isolation.
type ChannelMembershipChecker interface {
	// IsChannelMember reports whether userID is any member (admin or not) of
	// channelID. Used by the view/comment rules.
	IsChannelMember(userID, channelID string) bool
	// IsChannelAdmin reports whether userID is a channel admin of channelID.
	// Used by the delete rule for channel-scoped tasks.
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
func CanUserDeleteTask(userID string, task *model.Task, channels ChannelMembershipChecker) bool {
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

// CanUserCommentTask reports whether userID may comment on the task. Commenting
// follows the view rule: anyone who can view the task may comment on it.
func CanUserCommentTask(userID string, task *model.Task, cardChannelIDs []string, channels ChannelMembershipChecker) bool {
	return CanUserViewTask(userID, task, cardChannelIDs, channels)
}
