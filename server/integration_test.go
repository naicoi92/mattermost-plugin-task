package main

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/notification"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// fakeAPIForIntegration records posts and users for the integration flows.
type fakeAPIForIntegration struct {
	users map[string]*model.User
	posts []*model.Post
	dms   []*model.Post
}

func (f *fakeAPIForIntegration) GetUser(userID string) (*model.User, error) {
	return f.users[userID], nil
}

func (f *fakeAPIForIntegration) GetDirectChannel(userID1, userID2 string) (*model.Channel, error) {
	return &model.Channel{Id: "dm:" + userID1 + ":" + userID2}, nil
}

func (f *fakeAPIForIntegration) CreatePost(post *model.Post) (*model.Post, error) {
	if post.UserId != "bot" {
		f.posts = append(f.posts, post)
	} else {
		f.dms = append(f.dms, post)
	}
	return post, nil
}
func (f *fakeAPIForIntegration) LogError(message string, keyValuePairs ...any) {}

// adapt satisfies notification.API (string returns, no *AppError).
type notifAPIAdapter struct{ inner *fakeAPIForIntegration }

func (a notifAPIAdapter) GetUser(userID string) (*model.User, error) { return a.inner.GetUser(userID) }

func (a notifAPIAdapter) GetDirectChannel(u1, u2 string) (*model.Channel, error) {
	return a.inner.GetDirectChannel(u1, u2)
}

func (a notifAPIAdapter) CreatePost(post *model.Post) (*model.Post, error) {
	return a.inner.CreatePost(post)
}

func (a notifAPIAdapter) LogError(message string, kv ...any) { a.inner.LogError(message, kv...) }

// fakeTranslation is a minimal Translator for the integration flow.
type fakeTranslation struct{}

func (fakeTranslation) T(locale, key string, args ...any) string {
	parts := make([]string, 0, len(args)+2)
	parts = append(parts, locale, key)
	for _, a := range args {
		if s, ok := a.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ":")
}

// TestIntegration_CreateAssignStatusNotifies exercises the core Phase 1 flow
// end-to-end at the service + notification layer: create → assign (DM) → done
// (card + DM), verifying the notification rules and that the card renders.
func TestIntegration_CreateAssignStatusNotifies(t *testing.T) {
	api := &fakeAPIForIntegration{
		users: map[string]*model.User{
			"u-creator":  {Id: "u-creator", Username: "creator", Locale: "en"},
			"u-assignee": {Id: "u-assignee", Username: "assignee", Locale: "vi"},
		},
	}
	notifier := notification.New(notifAPIAdapter{api}, fakeTranslation{}, "bot")

	store := newFakeTaskStore()
	svc := task.NewService(store)

	// Create a task assigned to someone other than the creator.
	created, err := svc.Create(task.CreateInput{
		Summary: "Ship release", CreatorID: "u-creator", AssigneeID: "u-assignee",
	})
	require.NoError(t, err)
	require.NotNil(t, created)

	// Assign should DM the new assignee.
	_, ev, err := svc.Assign(created.ID, "u-assignee")
	require.NoError(t, err)
	notifier.NotifyAssigned(ev.NewAssigneeID, ev.CreatorID, notification.TaskSummary{
		ID: created.ID, Summary: created.Summary,
	})
	require.Len(t, api.dms, 1, "assignee DM'd on assign")
	assert.Contains(t, api.dms[0].Message, "notification.assigned")
	assert.Equal(t, "dm:u-assignee:bot", api.dms[0].ChannelId, "DM targets the assignee")

	// Mark done: card should render terminal + creator+assignee DM'd (minus actor).
	done, err := svc.SetStatus(created.ID, taskmodel.StatusDone)
	require.NoError(t, err)
	assert.NotNil(t, done.CompletedAt)

	card := buildTaskCard(done, 0, 0, 0)
	assert.Equal(t, "~~Ship release~~", card.Title, "done task struck-through")

	api.dms = nil
	notifier.NotifyCompleted(notification.TaskSummary{ID: done.ID, Summary: done.Summary},
		"u-creator", "u-creator", "u-assignee")
	// Only the assignee is DM'd (actor=creator excluded).
	require.Len(t, api.dms, 1)
	assert.Contains(t, api.dms[0].Message, "notification.completed")
	assert.Equal(t, "dm:u-assignee:bot", api.dms[0].ChannelId, "DM targets the assignee")
}

// remStore is a KVStore fake that actually persists reminders (unlike the
// api-test stub), so FireReadyReminders can scan them. It delegates task/index
// storage to an in-memory map.
type remStore struct {
	tasks     map[string]*taskmodel.Task
	indexes   map[string]struct{}
	reminders map[string]taskmodel.ReminderMetadata
}

func newRemStore() *remStore {
	return &remStore{
		tasks: map[string]*taskmodel.Task{}, indexes: map[string]struct{}{},
		reminders: map[string]taskmodel.ReminderMetadata{},
	}
}

func (s *remStore) GetTask(id string) (*taskmodel.Task, error) { return s.tasks[id], nil }
func (s *remStore) SaveTask(t taskmodel.Task) error            { s.tasks[t.ID] = &t; return nil }
func (s *remStore) DeleteTask(id string) error                 { delete(s.tasks, id); return nil }
func (s *remStore) SaveIndex(key string) error                 { s.indexes[key] = struct{}{}; return nil }

func (s *remStore) DeleteIndex(key string) error              { delete(s.indexes, key); return nil }
func (s *remStore) SaveSubtask(parentID, taskID string) error { return nil }
func (s *remStore) GetSubtaskIDs(parentID string) ([]string, error) {
	return nil, nil
}
func (s *remStore) SaveComment(taskID string, c taskmodel.Comment) error { return nil }
func (s *remStore) GetCommentIDs(taskID string) ([]string, error)        { return nil, nil }
func (s *remStore) SaveReminder(taskID string, m taskmodel.ReminderMetadata) error {
	s.reminders[taskID] = m
	return nil
}

func (s *remStore) GetReminder(taskID string) (*taskmodel.ReminderMetadata, error) {
	r, ok := s.reminders[taskID]
	if !ok {
		return nil, nil
	}
	return &r, nil
}
func (s *remStore) DeleteReminder(taskID string) error { delete(s.reminders, taskID); return nil }
func (s *remStore) ListReminderKeys() ([]string, error) {
	keys := make([]string, 0, len(s.reminders))
	for id := range s.reminders {
		keys = append(keys, "idx:reminder:"+id)
	}
	return keys, nil
}
func (s *remStore) ListTaskIDsByPrefix(prefix string) ([]string, error) { return nil, nil }
func (s *remStore) ListUserAssignedTaskIDs(userID string) ([]string, error) {
	return nil, nil
}
func (s *remStore) ListUserCreatedTaskIDs(userID string) ([]string, error) { return nil, nil }
func (s *remStore) ListChannelTaskIDs(channelID string) ([]string, error)  { return nil, nil }
func (s *remStore) ListAllTaskIDs() ([]string, error)                      { return nil, nil }
func (s *remStore) SetAtomicWithRetries(key string, fn func([]byte) (any, error)) error {
	return nil
}

// TestIntegration_ReminderFiresOnceAndSkipsTerminal verifies the reminder
// scheduler path: a due reminder fires for an open task, and the same edge on
// a done task is self-healed (dropped) without firing.
func TestIntegration_ReminderFiresOnceAndSkipsTerminal(t *testing.T) {
	store := newRemStore()
	svc := task.NewService(store)

	// Open task with a due + reminder offset -> eligible reminder edge.
	due := int64(100_000)
	open, err := svc.Create(task.CreateInput{Summary: "open", CreatorID: "u1", AssigneeID: "u2", Due: &due})
	require.NoError(t, err)
	_, err = svc.SetReminder(open.ID, 60_000) // fires at 40_000
	require.NoError(t, err)

	due2 := int64(100_000)
	doneTask, err := svc.Create(task.CreateInput{Summary: "done", CreatorID: "u1", AssigneeID: "u2", Due: &due2})
	require.NoError(t, err)
	_, err = svc.SetReminder(doneTask.ID, 60_000)
	require.NoError(t, err)
	_, err = svc.SetStatus(doneTask.ID, taskmodel.StatusDone)
	require.NoError(t, err)

	// now=50_000: open task's fire window is open; done task's edge self-heals.
	ready, err := svc.FireReadyReminders(50_000, 0)
	require.NoError(t, err)
	require.Len(t, ready, 1, "only the open task fires")
	assert.Equal(t, open.ID, ready[0].TaskID)

	// The done task's reminder edge is gone (self-healed).
	meta, err := store.GetReminder(doneTask.ID)
	require.NoError(t, err)
	assert.Nil(t, meta)
}
