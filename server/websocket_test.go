package main

import (
	"testing"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

// publishCall is one recorded PublishWebSocketEvent invocation.
type publishCall struct {
	event     string
	payload   map[string]any
	broadcast *mmmodel.WebsocketBroadcast
}

// capturePublish installs a mock that records every PublishWebSocketEvent call
// into calls, returning the (event, payload, broadcast) triple in order.
func capturePublish() (*plugintest.API, *[]publishCall) {
	api := &plugintest.API{}
	calls := &[]publishCall{}
	api.On("PublishWebSocketEvent", mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			*calls = append(*calls, publishCall{
				event:     args.Get(0).(string),
				payload:   args.Get(1).(map[string]any),
				broadcast: args.Get(2).(*mmmodel.WebsocketBroadcast),
			})
		}).Return().Maybe()
	return api, calls
}

func TestBroadcastTaskUpdated_ChannelScope(t *testing.T) {
	api, calls := capturePublish()
	p := &Plugin{}
	p.SetAPI(api)

	task := &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "t1", ChannelID: "ch1", Status: taskmodel.StatusTodo, UpdatedAt: 100}}
	p.broadcastTaskUpdated(task, []string{"status"})

	require.Len(t, *calls, 1)
	c := (*calls)[0]
	assert.Equal(t, wsEventTaskUpdated, c.event)
	assert.Equal(t, "ch1", c.broadcast.ChannelId)
	assert.Empty(t, c.broadcast.UserId, "channel broadcast must not set UserId")

	// Payload schema (PLAN §Phụ lục B).
	assert.Equal(t, "t1", c.payload["task_id"])
	assert.Equal(t, int64(100), c.payload["seq"])
	assert.Equal(t, int64(100), c.payload["updated_at"])
	assert.Equal(t, []string{"status"}, c.payload["changed_fields"])
	assert.Equal(t, "t1", taskPayloadID(t, c.payload["task"]))
}

func TestBroadcastTaskUpdated_PersonalScope_CreatorAndAssignee(t *testing.T) {
	api, calls := capturePublish()
	p := &Plugin{}
	p.SetAPI(api)

	// Personal task (no channel): creator + assignee are distinct.
	task := &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "t1", UpdatedAt: 5}, CreatorID: "creator", AssigneeID: "assignee"}
	p.broadcastTaskUpdated(task, []string{"created"})

	require.Len(t, *calls, 2, "personal task notifies creator and assignee")
	recipients := map[string]bool{}
	for _, c := range *calls {
		assert.NotEmpty(t, c.broadcast.UserId)
		assert.Empty(t, c.broadcast.ChannelId, "personal broadcast must not set ChannelId")
		recipients[c.broadcast.UserId] = true
	}
	assert.True(t, recipients["creator"])
	assert.True(t, recipients["assignee"])
}

func TestBroadcastTaskUpdated_PersonalScope_CreatorIsAssignee_Deduped(t *testing.T) {
	api, calls := capturePublish()
	p := &Plugin{}
	p.SetAPI(api)

	// Creator == assignee: only one event.
	task := &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "t1", UpdatedAt: 5}, CreatorID: "u1", AssigneeID: "u1"}
	p.broadcastTaskUpdated(task, []string{"status"})

	require.Len(t, *calls, 1, "creator==assignee must dedupe to a single event")
	assert.Equal(t, "u1", (*calls)[0].broadcast.UserId)
}

func TestBroadcastTaskUpdated_NilTask_NoOp(t *testing.T) {
	api, calls := capturePublish()
	p := &Plugin{}
	p.SetAPI(api)

	p.broadcastTaskUpdated(nil, []string{"status"})
	assert.Empty(t, *calls, "nil task must not publish")
}

func TestBroadcastTaskDeleted_OmitsTaskBody(t *testing.T) {
	api, calls := capturePublish()
	p := &Plugin{}
	p.SetAPI(api)

	task := &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "t1", UpdatedAt: 9}, CreatorID: "u1"}
	p.broadcastTaskDeleted(task)

	require.Len(t, *calls, 1)
	c := (*calls)[0]
	assert.Equal(t, "t1", c.payload["task_id"])
	assert.Nil(t, c.payload["task"], "delete payload must omit the task body")
	assert.Equal(t, []string{"deleted"}, c.payload["changed_fields"])
}

func TestBroadcastTaskUpdated_AssigneeOnly_NoCreator(t *testing.T) {
	api, calls := capturePublish()
	p := &Plugin{}
	p.SetAPI(api)

	// Personal task with only an assignee (creator empty — synthetic edge case).
	task := &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "t1", UpdatedAt: 1}, AssigneeID: "a1"}
	p.broadcastTaskUpdated(task, []string{"assignee_id"})

	require.Len(t, *calls, 1)
	assert.Equal(t, "a1", (*calls)[0].broadcast.UserId)
}

// taskPayloadID extracts the task id from a marshalled task payload (now a
// map[string]any rather than *model.Task).
func taskPayloadID(t *testing.T, v any) string {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	id, _ := m["id"].(string)
	return id
}
