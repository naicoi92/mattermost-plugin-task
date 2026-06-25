package permission

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// fakeMembershipChecker is a ChannelMembershipChecker backed by an allowlist of
// channel members. There is no separate admin concept in the task permission
// model — channel admins are treated like ordinary channel members.
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

// IsCreator / IsAssignee are the two ownership primitives composing the
// co-owner concept. They are pure, defensive against nil task / empty user.
func TestIsCreator(t *testing.T) {
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, IsCreator("creator1", task))
	assert.False(t, IsCreator("assignee1", task))
	assert.False(t, IsCreator("", task), "empty user")
	assert.False(t, IsCreator("creator1", nil), "nil task")
}

func TestIsAssignee(t *testing.T) {
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, IsAssignee("assignee1", task))
	assert.False(t, IsAssignee("creator1", task))
	assert.False(t, IsAssignee("", task), "empty user")
	assert.False(t, IsAssignee("assignee1", nil), "nil task")
}

// Matrix-driven coverage of the three primitives (IsCreator, IsAssignee,
// IsChannelMember) against every action family for a channel-scoped task.
// Channel admins are NOT a distinct actor — they are tested as ordinary
// channel members.
//
// View/list/comment: granted to creator, assignee, OR a channel member (via
// the checker). Manage/delete: co-owner (creator+assignee) only, with delete
// restricted to the creator.
func TestPermissionMatrix_ChannelTask(t *testing.T) {
	// Channel-scoped task; "member" is a plain member of the home channel.
	ch := fakeMembershipChecker{members: map[string]bool{"member:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	matrix := []struct {
		name  string
		allow bool
		check func(userID string) bool
	}{
		{"creator view", true, func(u string) bool { return CanUserViewTask(u, task, ch) }},
		{"creator list", true, func(u string) bool { return CanUserListTask(u, task, ch) }},
		{"creator comment", true, func(u string) bool { return CanUserCommentTask(u, task, ch) }},
		{"creator manage", true, func(u string) bool { return CanUserManageTask(u, task) }},
		{"creator delete", true, func(u string) bool { return CanUserDeleteTask(u, task) }},

		{"assignee view", true, func(u string) bool { return CanUserViewTask(u, task, ch) }},
		{"assignee list", true, func(u string) bool { return CanUserListTask(u, task, ch) }},
		{"assignee comment", true, func(u string) bool { return CanUserCommentTask(u, task, ch) }},
		{"assignee manage", true, func(u string) bool { return CanUserManageTask(u, task) }},
		{"assignee delete", false, func(u string) bool { return CanUserDeleteTask(u, task) }},

		{"channel member view", true, func(u string) bool { return CanUserViewTask(u, task, ch) }},
		{"channel member list", true, func(u string) bool { return CanUserListTask(u, task, ch) }},
		{"channel member comment", true, func(u string) bool { return CanUserCommentTask(u, task, ch) }},
		{"channel member manage", false, func(u string) bool { return CanUserManageTask(u, task) }},
		{"channel member delete", false, func(u string) bool { return CanUserDeleteTask(u, task) }},
	}
	actors := map[string]string{
		"creator":        "creator1",
		"assignee":       "assignee1",
		"channel member": "member",
	}
	for _, c := range matrix {
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

	assert.True(t, CanUserViewTask("admin", task, ch), "admin views like a member")
	assert.True(t, CanUserCommentTask("admin", task, ch), "admin comments like a member")
	assert.False(t, CanUserManageTask("admin", task), "admin cannot manage (no admin power)")
	assert.False(t, CanUserDeleteTask("admin", task), "admin cannot delete (creator-only)")
}

// Outsider (not a member of task.ChannelID per the checker) is denied view on
// a channel-scoped task. The checker is the single source of truth for
// channel-level access.
func TestPermissionMatrix_OutsiderDeniedChannelTask(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.False(t, CanUserViewTask("outsider", task, ch), "outsider denied view")
	assert.False(t, CanUserListTask("outsider", task, ch))
	assert.False(t, CanUserCommentTask("outsider", task, ch))
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

func TestCanUserViewTask_ChannelMemberAllowed(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{"member1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserViewTask("creator1", task, ch), "creator can view")
	assert.True(t, CanUserViewTask("assignee1", task, ch), "assignee can view")
	assert.True(t, CanUserViewTask("member1", task, ch), "channel member can view")
	assert.False(t, CanUserViewTask("outsider", task, ch), "outsider denied")
}

// Task with no ChannelID (personal, only transiently after migration) and no
// checker-based channel access: only creator + assignee may view. This is the
// defense for any lingering personal task.
func TestCanUserViewTask_NoChannelRestricted(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{}}
	personal := taskFixture("creator1", "assignee1", "")

	assert.True(t, CanUserViewTask("creator1", personal, ch), "creator can view")
	assert.True(t, CanUserViewTask("assignee1", personal, ch), "assignee can view")
	assert.False(t, CanUserViewTask("outsider", personal, ch), "no channel => outsider denied")
}

func TestCanUserCommentTask_FollowsView(t *testing.T) {
	ch := fakeMembershipChecker{members: map[string]bool{"member1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserCommentTask("member1", task, ch), "channel member can comment")
	assert.False(t, CanUserCommentTask("outsider", task, ch), "outsider denied comment")

	// No-channel task: only creator/assignee may comment.
	personal := taskFixture("creator1", "assignee1", "")
	assert.False(t, CanUserCommentTask("outsider", personal, ch), "no channel => outsider denied")
}

func TestCanUserViewTask_NilChecker(t *testing.T) {
	// A nil checker means channel membership cannot be evaluated, so a channel
	// task is only visible to creator/assignee. Personal task is still private.
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, CanUserViewTask("creator1", task, nil), "creator unaffected by nil checker")
	assert.False(t, CanUserViewTask("outsider", task, nil), "channel task private when checker nil")

	personal := taskFixture("creator1", "assignee1", "")
	assert.False(t, CanUserViewTask("outsider", personal, nil), "personal task protected regardless of checker")
}
