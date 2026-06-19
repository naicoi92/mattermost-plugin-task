package kvstore

import (
	"encoding/json"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

func setupTest() (*plugintest.API, KVStore) {
	api := &plugintest.API{}
	driver := &plugintest.Driver{}
	client := pluginapi.NewClient(api, driver)
	store := NewKVStore(client)
	return api, store
}

func TestTaskKey(t *testing.T) {
	assert.Equal(t, "t:task1", TaskKey("task1"))
}

func TestCommentKey(t *testing.T) {
	assert.Equal(t, "t:task1:c:c1", CommentKey("task1", "c1"))
}

func TestSubtaskKey(t *testing.T) {
	assert.Equal(t, "idx:t:parent1:sub:child1", SubtaskKey("parent1", "child1"))
}

func TestUserAssignedKey(t *testing.T) {
	assert.Equal(t, "idx:u:user1:assigned:task1", UserAssignedKey("user1", "task1"))
}

func TestUserCreatedKey(t *testing.T) {
	assert.Equal(t, "idx:u:user1:created:task1", UserCreatedKey("user1", "task1"))
}

func TestChannelTaskKey(t *testing.T) {
	assert.Equal(t, "idx:ch:ch1:task:task1", ChannelTaskKey("ch1", "task1"))
}

func TestAllTasksKey(t *testing.T) {
	assert.Equal(t, "idx:all:task:task1", AllTasksKey("task1"))
}

func TestReminderKey(t *testing.T) {
	assert.Equal(t, "idx:reminder:task1", ReminderKey("task1"))
}

func TestClient_GetTask(t *testing.T) {
	api, store := setupTest()

	t.Run("existing task", func(t *testing.T) {
		task := pmodel.Task{ID: "task1", Summary: "Do something", Status: pmodel.StatusTodo}
		data, err := json.Marshal(task)
		require.NoError(t, err)

		api.On("KVGet", "t:task1").Return(data, nil).Once()

		got, err := store.GetTask("task1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "task1", got.ID)
		assert.Equal(t, "Do something", got.Summary)
	})

	t.Run("missing task returns nil", func(t *testing.T) {
		api.On("KVGet", "t:missing").Return(nil, nil).Once()

		got, err := store.GetTask("missing")
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestClient_SaveTask(t *testing.T) {
	api, store := setupTest()

	t.Run("requires ID", func(t *testing.T) {
		assert.EqualError(t, store.SaveTask(pmodel.Task{}), "task ID is required")
	})

	t.Run("saves task", func(t *testing.T) {
		task := pmodel.Task{ID: "task1", Summary: "Do something", Status: pmodel.StatusTodo}
		expected, err := json.Marshal(task)
		require.NoError(t, err)

		api.On("KVSetWithOptions", "t:task1", expected, model.PluginKVSetOptions{}).Return(true, nil).Once()

		require.NoError(t, store.SaveTask(task))
	})
}

func TestClient_DeleteTask(t *testing.T) {
	api, store := setupTest()

	api.On("KVSetWithOptions", "t:task1", []byte(nil), model.PluginKVSetOptions{}).Return(true, nil).Once()
	require.NoError(t, store.DeleteTask("task1"))
}

func TestClient_SaveAndGetSubtaskIDs(t *testing.T) {
	api, store := setupTest()

	parentID := "parent1"
	childID := "child1"

	// Save subtask index edge.
	api.On("KVSetWithOptions", "idx:t:parent1:sub:child1", jsonEquals(t, struct{}{}), model.PluginKVSetOptions{}).Return(true, nil).Once()
	require.NoError(t, store.SaveSubtask(parentID, childID))

	// List subtasks: first return the index key, then verify the child task exists.
	api.On("KVList", 0, pageSize).Return([]string{"idx:t:parent1:sub:child1"}, nil).Once()
	api.On("KVGet", "t:child1").Return(mustMarshal(t, pmodel.Task{ID: "child1"}), nil).Once()

	ids, err := store.GetSubtaskIDs(parentID)
	require.NoError(t, err)
	assert.Equal(t, []string{"child1"}, ids)
}

func TestClient_ListTaskIDsByPrefix_SelfHealing(t *testing.T) {
	api, store := setupTest()

	// Index references a task that no longer exists.
	api.On("KVList", 0, pageSize).Return([]string{"idx:u:user1:assigned:gone"}, nil).Once()
	api.On("KVGet", "t:gone").Return(nil, nil).Once()
	api.On("KVSetWithOptions", "idx:u:user1:assigned:gone", []byte(nil), model.PluginKVSetOptions{}).Return(true, nil).Once()

	ids, err := store.ListTaskIDsByPrefix("idx:u:user1:assigned:")
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestClient_ListTaskIDsByPrefix_Pagination(t *testing.T) {
	api, store := setupTest()

	// Simulate a full first page of noise that points to missing tasks (self-healing
	// removes them), then a second page with real matches.
	page0 := make([]string, pageSize)
	for i := range page0 {
		page0[i] = "idx:all:task:noise" + string(rune('a'+i))
	}

	api.On("KVList", 0, pageSize).Return(page0, nil).Once()
	for _, key := range page0 {
		api.On("KVGet", "t:"+key[len("idx:all:task:"):]).Return(nil, nil).Once()
		api.On("KVSetWithOptions", key, []byte(nil), model.PluginKVSetOptions{}).Return(true, nil).Once()
	}
	api.On("KVList", 1, pageSize).Return([]string{"idx:all:task:task1", "idx:all:task:task2"}, nil).Once()
	api.On("KVGet", "t:task1").Return(mustMarshal(t, pmodel.Task{ID: "task1"}), nil).Once()
	api.On("KVGet", "t:task2").Return(mustMarshal(t, pmodel.Task{ID: "task2"}), nil).Once()
	api.On("KVList", 2, pageSize).Return([]string{}, nil).Once()

	ids, err := store.ListTaskIDsByPrefix("idx:all:task:")
	require.NoError(t, err)
	assert.Equal(t, []string{"task1", "task2"}, ids)
}

func TestClient_SaveIndex_DeleteIndex(t *testing.T) {
	api, store := setupTest()

	api.On("KVSetWithOptions", "idx:ch:ch1:task:task1", jsonEquals(t, struct{}{}), model.PluginKVSetOptions{}).Return(true, nil).Once()
	require.NoError(t, store.SaveIndex(ChannelTaskKey("ch1", "task1")))

	api.On("KVSetWithOptions", "idx:ch:ch1:task:task1", []byte(nil), model.PluginKVSetOptions{}).Return(true, nil).Once()
	require.NoError(t, store.DeleteIndex(ChannelTaskKey("ch1", "task1")))
}

func TestClient_SaveComment_GetCommentIDs(t *testing.T) {
	api, store := setupTest()

	comment := pmodel.Comment{ID: "c1", UserID: "u1", Content: "LGTM"}
	expected, err := json.Marshal(comment)
	require.NoError(t, err)

	api.On("KVSetWithOptions", "t:task1:c:c1", expected, model.PluginKVSetOptions{}).Return(true, nil).Once()
	require.NoError(t, store.SaveComment("task1", comment))

	api.On("KVList", 0, pageSize).Return([]string{"t:task1:c:c1", "t:task1:c:c2"}, nil).Once()
	ids, err := store.GetCommentIDs("task1")
	require.NoError(t, err)
	assert.Equal(t, []string{"c1", "c2"}, ids)
}

func TestClient_SaveComment_Validation(t *testing.T) {
	_, store := setupTest()

	assert.EqualError(t, store.SaveComment("", pmodel.Comment{ID: "c1"}), "task ID is required")
	assert.EqualError(t, store.SaveComment("task1", pmodel.Comment{}), "comment ID is required")
}

// Issue #23: GetComment reads the single t:{taskID}:c:{commentID} entity key.
func TestClient_GetComment(t *testing.T) {
	api, store := setupTest()

	t.Run("returns comment when present", func(t *testing.T) {
		comment := pmodel.Comment{ID: "c1", UserID: "u1", Content: "LGTM"}
		api.On("KVGet", "t:task1:c:c1").Return(mustMarshal(t, comment), nil).Once()

		got, err := store.GetComment("task1", "c1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "c1", got.ID)
		assert.Equal(t, "LGTM", got.Content)
	})

	t.Run("returns nil when absent", func(t *testing.T) {
		api.On("KVGet", "t:task1:c:ghost").Return(nil, nil).Once()
		got, err := store.GetComment("task1", "ghost")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	// A corrupt payload (undecodable bytes) surfaces as an error. ListComments
	// treats this error defensively (skips the comment) — this case locks the
	// store contract so the service can rely on it.
	t.Run("returns error on corrupt payload", func(t *testing.T) {
		api.On("KVGet", "t:task1:c:c1").Return([]byte("not-json"), nil).Once()
		got, err := store.GetComment("task1", "c1")
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("validation", func(t *testing.T) {
		_, store := setupTest()
		_, err := store.GetComment("", "c1")
		assert.EqualError(t, err, "task ID is required")
		_, err = store.GetComment("task1", "")
		assert.EqualError(t, err, "comment ID is required")
	})
}

func TestClient_SaveSubtask_Validation(t *testing.T) {
	_, store := setupTest()

	assert.EqualError(t, store.SaveSubtask("", "child1"), "parent task ID is required")
	assert.EqualError(t, store.SaveSubtask("parent1", ""), "task ID is required")
}

func TestClient_GetSubtaskIDs_Validation(t *testing.T) {
	_, store := setupTest()

	ids, err := store.GetSubtaskIDs("")
	assert.EqualError(t, err, "parent task ID is required")
	assert.Nil(t, ids)
}

// Issue #20/#23: subtask and comment IDs are sorted by ULID (creation order)
// regardless of the order ListKeys returns them.
func TestClient_GetSubtaskIDs_SortedByULID(t *testing.T) {
	api, store := setupTest()

	// ListKeys returns keys out of order; the store must sort by the embedded id.
	api.On("KVList", 0, pageSize).Return([]string{
		"idx:t:p:sub:02GNF8", "idx:t:p:sub:01HXY9", "idx:t:p:sub:03ABCD",
	}, nil).Once()
	api.On("KVGet", "t:02GNF8").Return(mustMarshal(t, pmodel.Task{ID: "02GNF8"}), nil).Once()
	api.On("KVGet", "t:01HXY9").Return(mustMarshal(t, pmodel.Task{ID: "01HXY9"}), nil).Once()
	api.On("KVGet", "t:03ABCD").Return(mustMarshal(t, pmodel.Task{ID: "03ABCD"}), nil).Once()

	ids, err := store.GetSubtaskIDs("p")
	require.NoError(t, err)
	assert.Equal(t, []string{"01HXY9", "02GNF8", "03ABCD"}, ids, "sorted by ULID/creation order")
}

func TestClient_GetCommentIDs_SortedByULID(t *testing.T) {
	api, store := setupTest()

	api.On("KVList", 0, pageSize).Return([]string{
		"t:task1:c:02C", "t:task1:c:01A", "t:task1:c:03B",
	}, nil).Once()

	ids, err := store.GetCommentIDs("task1")
	require.NoError(t, err)
	assert.Equal(t, []string{"01A", "02C", "03B"}, ids, "sorted by ULID/creation order")
}

func TestClient_SaveReminder_DeleteReminder_Validation(t *testing.T) {
	_, store := setupTest()

	assert.EqualError(t, store.SaveReminder("", pmodel.ReminderMetadata{OffsetMS: 900000}), "task ID is required")
	assert.EqualError(t, store.DeleteReminder(""), "task ID is required")
	_, err := store.GetReminder("")
	assert.EqualError(t, err, "task ID is required")
}

func TestClient_SaveReminder_DeleteReminder_ListReminderKeys(t *testing.T) {
	api, store := setupTest()

	meta := pmodel.ReminderMetadata{DueMS: 1_000_000, OffsetMS: 900000, AssigneeID: "u1"}
	api.On("KVSetWithOptions", "idx:reminder:task1", jsonEquals(t, meta), model.PluginKVSetOptions{}).Return(true, nil).Once()
	require.NoError(t, store.SaveReminder("task1", meta))

	api.On("KVList", 0, pageSize).Return([]string{"idx:reminder:task1", "idx:reminder:task2"}, nil).Once()
	keys, err := store.ListReminderKeys()
	require.NoError(t, err)
	assert.Equal(t, []string{"idx:reminder:task1", "idx:reminder:task2"}, keys)

	api.On("KVSetWithOptions", "idx:reminder:task1", []byte(nil), model.PluginKVSetOptions{}).Return(true, nil).Once()
	require.NoError(t, store.DeleteReminder("task1"))
}

func TestClient_GetReminder(t *testing.T) {
	api, store := setupTest()

	t.Run("returns metadata when present", func(t *testing.T) {
		meta := pmodel.ReminderMetadata{DueMS: 5, OffsetMS: 1, AssigneeID: "u1"}
		api.On("KVGet", "idx:reminder:task1").Return(mustMarshal(t, meta), nil).Once()

		got, err := store.GetReminder("task1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, meta, *got)
	})

	t.Run("returns nil when no reminder", func(t *testing.T) {
		api.On("KVGet", "idx:reminder:task2").Return(nil, nil).Once()
		got, err := store.GetReminder("task2")
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

// mustMarshal returns the JSON encoding of v.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// jsonEquals returns a matcher that compares JSON-encoded values.
func jsonEquals(t *testing.T, expected any) any {
	t.Helper()
	expectedBytes, err := json.Marshal(expected)
	require.NoError(t, err)
	return expectedBytes
}
