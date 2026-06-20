// Package store defines the persistence contracts and shared query types for
// the Task plugin's relational store.
//
// This file holds the cross-cutting types used by every repository: the
// ListQuery filter/pagination input, the PageResult envelope, and the Scope
// constants that name the list "views" (mine / channel / all). Repository
// methods live in the sqlstore package and operate on these types; the
// aggregate Store interface is assembled in M3-1.
package store

// Scope names a task-list "view". It selects which WHERE clause ListTasks
// applies: a user's assigned tasks, a channel's tasks, or everything.
type Scope string

const (
	// ScopeMine lists tasks assigned to a given user (JOIN task_members with
	// role='assignee'). UserID must be set on the ListQuery.
	ScopeMine Scope = "mine"
	// ScopeChannel lists tasks scoped to a channel. ChannelID must be set on
	// the ListQuery.
	ScopeChannel Scope = "channel"
	// ScopeAll lists every task regardless of scope. Used by admin/search
	// views; no extra join or WHERE is applied.
	ScopeAll Scope = "all"
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
// Scope is the only required field; it selects the list view (mine/channel/
// all). The other fields are optional filters — the repository composes only
// the WHERE clauses the populated fields imply. A ListQuery with
// Scope=ScopeAll and nothing else is the "list all" request.
type ListQuery struct {
	// Scope selects the list view (mine/channel/all). Required.
	Scope Scope
	// UserID is required when Scope == ScopeMine (the assignee to filter on).
	UserID string
	// ChannelID is required when Scope == ScopeChannel.
	ChannelID string
	// Status, when non-empty, restricts to that status value (todo/in_progress/
	// done/cancelled). Empty means "any status".
	Status string
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
// Items is []any so the store package does not depend on model.TaskView; the
// sqlstore layer fills it with concrete *model.TaskView values.
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
