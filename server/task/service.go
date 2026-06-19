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
		ChannelID:      in.ChannelID,
		CreatorID:      in.CreatorID,
		AssigneeID:     in.AssigneeID,
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

	task.UpdatedAt = nowFunc()
	if err := s.store.SaveTask(*task); err != nil {
		return nil, err
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
