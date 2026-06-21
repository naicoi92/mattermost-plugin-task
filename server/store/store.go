// Package store defines the persistence contracts and shared query types for
// the Task plugin's relational store.
//
// This file holds the cross-cutting types used by every repository (the
// ListQuery filter/pagination input, the PageResult envelope, the Scope
// constants) AND the aggregate Store interface that assembles every repository
// method from M2-1..M2-6 plus the WithTx transaction primitive. The concrete
// implementation lives in the sqlstore package; the service layer (M3-2)
// programs against this interface so it can be faked in tests.
package store

import (
	"context"
	"errors"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// Sentinel errors for the "not found" outcomes of single-row lookups. They live
// in the interface package (not sqlstore) so the service layer can check
// errors.Is against them without importing the concrete store — this keeps the
// dependency direction service -> store (interface), not service -> sqlstore.
var (
	// ErrTaskNotFound is returned by GetTask/UpdateTask/DeleteTask when no task
	// row matches the given id.
	ErrTaskNotFound = errors.New("task not found")
	// ErrMemberNotFound is returned by GetMemberByRole when no membership edge
	// matches (task, role).
	ErrMemberNotFound = errors.New("task member not found")
	// ErrReminderNotFound is returned by MarkReminderFired when no reminder row
	// matches the given reminder id.
	ErrReminderNotFound = errors.New("task reminder not found")
	// ErrPostNotFound is returned by GetPostByKind/DeletePost when no tracked
	// post matches.
	ErrPostNotFound = errors.New("task post not found")
	// ErrCommentNotFound is returned by UnlinkComment when no comment mapping
	// matches the given post id.
	ErrCommentNotFound = errors.New("task comment not found")
)

// Scope names a task-list "view". It selects which WHERE clause ListTasks
// applies. Two scopes are supported:
//   - channel: tasks that belong to a specific channel (ChannelID).
//   - direct:  tasks shared between two DM participants (UserID + PartnerID),
//     i.e. tasks on which either user holds the assignee or creator role.
//
// The earlier "mine" (tasks assigned to a user) and "all" (every task in the
// DB) scopes were removed when the slash-command and mobile-dialog paths were
// dropped; the desktop RHS derives the scope from the current channel type
// (channel vs DM) and never needs a cross-context "my tasks" view.
type Scope string

const (
	// ScopeChannel lists tasks scoped to a channel. ChannelID must be set on
	// the ListQuery.
	ScopeChannel Scope = "channel"
	// ScopeDirect lists tasks shared between two DM users: every task on which
	// either UserID or PartnerID is a member with the assignee or creator role
	// (JOIN task_members WHERE user_id IN (UserID, PartnerID) AND role IN
	// ('assignee','creator')). UserID and PartnerID must be set on the
	// ListQuery.
	ScopeDirect Scope = "direct"
)

// DueFilter narrows ListTasks results by due-date bucket. Each value maps to
// a WHERE clause on due_at so the filtering is pushed to the database rather
// than applied in Go (the KV store's core scalability problem).
type DueFilter string

const (
	// DueAny applies no due-date filter.
	DueAny DueFilter = ""
	// DueOverdue matches tasks whose due_at is in the past and still pending
	// (status not done/cancelled).
	DueOverdue DueFilter = "overdue"
	// DueToday matches tasks due in the current UTC day.
	DueToday DueFilter = "today"
	// DueWeek matches tasks due within the next 7 days.
	DueWeek DueFilter = "week"
)

// ListQuery is the filter + pagination input for ListTasks / CountTasksByStatus.
//
// Scope selects the list view (channel/direct) and is required. The other
// fields are optional filters — the repository composes only the WHERE clauses
// the populated fields imply.
type ListQuery struct {
	// Scope selects the list view (channel/direct). Required.
	Scope Scope
	// UserID is the authenticated user. Required when Scope == ScopeDirect
	// (one of the two DM participants).
	UserID string
	// ChannelID is required when Scope == ScopeChannel.
	ChannelID string
	// PartnerID is the other DM participant and is required when Scope ==
	// ScopeDirect. The result set is the union of tasks on which either UserID
	// or PartnerID holds the assignee or creator role.
	PartnerID string
	// Status, when non-empty, restricts to that status value (todo/in_progress/
	// done/cancelled). Empty means "any status".
	Status string
	// Priority, when non-empty, restricts to that priority value
	// (standard/important/urgent). Empty means "any priority".
	Priority string
	// Due filters by due-date bucket. DueAny means no due filter.
	Due DueFilter
	// DueAsOf is the reference timestamp (ms UTC) used to evaluate DueToday /
	// DueWeek / DueOverdue boundaries. Callers pass time.Now().UnixMilli().
	DueAsOf int64
	// AfterOrderKey is the cursor for keyset pagination: ListTasks returns
	// rows whose order_key is strictly greater than this value. Empty means
	// "from the start".
	AfterOrderKey string
	// Limit caps the page size. The repository fetches Limit+1 rows to compute
	// HasMore without a second round-trip (unless Total is also requested).
	Limit int
}

// PageResult is the envelope returned by ListTasks: a page of items, the total
// count across the whole filtered set, and whether another page follows.
// Items is []any so the store package does not depend on model.Task; the
// sqlstore layer fills it with concrete *model.Task values.
type PageResult struct {
	// Items is the current page; callers type-assert to the concrete repo type.
	Items []any
	// Total is the full filtered row count (COUNT(*) with the same WHERE),
	// used for "1–20 of 342" UI. It is computed in a second query; callers
	// that only need a page may ignore it.
	Total int
	// HasMore is true when at least one more row exists after this page
	// (detected by fetching Limit+1 rows).
	HasMore bool
}

// Store is the aggregate persistence interface for the Task plugin, assembling
// every repository method from M2-1..M2-6 plus the WithTx transaction
// primitive. The service layer (M3-2) programs against this interface so it
// can be faked in tests without a database.
//
// Every method takes context.Context first so requests respect cancellation /
// deadlines. Methods that mutate multiple tables in one logical operation
// (e.g. Create = task + members + reminder + event) are composed by the
// service inside WithTx so the whole operation commits or rolls back together.
//
// (Note: this is the SQL-store interface. It is deliberately separate from the
// legacy kvstore.KVStore interface until M5-1 removes the KV layer.)
type Store interface {
	// --- Tasks (M2-1) ---
	CreateTask(ctx context.Context, task model.TaskRow) (model.TaskRow, error)
	GetTask(ctx context.Context, id string) (*model.TaskRow, error)
	UpdateTask(ctx context.Context, task model.TaskRow) (model.TaskRow, error)
	TouchTaskUpdatedAt(ctx context.Context, id string, ts int64) error
	DeleteTask(ctx context.Context, id string) error
	ListTasks(ctx context.Context, q ListQuery) (PageResult, error)
	CountTasksByStatus(ctx context.Context, q ListQuery) (map[string]int, error)
	SearchTasks(ctx context.Context, keyword string, limit int) ([]model.TaskRow, error)
	ListSubtasks(ctx context.Context, parentID string) ([]model.TaskRow, error)
	SubtaskProgress(ctx context.Context, parentID string) (done, total int, err error)
	NextGlobalOrderKey(ctx context.Context) (string, error)
	// ListAllTasksForTest returns every task row ordered by order_key, with no
	// scope/filter. Test-only helper: the production list path is scope-driven
	// (channel/direct), but tests need an unfiltered snapshot to assert fixtures.
	ListAllTasksForTest(ctx context.Context) ([]model.TaskRow, error)

	// --- Members (M2-2) ---
	AddMember(ctx context.Context, taskID, userID, role string) error
	RemoveMember(ctx context.Context, taskID, userID, role string) error
	ListMembers(ctx context.Context, taskID string) ([]model.TaskMember, error)
	GetMemberByRole(ctx context.Context, taskID, role string) (string, error)
	// SetAssignee replaces the assignee edge (role='assignee') for a task. If
	// no assignee edge exists yet, it inserts one. The caller resolves the old
	// assignee separately via GetMemberByRole and handles the no-op case.
	SetAssignee(ctx context.Context, taskID, newAssigneeID string) error

	// --- Reminders (M2-3) ---
	SetReminder(ctx context.Context, id, taskID string, offsetMS int64) (model.TaskReminder, error)
	ClearReminder(ctx context.Context, taskID string) error
	ListReminders(ctx context.Context, taskID string) ([]model.TaskReminder, error)
	ListDueReminders(ctx context.Context, nowMs, graceMs int64) ([]model.DueReminder, error)
	MarkReminderFired(ctx context.Context, reminderID string, firedAt int64) error

	// --- Posts (M2-4) ---
	AddPost(ctx context.Context, id, taskID, postID, kind string) error
	ListPosts(ctx context.Context, taskID string) ([]model.TaskPost, error)
	GetPostByKind(ctx context.Context, taskID, kind string) (string, error)
	// GetTaskIDByPost reverse-looks-up the task whose card is postID. Used by
	// the MessageHasBeenPosted hook to detect task-thread replies.
	GetTaskIDByPost(ctx context.Context, postID string) (string, error)
	DeletePost(ctx context.Context, postID string) error

	// --- Comments (M2-5) ---
	LinkComment(ctx context.Context, id, taskID, postID, authorID string, createdAt int64) (model.TaskComment, error)
	ListComments(ctx context.Context, taskID string) ([]model.TaskComment, error)
	CountComments(ctx context.Context, taskID string) (int, error)
	UnlinkComment(ctx context.Context, postID string) error

	// --- Events (M2-6) ---
	AppendTaskEvent(ctx context.Context, e model.TaskEvent) error
	ListTaskEvents(ctx context.Context, taskID string, limit int) ([]model.TaskEvent, error)

	// --- Transaction (M3-1) ---
	// WithTx runs fn against a transaction-bound Store: every statement fn
	// issues shares the same *sql.Tx, so a multi-table operation (e.g. Create
	// = task + members + reminder + event) commits atomically or rolls back
	// together. If fn returns an error the tx is rolled back and that error is
	// returned; otherwise the tx is committed.
	WithTx(ctx context.Context, fn func(Store) error) error
}
