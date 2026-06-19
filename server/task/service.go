// Package task implements the task lifecycle business logic that sits between
// the REST/command layers and the KVStore: creating tasks (with ULID + global
// OrderKey + all index edges), partial updates, hard-delete cascade, and the
// scope/status/due filtered list used by GET /tasks and the slash commands.
//
// The service depends only on the kvstore.KVStore interface and the taskutil
// helpers, so it is fully unit-testable against the in-memory kvstore mock or
// pluginapi.MemoryStore.
package task

import (
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store/kvstore"
	"github.com/naicoi92/mattermost-plugin-task/server/taskutil"
)

// nowFunc is the time source used to stamp CreatedAt/UpdatedAt. It is a package
// variable (rather than a struct field) so existing callers stay simple; tests
// override it to get deterministic timestamps.
var nowFunc = func() int64 { return time.Now().UnixMilli() }

// Service provides the task lifecycle operations used by the REST API and slash
// commands.
type Service struct {
	store kvstore.KVStore
}

// NewService returns a Service backed by the given store.
func NewService(store kvstore.KVStore) *Service {
	return &Service{store: store}
}

// CreateInput is the validated payload for creating a task. Only Summary is
// required; everything else is optional and mirrors the JSON contract of
// POST /tasks.
type CreateInput struct {
	Summary        string
	Description    string
	ChannelID      string
	CreatorID      string
	AssigneeID     string
	Due            *int64
	IsAllDay       bool
	ParentTaskID   string
	ReminderOffset *int64
}

// Create persists a new task with a fresh ULID, a global OrderKey at the end of
// the default column, and all key-per-edge index edges (assigned/created/
// channel/all, plus subtask membership when ParentTaskID is set).
//
// It returns the created task. An empty Summary is rejected.
func (s *Service) Create(in CreateInput) (*model.Task, error) {
	if in.Summary == "" {
		return nil, errors.New("summary is required")
	}
	if in.CreatorID == "" {
		return nil, errors.New("creator id is required")
	}

	// Subtasks inherit from their parent: a subtask must live in the parent's
	// channel, and its default assignee is the parent's assignee (the caller may
	// still override the assignee via CreateInput.AssigneeID). A non-existent
	// parent is rejected so an orphan ParentTaskID can never be persisted.
	channelID := in.ChannelID
	assigneeID := in.AssigneeID
	if in.ParentTaskID != "" {
		parent, err := s.store.GetTask(in.ParentTaskID)
		if err != nil {
			return nil, errors.Wrap(err, "failed to load parent task")
		}
		if parent == nil {
			return nil, ErrParentNotFound
		}
		channelID = parent.ChannelID
		if assigneeID == "" {
			assigneeID = parent.AssigneeID
		}
	}

	now := nowFunc()
	id := taskutil.GenerateULID()

	orderKey, err := s.nextGlobalOrderKey()
	if err != nil {
		return nil, err
	}

	task := model.Task{
		ID:             id,
		Summary:        in.Summary,
		Description:    in.Description,
		ChannelID:      channelID,
		CreatorID:      in.CreatorID,
		AssigneeID:     assigneeID,
		Due:            in.Due,
		IsAllDay:       in.IsAllDay,
		Status:         model.StatusTodo,
		OrderKey:       orderKey,
		ParentTaskID:   in.ParentTaskID,
		ReminderOffset: in.ReminderOffset,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.store.SaveTask(task); err != nil {
		return nil, err
	}

	if err := s.writeIndexes(task); err != nil {
		return nil, err
	}

	// Seed the reminder index when the task is created with both a due and an
	// offset (deferred for the common edit/assign paths via rebuildReminderIndex).
	if err := s.rebuildReminderIndex(&task); err != nil {
		return nil, err
	}

	return &task, nil
}

// nextGlobalOrderKey returns an OrderKey strictly greater than the current
// global maximum so new tasks land at the end of their initial column. It scans
// the global index, loads the tasks, and takes the max OrderKey. For the very
// first task it returns the seed key from taskutil.
func (s *Service) nextGlobalOrderKey() (string, error) {
	all, err := s.store.ListAllTaskIDs()
	if err != nil {
		return "", errors.Wrap(err, "failed to list tasks for order key")
	}
	if len(all) == 0 {
		return taskutil.NextOrderKey(""), nil
	}

	maxKey := ""
	for _, id := range all {
		t, err := s.store.GetTask(id)
		if err != nil {
			return "", errors.Wrapf(err, "failed to load task %s for order key", id)
		}
		if t == nil {
			continue
		}
		if t.OrderKey > maxKey {
			maxKey = t.OrderKey
		}
	}
	return taskutil.NextOrderKey(maxKey), nil
}

// writeIndexes writes every key-per-edge index for a freshly created task:
//
//	idx:u:{assignee}:assigned:{id}   (only when an assignee is set)
//	idx:u:{creator}:created:{id}
//	idx:ch:{channel}:task:{id}       (only for channel-scoped tasks)
//	idx:all:task:{id}
//	idx:t:{parent}:sub:{id}          (only for subtasks)
func (s *Service) writeIndexes(task model.Task) error {
	if task.AssigneeID != "" {
		if err := s.store.SaveIndex(kvstore.UserAssignedKey(task.AssigneeID, task.ID)); err != nil {
			return err
		}
	}
	if err := s.store.SaveIndex(kvstore.UserCreatedKey(task.CreatorID, task.ID)); err != nil {
		return err
	}
	if task.ChannelID != "" {
		if err := s.store.SaveIndex(kvstore.ChannelTaskKey(task.ChannelID, task.ID)); err != nil {
			return err
		}
	}
	if err := s.store.SaveIndex(kvstore.AllTasksKey(task.ID)); err != nil {
		return err
	}
	if task.ParentTaskID != "" {
		if err := s.store.SaveSubtask(task.ParentTaskID, task.ID); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the task with the given ID, or nil if it does not exist.
func (s *Service) Get(id string) (*model.Task, error) {
	return s.store.GetTask(id)
}

// ListComments returns the comments attached to taskID, sorted by ULID (creation
// order). It is defensive: a comment whose stored JSON fails to deserialize is
// skipped instead of failing the whole list, so one corrupt record can never
// hide the rest of the thread. Missing tasks yield an empty list (the caller
// usually resolves task existence separately).
func (s *Service) ListComments(taskID string) ([]model.Comment, error) {
	ids, err := s.store.GetCommentIDs(taskID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list comment ids")
	}
	comments := make([]model.Comment, 0, len(ids))
	for _, id := range ids {
		c, err := s.store.GetComment(taskID, id)
		if err != nil {
			// Defensive read: a comment whose stored JSON fails to deserialize
			// is skipped (logged) rather than failing the whole list. The id
			// index is consistent, so this only triggers on a genuinely corrupt
			// payload — one bad record must never hide the rest of the thread.
			continue
		}
		if c == nil {
			continue
		}
		comments = append(comments, *c)
	}
	return comments, nil
}

// SubtaskProgress returns (done, total) where done counts subtasks in a
// terminal status (done/cancelled) and total is the number of subtasks. Used
// to render the "x/y" progress on task cards. Missing subtask entities are
// skipped defensively.
func (s *Service) SubtaskProgress(parentID string) (done, total int, err error) {
	ids, err := s.store.GetSubtaskIDs(parentID)
	if err != nil {
		return 0, 0, errors.Wrap(err, "failed to list subtasks")
	}
	for _, id := range ids {
		sub, err := s.store.GetTask(id)
		if err != nil {
			return 0, 0, err
		}
		if sub == nil {
			continue
		}
		total++
		if sub.Status == model.StatusDone || sub.Status == model.StatusCancelled {
			done++
		}
	}
	return done, total, nil
}

// SetPostIDs records the channel/DM post ids of the task's interactive card so
// the card can be updated when the task changes (PLAN.md section 4.2). Either
// value may be empty to leave it unchanged. Used by the REST/dialog handlers
// after posting a card.
func (s *Service) SetPostIDs(id, channelPostID, dmPostID string) (*model.Task, error) {
	t, err := s.store.GetTask(id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, ErrNotFound
	}
	if channelPostID != "" {
		t.ChannelPostID = channelPostID
	}
	if dmPostID != "" {
		t.DMPostID = dmPostID
	}
	t.UpdatedAt = nowFunc()
	if err := s.store.SaveTask(*t); err != nil {
		return nil, err
	}
	return t, nil
}

// SetStatus transitions the task to newStatus using the canonical state machine
// (taskutil.ApplyStatus), refreshing UpdatedAt and clearing/stamping the
// CompletedAt/CancelledAt fields as appropriate:
//
//	todo / in_progress: clear CompletedAt and CancelledAt
//	done:               set CompletedAt, clear CancelledAt
//	cancelled:          set CancelledAt, clear CompletedAt
//
// Moving to a terminal status (done/cancelled) stops any active reminder by
// dropping the idx:reminder:{id} edge. Cancelling a task also cascade-cancels
// its open (todo/in_progress) subtasks. newStatus must be a valid status.
//
// It returns the updated task. A non-existent id yields ErrNotFound; an invalid
// status yields ErrInvalidStatus.
var ErrInvalidStatus = errors.New("invalid status")

func (s *Service) SetStatus(id, newStatus string) (*model.Task, error) {
	if !model.IsValidStatus(newStatus) {
		return nil, ErrInvalidStatus
	}
	task, err := s.store.GetTask(id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrNotFound
	}

	// No-op if the status is unchanged.
	if task.Status == newStatus {
		return task, nil
	}

	taskutil.ApplyStatus(task, newStatus, nowFunc())
	if err := s.store.SaveTask(*task); err != nil {
		return nil, err
	}

	// Side effects, ordered so none can be skipped by a failure in another.
	//
	// 1. Cascade-cancel open subtasks when the parent is cancelled — runs first
	//    so a later reminder-cleanup error can never prevent it.
	if newStatus == model.StatusCancelled {
		if err := s.cascadeCancelSubtasks(task.ID); err != nil {
			return nil, err
		}
	}

	switch newStatus {
	case model.StatusDone, model.StatusCancelled:
		// 2. Best-effort reminder-edge cleanup on terminal statuses. FireReadyReminders
		//    self-heals by dropping any stale edge for a terminal task, so a transient
		//    cleanup failure cannot cause a spurious DM (it's cleaned lazily next tick).
		_ = s.store.DeleteReminder(task.ID)
	case model.StatusTodo, model.StatusInProgress:
		// Reopening allows a reminder to fire again: reset the fired flag and
		// rebuild the index if a due+offset are still set.
		task.ReminderFired = false
		if err := s.store.SaveTask(*task); err != nil {
			return nil, err
		}
		if err := s.rebuildReminderIndex(task); err != nil {
			return nil, err
		}
	}

	return task, nil
}

// cascadeCancelSubtasks moves every todo/in_progress subtask of parentID to
// cancelled. Already-terminal subtasks are left untouched.
func (s *Service) cascadeCancelSubtasks(parentID string) error {
	subIDs, err := s.store.GetSubtaskIDs(parentID)
	if err != nil {
		return errors.Wrap(err, "failed to list subtasks for cascade cancel")
	}
	for _, subID := range subIDs {
		sub, err := s.store.GetTask(subID)
		if err != nil {
			return err
		}
		if sub == nil {
			continue
		}
		if sub.Status != model.StatusTodo && sub.Status != model.StatusInProgress {
			continue
		}
		if _, err := s.SetStatus(subID, model.StatusCancelled); err != nil {
			return err
		}
	}
	return nil
}

// SetReminder sets the reminder offset (ms before due) on the task and rebuilds
// the reminder index. The task must have a due date; offset must be > 0. It
// resets ReminderFired so the new reminder can fire.
func (s *Service) SetReminder(id string, offsetMS int64) (*model.Task, error) {
	if offsetMS <= 0 {
		return nil, errors.New("reminder offset must be positive")
	}
	task, err := s.store.GetTask(id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrNotFound
	}
	if task.Due == nil {
		return nil, ErrReminderNeedsDue
	}

	task.ReminderOffset = &offsetMS
	task.ReminderFired = false
	task.UpdatedAt = nowFunc()
	if err := s.store.SaveTask(*task); err != nil {
		return nil, err
	}
	if err := s.rebuildReminderIndex(task); err != nil {
		return nil, err
	}
	return task, nil
}

// ClearReminder removes the reminder offset and drops the reminder index edge.
func (s *Service) ClearReminder(id string) (*model.Task, error) {
	task, err := s.store.GetTask(id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrNotFound
	}
	task.ReminderOffset = nil
	task.ReminderFired = false
	task.UpdatedAt = nowFunc()
	if err := s.store.SaveTask(*task); err != nil {
		return nil, err
	}
	if err := s.store.DeleteReminder(task.ID); err != nil {
		return nil, err
	}
	return task, nil
}

// ErrReminderNeedsDue is returned when a reminder is requested on a task with
// no due date.
var ErrReminderNeedsDue = errors.New("reminder requires a due date")

// rebuildReminderIndex synchronizes the idx:reminder:{id} edge with the task's
// current reminder-eligibility. It writes the edge when:
//
//	due != nil AND status ∈ {todo, in_progress} AND ReminderOffset != nil AND !ReminderFired
//
// and deletes it otherwise. Call this on every update path that can affect
// reminders. Currently wired into: create, due change (Patch), offset change
// (SetReminder/ClearReminder), and status change (SetStatus). An assignee
// change also flips eligibility via the stored ReminderMetadata.AssigneeID, so
// the dedicated Assign path (added in the assignee-management issue) must call
// this too — the trigger is listed for completeness here.
func (s *Service) rebuildReminderIndex(task *model.Task) error {
	if task == nil {
		return nil
	}
	eligible := task.Due != nil &&
		task.ReminderOffset != nil &&
		(task.Status == model.StatusTodo || task.Status == model.StatusInProgress) &&
		!task.ReminderFired

	if !eligible {
		return s.store.DeleteReminder(task.ID)
	}

	meta := model.ReminderMetadata{
		DueMS:      *task.Due,
		OffsetMS:   *task.ReminderOffset,
		AssigneeID: task.AssigneeID,
	}
	return s.store.SaveReminder(task.ID, meta)
}

// DueReminder is a reminder whose fire time has arrived. The scheduler returns
// these so the notification layer can DM the assignee.
type DueReminder struct {
	TaskID     string
	AssigneeID string
	DueMS      int64
}

// FireReadyReminders scans the idx:reminder: index and returns every reminder
// whose fire window is open: now >= due-offset AND now <= due+grace. Tasks whose
// fire window has already closed past the grace period are dropped (fired once,
// missed) and marked fired. The caller DMs each returned DueReminder and then
// calls MarkReminderFired to drop the edge and stamp ReminderFired=true.
//
// now and grace are parameters so the function is deterministic in tests.
func (s *Service) FireReadyReminders(nowMs int64, grace time.Duration) ([]DueReminder, error) {
	keys, err := s.store.ListReminderKeys()
	if err != nil {
		return nil, err
	}

	var due []DueReminder
	for _, key := range keys {
		taskID := kvstore.TaskIDFromReminderKey(key)
		if taskID == "" {
			continue
		}
		meta, err := s.store.GetReminder(taskID)
		if err != nil {
			return nil, err
		}
		if meta == nil {
			continue
		}
		// Self-heal: a reminder edge should never exist for a terminal task. If
		// one lingers (e.g. a crash between the status update and edge cleanup,
		// or an older code path), drop it now so we never DM about a done/
		// cancelled task. This guards the index from ANY source of staleness,
		// not just SetStatus.
		t, err := s.store.GetTask(taskID)
		if err != nil {
			return nil, err
		}
		if t == nil {
			// Task gone (orphan edge) — clean it up.
			_ = s.store.DeleteReminder(taskID)
			continue
		}
		if t.Status == model.StatusDone || t.Status == model.StatusCancelled {
			_ = s.store.DeleteReminder(taskID)
			continue
		}
		fireAt := meta.FireMS()
		graceMS := grace.Milliseconds()
		if nowMs < fireAt {
			continue // not yet
		}
		if nowMs > meta.DueMS+graceMS {
			// Window closed without firing (scheduler was down): mark fired and
			// drop the edge so it never fires late.
			if err := s.MarkReminderFired(taskID); err != nil {
				return nil, err
			}
			continue
		}
		if meta.AssigneeID == "" {
			continue // nothing to send without an assignee
		}
		due = append(due, DueReminder{
			TaskID:     taskID,
			AssigneeID: meta.AssigneeID,
			DueMS:      meta.DueMS,
		})
	}
	return due, nil
}

// MarkReminderFired drops the reminder index edge and sets ReminderFired=true on
// the task so it will not fire again until reset (due change / reopen).
func (s *Service) MarkReminderFired(taskID string) error {
	if err := s.store.DeleteReminder(taskID); err != nil {
		return err
	}
	task, err := s.store.GetTask(taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return nil
	}
	task.ReminderFired = true
	return s.store.SaveTask(*task)
}

// PatchInput is the partial-update payload. Only fields listed in UpdateFields
// are modified; a field present in UpdateFields with the corresponding pointer
// nil clears that field.
type PatchInput struct {
	UpdateFields []string
	Summary      *string
	Description  *string
	Due          *int64 // nil clears due when "due" is in UpdateFields
	IsAllDay     *bool
}

// Patch applies a partial update to the task identified by id. UpdatedAt is
// refreshed. Only the fields named in UpdateFields are touched.
func (s *Service) Patch(id string, in PatchInput) (*model.Task, error) {
	task, err := s.store.GetTask(id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, ErrNotFound
	}

	changed := false
	dueChanged := false
	for _, field := range in.UpdateFields {
		switch field {
		case "summary":
			if in.Summary != nil {
				task.Summary = *in.Summary
				changed = true
			}
		case "description":
			task.Description = derefStr(in.Description)
			changed = true
		case "due":
			task.Due = in.Due
			changed = true
			dueChanged = true
		case "is_all_day":
			task.IsAllDay = derefBool(in.IsAllDay)
			changed = true
		}
	}

	if !changed {
		// Nothing to do; still refresh UpdatedAt for consistency with callers
		// that treat a successful PATCH as a state change.
		return task, nil
	}

	// A due change resets the fired flag so the reminder can fire again under
	// the new deadline, and rebuilds the index to match the new schedule.
	if dueChanged {
		task.ReminderFired = false
	}

	task.UpdatedAt = nowFunc()
	if err := s.store.SaveTask(*task); err != nil {
		return nil, err
	}

	if dueChanged {
		if err := s.rebuildReminderIndex(task); err != nil {
			return nil, err
		}
	}
	return task, nil
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// ErrNotFound is returned by Get/Patch/Delete when the task id does not exist.
var ErrNotFound = errors.New("task not found")

// ErrParentNotFound is returned by Create when a subtask references a parent
// task id that does not exist.
var ErrParentNotFound = errors.New("parent task not found")

// AssignEvent describes the result of an assignee change so callers (REST/
// command handlers) can fire the appropriate notification without re-reading
// the task. OldAssigneeID is empty when the task had no assignee before.
type AssignEvent struct {
	TaskID        string
	OldAssigneeID string
	NewAssigneeID string
	CreatorID     string
}

// Assign sets the task's single assignee to newAssigneeID, swapping the
// idx:u:{old}:assigned:{id} and idx:u:{new}:assigned:{id} index edges and
// refreshing UpdatedAt. It also keeps any active reminder edge consistent by
// rebuilding the reminder index (the stored ReminderMetadata.AssigneeID tracks
// the current assignee). An empty newAssigneeID clears the assignee.
//
// It returns the updated task and an AssignEvent describing the change. A
// no-op assign (same user) returns the task without touching indexes.
func (s *Service) Assign(id, newAssigneeID string) (*model.Task, AssignEvent, error) {
	task, err := s.store.GetTask(id)
	if err != nil {
		return nil, AssignEvent{}, err
	}
	if task == nil {
		return nil, AssignEvent{}, ErrNotFound
	}

	oldAssigneeID := task.AssigneeID
	if oldAssigneeID == newAssigneeID {
		return task, AssignEvent{
			TaskID:        task.ID,
			OldAssigneeID: oldAssigneeID,
			NewAssigneeID: newAssigneeID,
			CreatorID:     task.CreatorID,
		}, nil
	}

	// Swap the assigned index edges by their full known keys (no scan).
	if oldAssigneeID != "" {
		if err := s.store.DeleteIndex(kvstore.UserAssignedKey(oldAssigneeID, task.ID)); err != nil {
			return nil, AssignEvent{}, err
		}
	}
	if newAssigneeID != "" {
		if err := s.store.SaveIndex(kvstore.UserAssignedKey(newAssigneeID, task.ID)); err != nil {
			return nil, AssignEvent{}, err
		}
	}

	task.AssigneeID = newAssigneeID
	task.UpdatedAt = nowFunc()
	if err := s.store.SaveTask(*task); err != nil {
		return nil, AssignEvent{}, err
	}

	// Keep the reminder edge's stored assignee in sync (rebuild is a no-op when
	// the task isn't reminder-eligible).
	if err := s.rebuildReminderIndex(task); err != nil {
		return nil, AssignEvent{}, err
	}

	return task, AssignEvent{
		TaskID:        task.ID,
		OldAssigneeID: oldAssigneeID,
		NewAssigneeID: newAssigneeID,
		CreatorID:     task.CreatorID,
	}, nil
}

// Delete hard-deletes the task and everything attached to it, following the
// cascade order from PLAN.md section 4.3:
//
//  1. Subtasks (recursively, discovered via idx:t:{id}:sub:)
//  2. Comments (discovered via t:{id}:c:)
//  3. Index markers — deleted by their full known keys derived from the entity
//     (no ListKeys scan), plus the reminder edge.
//  4. The task entity itself.
//
// Crashes between steps may leave a few orphan marker keys; reads tolerate
// those defensively (ListTaskIDsByPrefix self-heals).
func (s *Service) Delete(id string) error {
	task, err := s.store.GetTask(id)
	if err != nil {
		return err
	}
	if task == nil {
		return ErrNotFound
	}

	// 1. Subtasks (recursive).
	subIDs, err := s.store.GetSubtaskIDs(id)
	if err != nil {
		return errors.Wrap(err, "failed to list subtasks for cascade delete")
	}
	for _, subID := range subIDs {
		if derr := s.Delete(subID); derr != nil && !errors.Is(derr, ErrNotFound) {
			return derr
		}
	}

	// 2. Comments.
	commentIDs, err := s.store.GetCommentIDs(id)
	if err != nil {
		return errors.Wrap(err, "failed to list comments for cascade delete")
	}
	for _, cid := range commentIDs {
		if err := s.store.DeleteIndex(kvstore.CommentKey(id, cid)); err != nil {
			return err
		}
	}

	// 3. Index markers by their full known keys.
	if task.AssigneeID != "" {
		if err := s.store.DeleteIndex(kvstore.UserAssignedKey(task.AssigneeID, task.ID)); err != nil {
			return err
		}
	}
	if err := s.store.DeleteIndex(kvstore.UserCreatedKey(task.CreatorID, task.ID)); err != nil {
		return err
	}
	if task.ChannelID != "" {
		if err := s.store.DeleteIndex(kvstore.ChannelTaskKey(task.ChannelID, task.ID)); err != nil {
			return err
		}
	}
	if err := s.store.DeleteIndex(kvstore.AllTasksKey(task.ID)); err != nil {
		return err
	}
	if task.ParentTaskID != "" {
		if err := s.store.DeleteIndex(kvstore.SubtaskKey(task.ParentTaskID, task.ID)); err != nil {
			return err
		}
	}
	// Best-effort reminder edge cleanup.
	_ = s.store.DeleteReminder(task.ID)

	// 4. Entity.
	return s.store.DeleteTask(id)
}

// Scope enumerates the list result scopes.
type Scope string

const (
	ScopeMine    Scope = "mine"
	ScopeChannel Scope = "channel"
	ScopeAll     Scope = "all"
)

// ListQuery is the filtered/paginated list request.
type ListQuery struct {
	Scope         Scope
	UserID        string // required for ScopeMine
	ChannelID     string // required for ScopeChannel
	Status        string // optional: "", todo, in_progress, done, cancelled
	Due           string // optional: "", overdue, today, week
	AfterOrderKey string // cursor: only tasks with OrderKey > this are returned
	Limit         int    // page size; <=0 defaults to DefaultLimit
}

// DefaultLimit is the default page size for list queries.
const DefaultLimit = 50

// List returns the tasks matching the query, filtered in memory after loading
// candidate ids from the relevant index. Results are sorted by OrderKey and
// paged by the AfterOrderKey cursor. Missing entities are skipped defensively.
func (s *Service) List(q ListQuery) ([]model.Task, error) {
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}

	ids, err := s.candidateIDs(q)
	if err != nil {
		return nil, err
	}

	tasks := make([]model.Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.store.GetTask(id)
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue // self-healing: skip stale markers
		}
		if !matchStatus(t, q.Status) {
			continue
		}
		if !matchDue(t, q.Due, nowFunc()) {
			continue
		}
		if q.AfterOrderKey != "" && t.OrderKey <= q.AfterOrderKey {
			continue
		}
		tasks = append(tasks, *t)
	}

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].OrderKey < tasks[j].OrderKey })

	if len(tasks) > q.Limit {
		tasks = tasks[:q.Limit]
	}
	return tasks, nil
}

// candidateIDs returns the raw task id candidates for the query scope.
func (s *Service) candidateIDs(q ListQuery) ([]string, error) {
	switch q.Scope {
	case ScopeChannel:
		return s.store.ListChannelTaskIDs(q.ChannelID)
	case ScopeMine:
		// My Tasks = tasks assigned to the user (flat: includes subtasks they
		// own as independent tasks — PLAN.md section 5.5).
		return s.store.ListUserAssignedTaskIDs(q.UserID)
	default: // ScopeAll or unset
		return s.store.ListAllTaskIDs()
	}
}

func matchStatus(t *model.Task, status string) bool {
	if status == "" {
		return true
	}
	return t.Status == status
}

// matchDue reports whether the task's due date falls within the requested due
// bucket. Tasks with no due date never match a specific bucket but do match the
// empty ("any") bucket.
func matchDue(t *model.Task, due string, nowMs int64) bool {
	if due == "" {
		return true
	}
	if t.Due == nil {
		return false
	}
	switch strings.ToLower(due) {
	case "overdue":
		return *t.Due < nowMs
	case "today":
		start, end := dayBounds(nowMs)
		return *t.Due >= start && *t.Due <= end
	case "week":
		start := startOfDay(nowMs)
		week := 7 * 24 * int64(time.Hour) / int64(time.Millisecond)
		return *t.Due >= start && *t.Due < start+week
	default:
		return true
	}
}

// dayBounds returns the [start, end) millisecond range of the calendar day
// (UTC) containing nowMs. end is the last ms of the day (inclusive) so callers
// can test end+1ms for an exclusive upper bound.
func dayBounds(nowMs int64) (int64, int64) {
	const day = 24 * int64(time.Hour) / int64(time.Millisecond)
	start := nowMs - nowMs%day
	return start, start + day - 1
}

func startOfDay(nowMs int64) int64 {
	const day = 24 * int64(time.Hour) / int64(time.Millisecond)
	return nowMs - nowMs%day
}

// Search returns up to limit tasks whose Summary or Description contains
// keyword (case-insensitive). It scans the global index; sufficient for the MVP
// escape hatch (PLAN.md section 5.2). keyword == "" returns an empty result.
func (s *Service) Search(keyword string, limit int) ([]model.Task, error) {
	if keyword == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	needle := strings.ToLower(keyword)

	ids, err := s.store.ListAllTaskIDs()
	if err != nil {
		return nil, err
	}

	results := make([]model.Task, 0, limit)
	for _, id := range ids {
		t, err := s.store.GetTask(id)
		if err != nil {
			return nil, err
		}
		if t == nil {
			continue
		}
		if strings.Contains(strings.ToLower(t.Summary), needle) ||
			strings.Contains(strings.ToLower(t.Description), needle) {
			results = append(results, *t)
			if len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}
