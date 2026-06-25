package main

import (
	"context"
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// newHookTestPlugin wires a Plugin with a real sqlite store + service and a
// permissive mock API, for testing the MessageHasBeenPosted hook.
func newHookTestPlugin(t *testing.T) *Plugin {
	t.Helper()
	st := newTestTaskStore(t)
	api := &plugintest.API{}
	api.On("LogDebug", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	for n := 1; n <= 9; n++ {
		args := make([]any, n)
		for i := range args {
			args[i] = mock.Anything
		}
		api.On("LogError", args...).Return().Maybe()
	}
	api.On("PublishWebSocketEvent", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	p := &Plugin{
		taskStore:   st,
		taskService: task.NewService(st),
		botUserID:   "bot",
	}
	p.SetAPI(api)
	return p
}

// createCardForTask posts a card and records it in task_posts, returning the
// root post id so the test can reply under it.
func createCardForTask(t *testing.T, p *Plugin, taskID string) string {
	t.Helper()
	ctx := context.Background()
	rootPostID := "card-root-" + taskID
	require.NoError(t, p.taskStore.SetChannelPostID(ctx, taskID, rootPostID))
	return rootPostID
}

func TestMessageHasBeenPosted_ReplyOnCardThreadLinksComment(t *testing.T) {
	p := newHookTestPlugin(t)
	task := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	rootPostID := createCardForTask(t, p, task.ID)

	// A user replies in the card thread.
	reply := &mmmodel.Post{
		Id:       "reply-1",
		UserId:   "u-commenter",
		RootId:   rootPostID,
		Message:  "looks good",
		CreateAt: 1_000,
	}
	p.MessageHasBeenPosted(nil, reply)

	// The task_comments table must have exactly one row linking reply-1.
	ctx := context.Background()
	comments, err := p.taskStore.ListComments(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "reply-1", comments[0].PostID)
	assert.Equal(t, "u-commenter", comments[0].AuthorID)
}

func TestMessageHasBeenPosted_NonCardReplyDoesNothing(t *testing.T) {
	p := newHookTestPlugin(t)
	task := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	_ = task

	// A reply in a non-task thread.
	reply := &mmmodel.Post{
		Id:       "reply-other",
		UserId:   "u-x",
		RootId:   "some-other-root-post",
		Message:  "hi",
		CreateAt: 1,
	}
	p.MessageHasBeenPosted(nil, reply)

	// No comment linked to any task. Check by counting on the task we created.
	ctx := context.Background()
	count, err := p.taskStore.CountComments(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestMessageHasBeenPosted_TopLevelPostIgnored(t *testing.T) {
	p := newHookTestPlugin(t)
	task := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	_ = createCardForTask(t, p, task.ID)

	// A top-level post (no RootId) — not a reply.
	topLevel := &mmmodel.Post{
		Id:       "top-1",
		UserId:   "u-x",
		RootId:   "",
		Message:  "new thread",
		CreateAt: 1,
	}
	p.MessageHasBeenPosted(nil, topLevel)

	ctx := context.Background()
	count, err := p.taskStore.CountComments(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "top-level post must not be linked as a comment")
}

func TestMessageHasBeenPosted_BotPostIgnored(t *testing.T) {
	p := newHookTestPlugin(t)
	task := createTaskViaService(t, p, task.CreateInput{Summary: "x", ChannelID: "ch1", CreatorID: "u1"})
	rootPostID := createCardForTask(t, p, task.ID)

	// The bot posts in the card thread (e.g. a card refresh) — must be skipped
	// to avoid a loop.
	botReply := &mmmodel.Post{
		Id:       "bot-reply",
		UserId:   p.botUserID,
		RootId:   rootPostID,
		Message:  "card updated",
		CreateAt: 1,
	}
	p.MessageHasBeenPosted(nil, botReply)

	ctx := context.Background()
	count, err := p.taskStore.CountComments(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "bot post must not be linked as a comment")
}

// TestOtherDMParticipant verifies the DM name parsing helper used by the
// deactivation hook to find the non-deactivated participant.
func TestOtherDMParticipant(t *testing.T) {
	cases := []struct {
		name    string
		channel *mmmodel.Channel
		userID  string
		want    string
	}{
		{"two-user DM, me first", &mmmodel.Channel{Type: mmmodel.ChannelTypeDirect, Name: "u1__u2"}, "u1", "u2"},
		{"two-user DM, me second", &mmmodel.Channel{Type: mmmodel.ChannelTypeDirect, Name: "u2__u1"}, "u1", "u2"},
		{"self-DM", &mmmodel.Channel{Type: mmmodel.ChannelTypeDirect, Name: "u1__u1"}, "u1", ""},
		{"not a DM", &mmmodel.Channel{Type: mmmodel.ChannelTypeOpen, Name: "town-square"}, "u1", ""},
		{"nil channel", nil, "u1", ""},
		{"user not in DM", &mmmodel.Channel{Type: mmmodel.ChannelTypeDirect, Name: "u2__u3"}, "u1", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, otherDMParticipant(tc.channel, tc.userID))
		})
	}
}

// TestUserHasBeenDeactivated_MigratesDMTask: when a DM participant is
// deactivated, their DM-scoped task is relocated to the other (still-active)
// participant's self-DM. The card is reposted and the ChannelID updated.
func TestUserHasBeenDeactivated_MigratesDMTask(t *testing.T) {
	p, _ := newTestPlugin(t)
	api := p.API.(*plugintest.API)
	reseedGetChannelMember(t, api)
	// Drop default GetChannel/GetDirectChannel mocks so DM-specific ones win.
	var kept []*mock.Call
	for _, c := range api.ExpectedCalls {
		if c.Method != "GetChannel" && c.Method != "GetDirectChannel" {
			kept = append(kept, c)
		}
	}
	api.ExpectedCalls = kept

	created := createTaskViaService(t, p, task.CreateInput{ChannelID: "dm-a-b", Summary: "dm task", CreatorID: "u-a", AssigneeID: "u-b"})
	api.On("GetChannel", "dm-a-b").Return(&mmmodel.Channel{Id: "dm-a-b", Type: mmmodel.ChannelTypeDirect, Name: "u-a__u-b"}, nil).Maybe()
	api.On("GetUser", "u-a").Return(&mmmodel.User{Id: "u-a", DeleteAt: 0}, nil).Maybe()
	api.On("GetDirectChannel", "u-a", "u-a").Return(&mmmodel.Channel{Id: "dm-a-self", Type: mmmodel.ChannelTypeDirect}, nil).Maybe()
	api.On("GetChannelMember", mock.Anything, mock.Anything).Return(&mmmodel.ChannelMember{}, nil).Maybe()

	p.UserHasBeenDeactivated(nil, &mmmodel.User{Id: "u-b"})

	got, err := p.taskService.Get(created.ID)
	require.NoError(t, err)
	assert.Equal(t, "dm-a-self", got.ChannelID, "task relocated to active participant's self-DM")
}

