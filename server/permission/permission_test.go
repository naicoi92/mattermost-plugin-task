package permission

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// fakeMembershipChecker is a ChannelMembershipChecker backed by two allowlists:
// one for plain members (view/comment rule) and one for admins (delete rule).
// Keeping the maps separate mirrors the production distinction between channel
// membership and channel admin status.
type fakeMembershipChecker struct {
	members map[string]bool // "userID:channelID" -> member
	admins  map[string]bool // "userID:channelID" -> admin
}

func (f fakeMembershipChecker) IsChannelMember(userID, channelID string) bool {
	return f.members[userID+":"+channelID]
}

func (f fakeMembershipChecker) IsChannelAdmin(userID, channelID string) bool {
	return f.admins[userID+":"+channelID]
}

func taskFixture(creator, assignee, channel string) *model.Task {
	return &model.Task{
		CreatorID:  creator,
		AssigneeID: assignee,
		TaskRow:    model.TaskRow{ChannelID: channel},
	}
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
	ch := fakeMembershipChecker{
		admins:  map[string]bool{"admin1:ch1": true},
		members: map[string]bool{"member1:ch1": true},
	}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserDeleteTask("creator1", task, ch), "creator can always delete")
	assert.False(t, CanUserDeleteTask("assignee1", task, ch), "assignee cannot delete")
	assert.False(t, CanUserDeleteTask("other", task, ch), "random member cannot delete")
	assert.True(t, CanUserDeleteTask("admin1", task, ch), "channel admin can delete channel task")
	// A plain channel member (not an admin) must not be able to delete: guards
	// against regressions that widen the delete rule to all channel members.
	assert.False(t, CanUserDeleteTask("member1", task, ch), "channel member (non-admin) cannot delete")
}

func TestCanUserDeleteTask_AdminIsNotMember(t *testing.T) {
	// Sanity: an admin who is not registered as a plain member still deletes
	// (admin and member are independent concepts).
	ch := fakeMembershipChecker{
		admins:  map[string]bool{"admin1:ch1": true},
		members: map[string]bool{},
	}
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, CanUserDeleteTask("admin1", task, ch))
}

func TestCanUserDeleteTask_PersonalTask(t *testing.T) {
	ch := fakeMembershipChecker{admins: map[string]bool{"admin1:": true}}
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
	ch := fakeMembershipChecker{members: map[string]bool{"member1:ch1": true}}
	task := taskFixture("creator1", "assignee1", "ch1")

	assert.True(t, CanUserViewTask("creator1", task, nil, ch), "creator can view")
	assert.True(t, CanUserViewTask("assignee1", task, nil, ch), "assignee can view")
	assert.True(t, CanUserViewTask("member1", task, nil, ch), "channel member can view channel task")
	assert.False(t, CanUserViewTask("outsider", task, nil, ch), "non-member cannot view")
}

func TestCanUserViewTask_MemberIsNotAdmin(t *testing.T) {
	// A plain member (not an admin) can view but the view check must not depend
	// on admin status — confirms the two concepts are now separate.
	ch := fakeMembershipChecker{
		members: map[string]bool{"member1:ch1": true},
		admins:  map[string]bool{},
	}
	task := taskFixture("creator1", "assignee1", "ch1")
	assert.True(t, CanUserViewTask("member1", task, nil, ch))
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

// Change C: a shared task's card lives in a channel OTHER than task.ChannelID.
// A member of that shared channel (who is NOT a member of task.ChannelID) must
// still be able to view AND comment — consistent with "they can read the card
// thread, they can reply". The rule checks IsChannelMember against the union of
// task.ChannelID + the card-channel ids passed by the caller (resolved from
// task_posts).
func TestCanUserViewTask_SharedChannelMemberAllowed(t *testing.T) {
	// Home channel ch-home; share card in ch-shared. sharer is a member of
	// ch-shared ONLY (not ch-home).
	ch := fakeMembershipChecker{members: map[string]bool{
		"sharer:ch-shared":    true,
		"member-home:ch-home": true,
	}}
	// task.ChannelID == ch-home; card also lives in ch-shared.
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
