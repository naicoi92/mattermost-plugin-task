package permission

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// fakeMembershipChecker is a ChannelMembershipChecker backed by a single
// allowlist of channel members (view/comment/list rule). There is no separate
// admin concept in the task permission model anymore.
type fakeMembershipChecker struct {
	members map[string]bool // "userID:channelID" -> member
}

func (f fakeMembershipChecker) IsChannelMember(userID, channelID string) bool {
	return f.members[userID+":"+channelID]
}

func taskFixture(creator, assignee, channel string) *model.Task {
	return &model.Task{
		CreatorID:  creator,
		AssigneeID: assignee,
		TaskRow:    model.TaskRow{ChannelID: channel},
	}
}

// Matrix-driven coverage of the three actors (creator, assignee, channel
// member) against every action family. Channel admins are intentionally NOT a
// distinct actor here — they are tested as ordinary channel members.
//
// View: a channel-surfaced task is readable by ANYONE (its card is already
// public in the channel); view does NOT gate on channel membership. Only a
// personal task restricts view to creator + assignee.
func TestPermissionMatrix_ChannelTask(t *testing.T) {
	// Channel-scoped task; "member" is a plain member of the home channel.
	ch := fakeMembershipChecker{members: map[string]bool{"member:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	matrix := []struct {
		name  string
		allow bool
		check func(userID string) bool
	}{
		{"creator view", true, func(u string) bool { return CanUserViewTask(u, task, nil, ch) }},
		{"creator list", true, func(u string) bool { return CanUserListTask(u, task, nil, ch) }},
		{"creator comment", true, func(u string) bool { return CanUserCommentTask(u, task, nil, ch) }},
		{"creator manage", true, func(u string) bool { return CanUserManageTask(u, task) }},
		{"creator delete", true, func(u string) bool { return CanUserDeleteTask(u, task) }},

		{"assignee view", true, func(u string) bool { return CanUserViewTask(u, task, nil, ch) }},
		{"assignee list", true, func(u string) bool { return CanUserListTask(u, task, nil, ch) }},
		{"assignee comment", true, func(u string) bool { return CanUserCommentTask(u, task, nil, ch) }},
		{"assignee manage", true, func(u string) bool { return CanUserManageTask(u, task) }},
		{"assignee delete", false, func(u string) bool { return CanUserDeleteTask(u, task) }},

		{"channel member view", true, func(u string) bool { return CanUserViewTask(u, task, nil, ch) }},
		{"channel member list", true, func(u string) bool { return CanUserListTask(u, task, nil, ch) }},
		{"channel member comment", true, func(u string) bool { return CanUserCommentTask(u, task, nil, ch) }},
		{"channel member manage", false, func(u string) bool { return CanUserManageTask(u, task) }},
		{"channel member delete", false, func(u string) bool { return CanUserDeleteTask(u, task) }},
	}
	actors := map[string]string{
		"creator":        "creator1",
		"assignee":       "assignee1",
		"channel member": "member",
	}
	for _, c := range matrix {
		// Resolve the actor id by matching the matrix row name prefix.
		userID := actorIDFor(c.name, actors)
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.allow, c.check(userID))
		})
	}
}

// actorIDFor maps a matrix row name to the user id used in the fixtures. Rows
// are prefixed by the actor label (e.g. "assignee delete").
func actorIDFor(name string, actors map[string]string) string {
	for label, id := range actors {
		if len(name) >= len(label) && name[:len(label)] == label {
			return id
		}
	}
	return name
}

func TestPermissionMatrix_ChannelAdminTreatedAsMember(t *testing.T) {
	// A channel admin is reported only via membership (there is no admin role
	// in the interface); the rules treat them exactly like any channel member:
	// they may view/comment but NOT manage or delete.
	ch := fakeMembershipChecker{members: map[string]bool{"admin:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserViewTask("admin", task, nil, ch), "admin views like a member")
	assert.True(t, CanUserCommentTask("admin", task, nil, ch), "admin comments like a member")
	assert.False(t, CanUserManageTask("admin", task), "admin cannot manage (no admin power)")
	assert.False(t, CanUserDeleteTask("admin", task), "admin cannot delete (creator-only)")
}

func TestPermissionMatrix_OutsiderCanReadChannelTask(t *testing.T) {
	// Channel tasks are readable by anyone (card is public); an outsider is
	// only denied on a PERSONAL task. Manage/delete stay denied for everyone
	// but the co-owners.
	ch := fakeMembershipChecker{members: map[string]bool{}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserViewTask("outsider", task, nil, ch), "channel task readable by anyone")
	assert.True(t, CanUserListTask("outsider", task, nil, ch))
	assert.True(t, CanUserCommentTask("outsider", task, nil, ch))
	assert.False(t, CanUserManageTask("outsider", task))
	assert.False(t, CanUserDeleteTask("outsider", task))
}

func TestPermissionMatrix_OutsiderDeniedPersonalTask(t *testing.T) {
	// Personal task (no channel surface) is private to creator + assignee.
	ch := fakeMembershipChecker{members: map[string]bool{}}
	personal := taskFixture("creator1", "assignee1", "")

	assert.False(t, CanUserViewTask("outsider", personal, nil, ch), "personal task hidden from outsiders")
	assert.False(t, CanUserListTask("outsider", personal, nil, ch))
	assert.False(t, CanUserCommentTask("outsider", personal, nil, ch))
}

func TestCanUserManageTask_Unassigned(t *testing.T) {
	// Task with no assignee: only creator may manage.
	task := taskFixture("creator1", "", "")
	assert.True(t, CanUserManageTask("creator1", task))
	assert.False(t, CanUserManageTask("anyone", task))
}

func TestCanUserManageTask_Guards(t *testing.T) {
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.False(t, CanUserManageTask("", task), "empty user cannot manage")
	assert.False(t, CanUserManageTask("creator1", nil), "nil task is not manageable")
}

func TestCanUserDeleteTask_PersonalTask(t *testing.T) {
	personal := taskFixture("creator1", "assignee1", "")

	assert.True(t, CanUserDeleteTask("creator1", personal), "creator deletes personal task")
	assert.False(t, CanUserDeleteTask("assignee1", personal), "assignee cannot delete personal task")
}

func TestCanUserDeleteTask_Guards(t *testing.T) {
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.False(t, CanUserDeleteTask("", task), "empty user cannot delete")
	assert.False(t, CanUserDeleteTask("creator1", nil), "nil task is not deletable")
}

func TestCanUserViewTask(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{"member1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserViewTask("creator1", task, nil, ch), "creator can view")
	assert.True(t, CanUserViewTask("assignee1", task, nil, ch), "assignee can view")
	// Channel task is readable by anyone (its card is already public in the
	// channel) — view does not gate on channel membership.
	assert.True(t, CanUserViewTask("member1", task, nil, ch), "channel task readable without membership gate")
	assert.True(t, CanUserViewTask("outsider", task, nil, ch), "channel task readable by anyone")
}

func TestCanUserViewTask_PersonalTask(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{"member1:": true}}
	personal := taskFixture("creator1", "assignee1", "")

	assert.True(t, CanUserViewTask("creator1", personal, nil, ch), "creator can view personal")
	assert.True(t, CanUserViewTask("assignee1", personal, nil, ch), "assignee can view personal")
	assert.False(t, CanUserViewTask("member1", personal, nil, ch), "personal task hidden from others")
}

func TestCanUserCommentTask_FollowsView(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{"member1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	// Channel task: anyone (member or outsider) may comment — the card is
	// public, so comment access follows view.
	assert.True(t, CanUserCommentTask("member1", task, nil, ch), "channel task commentable by anyone")
	assert.True(t, CanUserCommentTask("outsider", task, nil, ch), "channel task commentable by anyone")

	// Personal task: only creator/assignee may comment.
	personal := taskFixture("creator1", "assignee1", "")
	assert.False(t, CanUserCommentTask("outsider", personal, nil, ch), "personal task hidden from outsiders")
}

// A shared task's card lives in a channel OTHER than task.ChannelID. A member
// of that shared channel (who is NOT a member of task.ChannelID) must still be
// able to view AND comment — consistent with "they can read the card thread,
// they can reply". The rule checks IsChannelMember against the union of
// task.ChannelID + the card-channel ids passed by the caller (resolved from
// task_posts).
func TestCanUserViewTask_SharedChannelMemberAllowed(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{
		"sharer:ch-shared":    true,
		"member-home:ch-home": true,
	}}
	task := taskFixture("creator1", "assignee1", "ch-home")
	cardChannels := []string{"ch-shared"}

	assert.True(t, CanUserViewTask("sharer", task, cardChannels, ch),
		"member of a shared card channel may view")
	assert.True(t, CanUserViewTask("member-home", task, nil, ch),
		"member of home channel still views via task.ChannelID")
}

func TestCanUserCommentTask_SharedChannelMemberAllowed(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{"sharer:ch-shared": true}}
	task := taskFixture("creator1", "assignee1", "ch-home")
	cardChannels := []string{"ch-shared"}

	assert.True(t, CanUserCommentTask("sharer", task, cardChannels, ch),
		"member of a shared card channel may comment")
}

// Non-member outsider (member of neither home nor any card channel) is still
// denied — the card-channel expansion does not open the task to everyone.
// A task whose card lives in a channel OTHER than task.ChannelID is also
// readable (the card is public there too). The card-channel expansion means a
// shared task is viewable by anyone who can see the shared card.
func TestCanUserViewTask_CardChannelReadable(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{
		"sharer:ch-shared": true,
	}}
	// Home channel set but no membership recorded for "outsider"; card in shared.
	task := taskFixture("creator1", "assignee1", "ch-home")
	cardChannels := []string{"ch-shared"}

	assert.True(t, CanUserViewTask("outsider", task, cardChannels, ch),
		"task with a card post is readable by anyone")
}

func TestCanUserViewTask_NilChecker(t *testing.T) {
	// A nil checker does not block channel tasks: view no longer depends on
	// channel membership (GetChannelMember can flap). Only personal tasks need
	// protection, and that is independent of the checker.
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, CanUserViewTask("creator1", task, nil, nil), "creator unaffected by nil checker")
	assert.True(t, CanUserViewTask("outsider", task, nil, nil), "channel task readable even with nil checker")

	personal := taskFixture("creator1", "assignee1", "")
	assert.False(t, CanUserViewTask("outsider", personal, nil, nil), "personal task protected regardless of checker")
}
