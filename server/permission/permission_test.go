package permission

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// fakeAdminChecker is a ChannelAdminChecker backed by an allowlist, for tests.
type fakeAdminChecker struct {
	admins map[string]bool // "userID:channelID" -> true
	member map[string]bool // membership for view checks
}

func (f fakeAdminChecker) IsChannelAdmin(userID, channelID string) bool {
	return f.admins[userID+":"+channelID] || f.member[userID+":"+channelID]
}

func taskFixture(creator, assignee, channel string) *model.Task {
	return &model.Task{CreatorID: creator, AssigneeID: assignee, ChannelID: channel}
}

func TestCanUserModifyTask(t *testing.T) {
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserModifyTask("creator1", task), "creator can modify")
	assert.True(t, CanUserModifyTask("assignee1", task), "assignee (co-owner) can modify")
	assert.False(t, CanUserModifyTask("other", task), "third party cannot modify")
	assert.False(t, CanUserModifyTask("", task), "empty user cannot modify")
	assert.False(t, CanUserModifyTask("creator1", nil), "nil task is not modifiable")
}

func TestCanUserModifyTask_Unassigned(t *testing.T) {
	// Task with no assignee: only creator may modify.
	task := taskFixture("creator1", "", "")
	assert.True(t, CanUserModifyTask("creator1", task))
	assert.False(t, CanUserModifyTask("anyone", task))
}

func TestCanUserDeleteTask(t *testing.T) {
	ch := fakeAdminChecker{admins: map[string]bool{"admin1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserDeleteTask("creator1", task, ch), "creator can always delete")
	assert.False(t, CanUserDeleteTask("assignee1", task, ch), "assignee cannot delete")
	assert.False(t, CanUserDeleteTask("other", task, ch), "random member cannot delete")
	assert.True(t, CanUserDeleteTask("admin1", task, ch), "channel admin can delete channel task")
}

func TestCanUserDeleteTask_PersonalTask(t *testing.T) {
	ch := fakeAdminChecker{admins: map[string]bool{"admin1:": true}}
	personal := taskFixture("creator1", "assignee1", "")

	// Personal task: only creator may delete; channel-admin rule does not apply.
	assert.True(t, CanUserDeleteTask("creator1", personal, ch))
	assert.False(t, CanUserDeleteTask("admin1", personal, ch), "admin cannot delete personal task")
}

func TestCanUserDeleteTask_NilChecker(t *testing.T) {
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, CanUserDeleteTask("creator1", task, nil), "creator unaffected by nil checker")
	assert.False(t, CanUserDeleteTask("admin1", task, nil), "nil checker blocks non-creator")
}

func TestCanUserViewTask(t *testing.T) {
	ch := fakeAdminChecker{member: map[string]bool{"member1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserViewTask("creator1", task, ch), "creator can view")
	assert.True(t, CanUserViewTask("assignee1", task, ch), "assignee can view")
	assert.True(t, CanUserViewTask("member1", task, ch), "channel member can view channel task")
	assert.False(t, CanUserViewTask("outsider", task, ch), "non-member cannot view")
}

func TestCanUserViewTask_PersonalTask(t *testing.T) {
	ch := fakeAdminChecker{member: map[string]bool{"member1:": true}}
	personal := taskFixture("creator1", "assignee1", "")

	assert.True(t, CanUserViewTask("creator1", personal, ch), "creator can view personal")
	assert.True(t, CanUserViewTask("assignee1", personal, ch), "assignee can view personal")
	assert.False(t, CanUserViewTask("member1", personal, ch), "personal task hidden from others")
}

func TestCanUserCommentTask_FollowsView(t *testing.T) {
	ch := fakeAdminChecker{member: map[string]bool{"member1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserCommentTask("member1", task, ch), "viewers may comment")
	assert.False(t, CanUserCommentTask("outsider", task, ch), "non-viewers cannot comment")
}
