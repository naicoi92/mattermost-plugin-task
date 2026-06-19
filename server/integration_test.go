package main

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/notification"
	"github.com/naicoi92/mattermost-plugin-task/server/store/kvstore"
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

	card := buildTaskCard(done, 0, 0, 0, 0)
	assert.Equal(t, "~~Ship release~~", card.Title, "done task struck-through")

	api.dms = nil
	notifier.NotifyCompleted(notification.TaskSummary{ID: done.ID, Summary: done.Summary},
		"u-creator", "u-creator", "u-assignee")
	// Only the assignee is DM'd (actor=creator excluded).
	require.Len(t, api.dms, 1)
	assert.Contains(t, api.dms[0].Message, "notification.completed")
	assert.Equal(t, "dm:u-assignee:bot", api.dms[0].ChannelId, "DM targets the assignee")
}

// --- Phase 2 integration tests (issue #26) ---

// newIntegrationNotifier builds a notifier over a fresh fake API for the
// Phase 2 flows.
func newIntegrationNotifier(api *fakeAPIForIntegration) *notification.Notifier {
	if api.users == nil {
		api.users = map[string]*model.User{}
	}
	return notification.New(notifAPIAdapter{api}, fakeTranslation{}, "bot")
}

// TestIntegration_Phase2_SubtaskInheritsAndProgress exercises the subtask
// create-from-parent flow end-to-end: a subtask inherits the parent's channel
// and assignee, and the parent card reflects subtask progress.
func TestIntegration_Phase2_SubtaskInheritsAndProgress(t *testing.T) {
	store := newFakeTaskStore()
	svc := task.NewService(store)

	parent, err := svc.Create(task.CreateInput{
		Summary: "Epic", CreatorID: "u-creator", AssigneeID: "u-doer", ChannelID: "ch1",
	})
	require.NoError(t, err)

	// Add two subtasks; both inherit ch1 and the parent's assignee by default.
	sub1, err := svc.CreateSubtask(parent.ID, "u-creator", "part A", "", nil)
	require.NoError(t, err)
	sub2, err := svc.CreateSubtask(parent.ID, "u-creator", "part B", "", nil)
	require.NoError(t, err)
	for _, s := range []*taskmodel.Task{sub1, sub2} {
		assert.Equal(t, "ch1", s.ChannelID, "subtask inherits parent channel")
		assert.Equal(t, "u-doer", s.AssigneeID, "subtask default assignee = parent assignee")
	}

	// Complete one subtask: parent progress becomes 1/2.
	_, err = svc.SetStatus(sub1.ID, taskmodel.StatusDone)
	require.NoError(t, err)
	done, total, err := svc.SubtaskProgress(parent.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, done)
	assert.Equal(t, 2, total)

	// The card renders the progress.
	parent2, gerr := svc.Get(parent.ID)
	require.NoError(t, gerr)
	require.NotNil(t, parent2)
	card := buildTaskCard(parent2, 0, done, total, 0)
	var subtaskField string
	for _, f := range card.Fields {
		if f.Title == "Subtasks" {
			if s, ok := f.Value.(string); ok {
				subtaskField = s
			}
		}
	}
	assert.Equal(t, "1/2 done", subtaskField)
}

// TestIntegration_Phase2_ParentDoneBlockedThenAllowed covers the manual E2E
// step "mark subtasks done and then mark parent done": the parent can't be done
// with an open subtask, but can once all are terminal.
func TestIntegration_Phase2_ParentDoneBlockedThenAllowed(t *testing.T) {
	store := newFakeTaskStore()
	svc := task.NewService(store)

	parent, err := svc.Create(task.CreateInput{Summary: "Epic", CreatorID: "u-creator"})
	require.NoError(t, err)
	sub, err := svc.CreateSubtask(parent.ID, "u-creator", "only part", "", nil)
	require.NoError(t, err)

	// Parent done blocked while the subtask is open.
	_, err = svc.SetStatus(parent.ID, taskmodel.StatusDone)
	require.Error(t, err)
	var blocked task.ErrOpenSubtasks
	require.ErrorAs(t, err, &blocked)
	assert.Contains(t, err.Error(), "only part", "blocking message lists the open subtask")

	// Complete the subtask, then the parent can be done.
	_, err = svc.SetStatus(sub.ID, taskmodel.StatusDone)
	require.NoError(t, err)
	updated, err := svc.SetStatus(parent.ID, taskmodel.StatusDone)
	require.NoError(t, err)
	assert.Equal(t, taskmodel.StatusDone, updated.Status)
}

// TestIntegration_Phase2_CancelParentCascades covers "cancel parent and observe
// subtasks cancelled": cancelling the parent cascade-cancels its open subtask,
// and participants are notified once (for the parent) — not per subtask.
func TestIntegration_Phase2_CancelParentCascades(t *testing.T) {
	api := &fakeAPIForIntegration{
		users: map[string]*model.User{
			"u-creator": {Id: "u-creator", Username: "creator", Locale: "en"},
			"u-doer":    {Id: "u-doer", Username: "doer", Locale: "en"},
		},
	}
	notifier := newIntegrationNotifier(api)
	store := newFakeTaskStore()
	svc := task.NewService(store)

	parent, err := svc.Create(task.CreateInput{
		Summary: "Epic", CreatorID: "u-creator", AssigneeID: "u-doer",
	})
	require.NoError(t, err)
	open, err := svc.CreateSubtask(parent.ID, "u-creator", "open part", "", nil)
	require.NoError(t, err)
	done, err := svc.CreateSubtask(parent.ID, "u-creator", "done part", "", nil)
	require.NoError(t, err)
	_, err = svc.SetStatus(done.ID, taskmodel.StatusDone)
	require.NoError(t, err)

	// Cancel the parent: the open subtask is cascade-cancelled, the done one untouched.
	cancelled, err := svc.SetStatus(parent.ID, taskmodel.StatusCancelled)
	require.NoError(t, err)
	assert.Equal(t, taskmodel.StatusCancelled, cancelled.Status)
	openGot, gerr := svc.Get(open.ID)
	require.NoError(t, gerr)
	assert.Equal(t, taskmodel.StatusCancelled, openGot.Status, "open subtask cascade-cancelled")
	doneGot, derr := svc.Get(done.ID)
	require.NoError(t, derr)
	assert.Equal(t, taskmodel.StatusDone, doneGot.Status, "already-done subtask untouched")

	// Notify-once contract: only the parent cancellation fires the DM (the service
	// cascade is silent). The caller (here the test) notifies once.
	api.dms = nil
	notifier.NotifyCancelled(notification.TaskSummary{ID: parent.ID, Summary: parent.Summary},
		"u-creator", parent.CreatorID, parent.AssigneeID)
	require.Len(t, api.dms, 1, "participants notified once for the parent cancellation")
	assert.Equal(t, "dm:u-doer:bot", api.dms[0].ChannelId, "only the non-actor participant is DM'd")
}

// TestIntegration_Phase2_CommentNotifiesParticipants covers "add comment and see
// participants notified": a new comment DMs the creator + assignee, excluding
// the commenter.
func TestIntegration_Phase2_CommentNotifiesParticipants(t *testing.T) {
	api := &fakeAPIForIntegration{
		users: map[string]*model.User{
			"u-creator": {Id: "u-creator", Username: "creator", Locale: "en"},
			"u-doer":    {Id: "u-doer", Username: "doer", Locale: "vi"},
		},
	}
	notifier := newIntegrationNotifier(api)
	store := newFakeTaskStore()
	svc := task.NewService(store)

	task1, err := svc.Create(task.CreateInput{
		Summary: "Task", CreatorID: "u-creator", AssigneeID: "u-doer",
	})
	require.NoError(t, err)

	// The creator comments; both participants (minus the commenter) are notified.
	_, ev, err := svc.AddComment(task1.ID, "u-creator", "looks good")
	require.NoError(t, err)
	notifier.NotifyCommented(notification.TaskSummary{ID: task1.ID, Summary: task1.Summary},
		ev.UserID, ev.CreatorID, ev.AssigneeID)
	require.Len(t, api.dms, 1, "only the assignee DM'd (commenter=creator excluded)")
	assert.Equal(t, "dm:u-doer:bot", api.dms[0].ChannelId)

	// The comments are visible in the list, in creation order. A second comment
	// (by the assignee this time) makes the ordering assertion non-vacuous.
	_, _, err = svc.AddComment(task1.ID, "u-doer", "ship it")
	require.NoError(t, err)
	comments, err := svc.ListComments(task1.ID)
	require.NoError(t, err)
	require.Len(t, comments, 2)
	assert.Equal(t, "looks good", comments[0].Content)
	assert.Equal(t, "ship it", comments[1].Content)
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
func (s *remStore) TouchTaskUpdatedAt(id string, updatedAt int64) error {
	t, ok := s.tasks[id]
	if !ok {
		return kvstore.ErrTaskNotFound
	}
	t.UpdatedAt = updatedAt
	return nil
}
func (s *remStore) DeleteTask(id string) error { delete(s.tasks, id); return nil }
func (s *remStore) SaveIndex(key string) error { s.indexes[key] = struct{}{}; return nil }

func (s *remStore) DeleteIndex(key string) error              { delete(s.indexes, key); return nil }
func (s *remStore) SaveSubtask(parentID, taskID string) error { return nil }
func (s *remStore) GetSubtaskIDs(parentID string) ([]string, error) {
	return nil, nil
}
func (s *remStore) SaveComment(taskID string, c taskmodel.Comment) error { return nil }
func (s *remStore) GetComment(taskID, commentID string) (*taskmodel.Comment, error) {
	return nil, nil
}
func (s *remStore) GetCommentIDs(taskID string) ([]string, error) { return nil, nil }
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
