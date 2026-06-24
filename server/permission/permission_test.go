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

func TestPermissionMatrix_OutsiderDeniedEverything(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.False(t, CanUserViewTask("outsider", task, nil, ch))
	assert.False(t, CanUserListTask("outsider", task, nil, ch))
	assert.False(t, CanUserCommentTask("outsider", task, nil, ch))
	assert.False(t, CanUserManageTask("outsider", task))
	assert.False(t, CanUserDeleteTask("outsider", task))
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
	assert.True(t, CanUserViewTask("member1", task, nil, ch), "channel member can view channel task")
	assert.False(t, CanUserViewTask("outsider", task, nil, ch), "non-member cannot view")
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

	assert.True(t, CanUserCommentTask("member1", task, nil, ch), "viewers may comment")
	assert.False(t, CanUserCommentTask("outsider", task, nil, ch), "non-viewers cannot comment")
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
func TestCanUserViewTask_OutsiderStillDeniedWithCardChannels(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{
		"sharer:ch-shared": true,
	}}
	task := taskFixture("creator1", "assignee1", "ch-home")
	cardChannels := []string{"ch-shared"}

	assert.False(t, CanUserViewTask("outsider", task, cardChannels, ch),
		"non-member of home AND card channels is denied")
}

func TestCanUserViewTask_NilChecker(t *testing.T) {
	// A nil checker blocks channel-member-based access; only creator/assignee
	// can view. Guards handlers that may run before the checker is wired.
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, CanUserViewTask("creator1", task, nil, nil), "creator unaffected by nil checker")
	assert.False(t, CanUserViewTask("member1", task, nil, nil), "nil checker blocks channel member")
}
