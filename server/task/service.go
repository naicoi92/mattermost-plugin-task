// Package task implements the task lifecycle business logic that sits between
// the REST/command layers and the relational store. Each method composes one or
// more store.Store calls, wrapping multi-table mutations in WithTx so they
// commit or roll back atomically, and assembles the result into a model.Task
// (the denormalized entity consumers see) via assembleTask.
//
// The service depends only on the store.Store interface and the taskutil
// helpers, so it is fully unit-testable against an in-memory sqlite SQLStore.
package task

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
	"github.com/naicoi92/mattermost-plugin-task/server/taskutil"
)

// nowFunc is the time source used to stamp CreatedAt/UpdatedAt and to evaluate
// due-filter windows. It is a package variable so existing callers stay simple;
// tests override it to get deterministic timestamps.
var nowFunc = func() int64 { return time.Now().UnixMilli() }

// errorLogger is the minimal logging surface the service needs to report
// non-fatal store failures. The plugin wires it to pluginapi.Client; tests can
// inject a no-op. Keeping it local avoids importing pluginapi into this package.
type errorLogger interface {
	Error(message string, keyValuePairs ...any)
}

// Service provides the task lifecycle operations used by the REST API and slash
// commands.
type Service struct {
	store  store.Store
	logger errorLogger
}

// NewService returns a Service backed by the given relational store. An optional
// logger surfaces non-fatal store failures for debugging; nil disables logging.
func NewService(s store.Store, logger ...errorLogger) *Service {
	var l errorLogger
	if len(logger) > 0 {
		l = logger[0]
	}
	return &Service{store: s, logger: l}
}

// logUnexpected records a non-fatal error when a logger is configured; a no-op
// otherwise so callers don't need to nil-check.
func (s *Service) logUnexpected(msg string, err error) {
	if s.logger == nil || err == nil {
		return
	}
	s.logger.Error(msg, "error", err)
}

// ctx returns a context with a short timeout so service calls don't hang the
// request thread indefinitely. A bounded context keeps a wedged DB from piling
// up goroutines; callers that already hold a request context can thread it
// through the (future) ctx-accepting overloads.
func (s *Service) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// assembleTask denormalizes a TaskRow into the model.Task entity consumers see:
// it joins the members (creator/assignee), posts (channel/dm card ids),
// reminder, subtask progress, and comment count. Called on every read path so
// the flat REST shape stays populated even though storage is fully normalized.
//
// Best-effort on the relation reads: a missing member/post row leaves the
// corresponding field empty rather than failing the whole assembly, matching
// the legacy KV store's tolerance for partially-populated tasks.
func (s *Service) assembleTask(ctx context.Context, row *model.TaskRow) (*model.Task, error) {
	t := &model.Task{TaskRow: *row}

	// Creator + assignee (task_members). A "not found" is expected for tasks
	// with no assignee / tasks migrated before members were written; leave the
	// field empty rather than failing.
	if creator, err := s.store.GetMemberByRole(ctx, row.ID, model.MemberRoleCreator); err == nil {
		t.CreatorID = creator
	} else if !errors.Is(err, store.ErrMemberNotFound) {
		return nil, errors.Wrap(err, "assemble task: creator")
	}
	if assignee, err := s.store.GetMemberByRole(ctx, row.ID, model.MemberRoleAssignee); err == nil {
		t.AssigneeID = assignee
	} else if !errors.Is(err, store.ErrMemberNotFound) {
		return nil, errors.Wrap(err, "assemble task: assignee")
	}

	// Card post ids (task_posts).
	if ch, err := s.store.GetPostByKind(ctx, row.ID, model.PostKindChannel); err == nil {
		t.ChannelPostID = ch
	}
	if dm, err := s.store.GetPostByKind(ctx, row.ID, model.PostKindDM); err == nil {
		t.DMPostID = dm
	}

	// Reminder (task_reminders; at most one per task at MVP).
	if reminders, err := s.store.ListReminders(ctx, row.ID); err == nil && len(reminders) > 0 {
		t.ReminderOffset = &reminders[0].OffsetMS
		t.ReminderFired = reminders[0].FiredAt != nil
	}

	// Subtask progress + comment count aggregates.
	if done, total, err := s.store.SubtaskProgress(ctx, row.ID); err == nil {
		t.SubtaskDone, t.SubtaskTotal = done, total
	}
	if n, err := s.store.CountComments(ctx, row.ID); err == nil {
		t.CommentCount = n
	}

	return t, nil
}

// loadTaskRow fetches a task row and translates the store's
// store.ErrTaskNotFound into the service's ErrNotFound sentinel. Service
// methods call this instead of s.store.GetTask directly so not-found is a
// single, consistent error callers can errors.Is against.
func (s *Service) loadTaskRow(ctx context.Context, id string) (*model.TaskRow, error) {
	row, err := s.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if row == nil {
		return nil, ErrNotFound
	}
	return row, nil
}

// CreateInput is the validated payload for creating a task. Only Summary and
// CreatorID are required; everything else is optional and mirrors the JSON
// contract of POST /tasks.
type CreateInput struct {
	Summary        string
	Description    string
	ChannelID      string
	CreatorID      string
	AssigneeID     string
	DueAt          *int64
	IsAllDay       bool
	ParentTaskID   string
	ReminderOffset *int64
	// Priority is one of the model.Priority* constants. Empty defaults to
	// standard; an invalid value is rejected.
	Priority string
}

// ensurePriority normalizes an empty priority to the default. Tasks created
// before the priority migration (000008) are backfilled to 'standard' by the
// DEFAULT clause, but defensive reads can still surface an empty string in
// edge cases (e.g. a row cached before the migration ran). Calling this before
// any UpdateTask prevents a NOT NULL constraint violation.
func ensurePriority(p string) string {
	if p == "" {
		return model.PriorityStandard
	}
	return p
}

// Create persists a new task atomically: the task row, its creator + assignee
// member edges, an optional reminder, and a "created" audit event all commit
// together via WithTx. A subtask inherits its parent's channel and (as default)
// assignee; an explicit AssigneeID overrides the inherited default.
//
// It returns the assembled task. Empty Summary or CreatorID is rejected; a
// missing parent yields ErrParentNotFound; a cyclic ParentTaskID yields
// ErrSubtaskCycle.
func (s *Service) Create(in CreateInput) (*model.Task, error) {
	if in.Summary == "" {
		return nil, errors.New("summary is required")
	}
	if in.CreatorID == "" {
		return nil, errors.New("creator id is required")
	}

	// Normalize + validate priority. Empty defaults to standard (the implicit
	// default in the column); an explicit value must be one of the recognized
	// constants so the UI can always render it.
	priority := in.Priority
	if priority == "" {
		priority = model.PriorityStandard
	}
	if !model.IsValidPriority(priority) {
		return nil, errors.New("invalid priority")
	}

	// Resolve parent inheritance BEFORE the transaction so the tx body is a
	// tight write sequence; reads outside the tx are safe here because the
	// parent's channel/assignee are stable for the lifetime of the create.
	channelID := in.ChannelID
	assigneeID := in.AssigneeID
	if in.ParentTaskID != "" {
		ctx, cancel := s.ctx()
		defer cancel()
		parentRow, err := s.loadTaskRow(ctx, in.ParentTaskID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, ErrParentNotFound
			}
			return nil, errors.Wrap(err, "failed to load parent task")
		}
		if err := s.assertNoCycle(ctx, in.ParentTaskID, maxSubtaskDepth); err != nil {
			return nil, err
		}
		channelID = parentRow.ChannelID
		if assigneeID == "" {
			// Inherit the parent's assignee (best-effort; missing assignee
			// leaves the subtask unassigned).
			if aid, err := s.store.GetMemberByRole(ctx, in.ParentTaskID, model.MemberRoleAssignee); err == nil {
				assigneeID = aid
			}
		}
	}

	now := nowFunc()
	id := taskutil.GenerateULID()

	ctx, cancel := s.ctx()
	defer cancel()
	orderKey, orderErr := s.nextGlobalOrderKey(ctx)
	if orderErr != nil {
		return nil, orderErr
	}

	row := model.TaskRow{
		ID:           id,
		Summary:      in.Summary,
		Description:  in.Description,
		ChannelID:    channelID,
		ParentTaskID: in.ParentTaskID,
		Status:       model.StatusTodo,
		Priority:     priority,
		OrderKey:     orderKey,
		IsAllDay:     in.IsAllDay,
		DueAt:        in.DueAt,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	// Atomic write: task + members + (optional) reminder + audit event.
	txCtx, txCancel := s.ctx()
	defer txCancel()
	var created *model.Task
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		saved, saveErr := tx.CreateTask(txCtx, row)
		if saveErr != nil {
			return saveErr
		}
		row = saved

		if err := tx.AddMember(txCtx, row.ID, in.CreatorID, model.MemberRoleCreator); err != nil {
			return err
		}
		if assigneeID != "" {
			if err := tx.AddMember(txCtx, row.ID, assigneeID, model.MemberRoleAssignee); err != nil {
				return err
			}
		}

		// Seed the reminder when created with both a due and an offset.
		if row.DueAt != nil && in.ReminderOffset != nil && *in.ReminderOffset > 0 {
			if _, err := tx.SetReminder(txCtx, taskutil.GenerateULID(), row.ID, *in.ReminderOffset); err != nil {
				return err
			}
		}

		createdSnapshot := snapshotTaskJSON(&row)
		if err := tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    row.ID,
			ActorID:   in.CreatorID,
			EventType: model.EventCreated,
			CreatedAt: now,
			ToValue:   &createdSnapshot,
		}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	created, asmErr := s.assembleTask(ctx, &row)
	if asmErr != nil {
		return nil, asmErr
	}
	return created, nil
}

// nextGlobalOrderKey returns an OrderKey strictly greater than the current
// global maximum so new tasks land at the end of their initial column. A single
// SELECT MAX(order_key) replaces the KV store's ListAllTaskIDs + per-task Get
// loop.
func (s *Service) nextGlobalOrderKey(ctx context.Context) (string, error) {
	maxKey, err := s.store.NextGlobalOrderKey(ctx)
	if err != nil {
		return "", errors.Wrap(err, "failed to compute next order key")
	}
	return taskutil.NextOrderKey(maxKey), nil
}

// Get returns the assembled task with the given ID, or ErrNotFound if it does
// not exist.
func (s *Service) Get(id string) (*model.Task, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	row, err := s.loadTaskRow(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.assembleTask(ctx, row)
}

// ListComments returns the comment mappings attached to taskID, sorted by
// creation time. Content lives in the Mattermost post (PostID); the caller
// fetches it via GetPost and skips nil results defensively.
func (s *Service) ListComments(taskID string) ([]model.TaskComment, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.store.ListComments(ctx, taskID)
}

// ListTaskEvents returns the audit trail for a task, newest-first, capped at
// limit (<=0 defaults to 50). Powers the GET /tasks/:id/events timeline
// endpoint (M4-4).
func (s *Service) ListTaskEvents(taskID string, limit int) ([]model.TaskEvent, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.store.ListTaskEvents(ctx, taskID, limit)
}

// CommentEvent describes the result of linking a comment so callers (REST/
// command handlers) can fire the participant notification without re-reading
// the task.
type CommentEvent struct {
	TaskID     string
	CommentID  string
	AuthorID   string // the comment author
	CreatorID  string
	AssigneeID string
}

// LinkComment records that postID is a thread reply on taskID (the
// comment-as-thread design: content stays in the Mattermost post). It bumps the
// task's UpdatedAt (so the WS seq advances) and appends a "commented" audit
// event, all atomically via WithTx. A non-existent task yields ErrNotFound.
func (s *Service) LinkComment(taskID, postID, userID string) (model.TaskComment, CommentEvent, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	if _, err := s.loadTaskRow(ctx, taskID); err != nil {
		return model.TaskComment{}, CommentEvent{}, err
	}

	now := nowFunc()
	commentID := taskutil.GenerateULID()

	txCtx, txCancel := s.ctx()
	defer txCancel()
	var c model.TaskComment
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		// Atomic check-and-insert under the same tx so two concurrent callers
		// can't both miss the pre-check and race to the UNIQUE(post_id) insert:
		// the loser now sees the row the winner committed inside this tx and
		// returns it instead of erroring on uq_comments_post.
		if existing, err := tx.GetCommentByPostID(txCtx, postID); err == nil {
			c = existing
			return nil // already linked; skip the duplicate event append below
		} else if !errors.Is(err, store.ErrCommentNotFound) {
			return errors.Wrap(err, "link comment: check existing")
		}
		var lErr error
		c, lErr = tx.LinkComment(txCtx, commentID, taskID, postID, userID, now)
		if lErr != nil {
			return lErr
		}
		if err := tx.TouchTaskUpdatedAt(txCtx, taskID, now); err != nil {
			return err
		}
		return tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    taskID,
			ActorID:   userID,
			EventType: model.EventCommented,
			CreatedAt: now,
			ToValue:   ptrString(commentID),
		})
	}); err != nil {
		return model.TaskComment{}, CommentEvent{}, err
	}

	// Assemble participant ids for the notification event.
	creatorID, _ := s.store.GetMemberByRole(ctx, taskID, model.MemberRoleCreator)
	assigneeID, _ := s.store.GetMemberByRole(ctx, taskID, model.MemberRoleAssignee)
	return c, CommentEvent{
		TaskID:     taskID,
		CommentID:  commentID,
		AuthorID:   userID,
		CreatorID:  creatorID,
		AssigneeID: assigneeID,
	}, nil
}

// SubtaskProgress returns (done, total) for parentID's direct subtasks. A
// single GROUP BY query replaces the KV store's ListSubtasks + per-task loop.
func (s *Service) SubtaskProgress(parentID string) (done, total int, err error) {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.store.SubtaskProgress(ctx, parentID)
}

// ListSubtasks returns the direct subtasks of parentID as assembled entities,
// sorted by creation time. A missing parent yields an empty list.
func (s *Service) ListSubtasks(parentID string) ([]*model.Task, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	rows, err := s.store.ListSubtasks(ctx, parentID)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Task, 0, len(rows))
	for i := range rows {
		t, err := s.assembleTask(ctx, &rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// CreateSubtask creates a new task under parentID, inheriting the parent's
// channel and (as default) assignee, then atomically bumps the parent's
// UpdatedAt so the WebSocket seq advances. Thin wrapper over Create + a touch.
func (s *Service) CreateSubtask(parentID, creatorID, summary, assigneeID string, due *int64) (*model.Task, error) {
	created, err := s.Create(CreateInput{
		Summary:      summary,
		CreatorID:    creatorID,
		AssigneeID:   assigneeID,
		DueAt:        due,
		ParentTaskID: parentID,
	})
	if err != nil {
		return nil, err
	}

	// Record a subtask_added audit event on the PARENT and bump its
	// UpdatedAt, atomically. The parent's WS seq advancing lets a card show
	// the new subtask progress immediately.
	now := created.CreatedAt
	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		if err := tx.TouchTaskUpdatedAt(txCtx, parentID, now); err != nil {
			return err
		}
		return tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    parentID,
			ActorID:   creatorID,
			EventType: model.EventSubtaskAdded,
			CreatedAt: now,
			ToValue:   ptrString(created.ID),
		})
	}); err != nil {
		s.logUnexpected("failed to record subtask_added event / touch parent", err)
	}
	return created, nil
}

// SetPostIDs records the channel/DM card post ids for the task. Either value
// may be empty to leave that post kind unchanged. The writes (AddPost for each
// kind + UpdatedAt touch) commit atomically via WithTx.
func (s *Service) SetPostIDs(id, channelPostID, dmPostID string) (*model.Task, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	if _, err := s.loadTaskRow(ctx, id); err != nil {
		return nil, err
	}

	now := nowFunc()
	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		if channelPostID != "" {
			if err := tx.AddPost(txCtx, taskutil.GenerateULID(), id, channelPostID, model.PostKindChannel); err != nil {
				return err
			}
		}
		if dmPostID != "" {
			if err := tx.AddPost(txCtx, taskutil.GenerateULID(), id, dmPostID, model.PostKindDM); err != nil {
				return err
			}
		}
		return tx.TouchTaskUpdatedAt(txCtx, id, now)
	}); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// SetStatus transitions the task to newStatus using the canonical state machine
// (taskutil.ApplyStatus), and applies side effects atomically via WithTx:
//
//   - done:    rejected if open subtasks remain (ErrOpenSubtasks).
//   - cancelled: cascade-cancels open subtasks.
//   - terminal: clears any reminder.
//   - reopen:  resets the reminder so it can fire again.
//
// A "status_changed" audit event is appended in the same transaction.
var ErrInvalidStatus = errors.New("invalid status")

func (s *Service) SetStatus(actorID, id, newStatus string) (*model.Task, error) {
	if !model.IsValidStatus(newStatus) {
		return nil, ErrInvalidStatus
	}
	ctx, cancel := s.ctx()
	defer cancel()
	row, err := s.loadTaskRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.Status == newStatus {
		return s.assembleTask(ctx, row)
	}
	oldStatus := row.Status

	now := nowFunc()
	taskutil.ApplyStatus(row, newStatus, now)
	row.Priority = ensurePriority(row.Priority)

	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		updated, err := tx.UpdateTask(txCtx, *row)
		if err != nil {
			return err
		}
		row = &updated

		// Cascade-cancel open subtasks when the parent reaches a terminal
		// status (done or cancelled). Open subtasks are forced to cancelled
		// so a parent can be completed without manually closing every child.
		// The cascade is recursive: a cancelled subtask with its own open
		// subtasks cascades again (cascadeCancelSubtasks calls UpdateTask on
		// each child, which is itself a terminal transition — but it writes
		// directly rather than going through SetStatus, so the recursion is
		// handled by the explicit loop below).
		if newStatus == model.StatusDone || newStatus == model.StatusCancelled {
			if err := s.cascadeCancelSubtasks(txCtx, tx, id, actorID, now); err != nil {
				return err
			}
		}

		switch newStatus {
		case model.StatusDone, model.StatusCancelled:
			// Terminal: clear any reminder (idempotent if none).
			if err := tx.ClearReminder(txCtx, id); err != nil {
				return err
			}
		case model.StatusTodo, model.StatusInProgress:
			// Reopen: if the task has a due and a reminder offset, re-arm the
			// reminder so it can fire again. ListReminders carries the offset.
			reminders, rErr := tx.ListReminders(txCtx, id)
			if rErr != nil {
				return rErr
			}
			if len(reminders) > 0 && row.DueAt != nil {
				if _, err := tx.SetReminder(txCtx, reminders[0].ID, id, reminders[0].OffsetMS); err != nil {
					return err
				}
			}
		}

		return tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    id,
			ActorID:   actorID,
			EventType: model.EventStatusChanged,
			CreatedAt: now,
			FromValue: ptrString(oldStatus),
			ToValue:   ptrString(newStatus),
		})
	}); err != nil {
		return nil, err
	}
	return s.assembleTask(ctx, row)
}

// maxSubtaskDepth caps how deep a subtask chain may nest. It bounds the
// ancestor walk used by the cycle guard and prevents pathologically deep
// hierarchies.
const maxSubtaskDepth = 16

// assertNoCycle walks the parent chain from startID up to maxDepth ancestors via
// GetTask, returning ErrSubtaskCycle if the chain repeats a node or exceeds the
// depth cap. A nil error means attaching a child under startID cannot form a
// loop within the bound.
func (s *Service) assertNoCycle(ctx context.Context, startID string, maxDepth int) error {
	seen := map[string]struct{}{startID: {}}
	cur := startID
	for range maxDepth {
		t, err := s.store.GetTask(ctx, cur)
		if err != nil {
			// A missing ancestor ends the chain (no cycle possible within
			// the bound). This is expected when walking up from a task whose
			// parent was deleted or never existed.
			if errors.Is(err, store.ErrTaskNotFound) {
				return nil
			}
			return err
		}
		if t == nil || t.ParentTaskID == "" {
			return nil
		}
		if _, loop := seen[t.ParentTaskID]; loop {
			return ErrSubtaskCycle
		}
		seen[t.ParentTaskID] = struct{}{}
		cur = t.ParentTaskID
	}
	return ErrSubtaskCycle
}

// ErrSubtaskCycle is returned when creating a subtask would form a cycle or
// exceed the nesting depth cap (maxSubtaskDepth).
var ErrSubtaskCycle = errors.New("subtask would form a cycle or exceed the nesting limit")

// cascadeCancelSubtasks moves every todo/in_progress subtask of parentID to
// cancelled, issuing UpdateTask per subtask inside the already-open tx. Already
// terminal subtasks are left untouched. The cascade is recursive: each
// cancelled subtask's own open subtasks are cancelled too, depth-first, so a
// deep hierarchy collapses in one transition.
func (s *Service) cascadeCancelSubtasks(ctx context.Context, tx store.Store, parentID, actorID string, now int64) error {
	subs, err := tx.ListSubtasks(ctx, parentID)
	if err != nil {
		return errors.Wrap(err, "failed to list subtasks for cascade cancel")
	}
	for i := range subs {
		sub := subs[i]
		if model.IsTerminalStatus(sub.Status) {
			continue
		}
		oldStatus := sub.Status
		taskutil.ApplyStatus(&sub, model.StatusCancelled, now)
		sub.Priority = ensurePriority(sub.Priority)
		if _, err := tx.UpdateTask(ctx, sub); err != nil {
			return err
		}
		// Audit each cascade-cancelled subtask so the trail records the
		// system-initiated cancellation (from open -> cancelled).
		from, to := oldStatus, model.StatusCancelled
		if err := tx.AppendTaskEvent(ctx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    sub.ID,
			ActorID:   actorID,
			EventType: model.EventStatusChanged,
			CreatedAt: now,
			FromValue: &from,
			ToValue:   &to,
		}); err != nil {
			return err
		}
		// Recurse: cancel this subtask's own open subtasks too.
		if err := s.cascadeCancelSubtasks(ctx, tx, sub.ID, actorID, now); err != nil {
			return err
		}
	}
	return nil
}

// SetReminder sets the reminder offset (ms before due) on the task. The task
// must have a due date; offset must be > 0. The reminder's fired_at is reset on
// set (ON CONFLICT DO UPDATE in the store) so the reminder can fire fresh.
func (s *Service) SetReminder(actorID, id string, offsetMS int64) (*model.Task, error) {
	if offsetMS <= 0 {
		return nil, errors.New("reminder offset must be positive")
	}
	ctx, cancel := s.ctx()
	defer cancel()
	row, err := s.loadTaskRow(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.DueAt == nil {
		return nil, ErrReminderNeedsDue
	}

	now := nowFunc()
	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		if _, err := tx.SetReminder(txCtx, taskutil.GenerateULID(), id, offsetMS); err != nil {
			return err
		}
		if err := tx.TouchTaskUpdatedAt(txCtx, id, now); err != nil {
			return err
		}
		return tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    id,
			ActorID:   actorID,
			EventType: model.EventReminderSet,
			CreatedAt: now,
			ToValue:   ptrString(strconv.FormatInt(offsetMS, 10)),
		})
	}); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// ClearReminder removes the reminder from the task.
func (s *Service) ClearReminder(actorID, id string) (*model.Task, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	if _, err := s.loadTaskRow(ctx, id); err != nil {
		return nil, err
	}

	now := nowFunc()
	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		if err := tx.ClearReminder(txCtx, id); err != nil {
			return err
		}
		if err := tx.TouchTaskUpdatedAt(txCtx, id, now); err != nil {
			return err
		}
		return tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    id,
			ActorID:   actorID,
			EventType: model.EventReminderCleared,
			CreatedAt: now,
		})
	}); err != nil {
		return nil, err
	}
	return s.Get(id)
}

// ErrReminderNeedsDue is returned when a reminder is requested on a task with
// no due date.
var ErrReminderNeedsDue = errors.New("reminder requires a due date")

// FireReadyReminders returns every pending reminder whose fire time has arrived
// (now >= due-offset, within grace). The 3-table JOIN in store.ListDueReminders
// replaces the KV store's ListReminderKeys + per-task Get loop. The caller DMs
// each reminder and then calls MarkReminderFired(reminderID).
func (s *Service) FireReadyReminders(nowMs int64, grace time.Duration) ([]model.DueReminder, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.store.ListDueReminders(ctx, nowMs, grace.Milliseconds())
}

// MarkReminderFired stamps the reminder row's fired_at so it won't fire again,
// and appends a reminder-fired audit event, atomically. Takes the reminder id
// (not task id) because SQL reminders have their own id.
func (s *Service) MarkReminderFired(reminderID, taskID string) error {
	now := nowFunc()
	ctx, cancel := s.ctx()
	defer cancel()
	return s.store.WithTx(ctx, func(tx store.Store) error {
		if err := tx.MarkReminderFired(ctx, reminderID, now); err != nil {
			return err
		}
		return tx.AppendTaskEvent(ctx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    taskID,
			ActorID:   "system", // reminder job has no human actor
			EventType: model.EventReminderFired,
			CreatedAt: now,
		})
	})
}

// PatchInput is the partial-update payload. Only fields listed in UpdateFields
// are modified; a field present in UpdateFields with the corresponding pointer
// nil clears that field.
type PatchInput struct {
	UpdateFields []string
	Summary      *string
	Description  *string
	DueAt        *int64 // nil clears due when "due" is in UpdateFields
	IsAllDay     *bool
	// Priority, when "priority" is in UpdateFields. The pointer is non-nil in
	// practice (there is no "clear" semantic for priority — it always has a
	// value); nil keeps the existing value as a safety net.
	Priority *string
}

// Patch applies a partial update to the task identified by id. A due change
// re-arms the reminder (resetting fired_at) if a reminder offset exists, or
// leaves it cleared otherwise. All writes commit atomically via WithTx, and a
// per-field audit event (summary_changed/description_changed/due_changed) is
// appended for each changed field with its from/to value.
func (s *Service) Patch(actorID, id string, in PatchInput) (*model.Task, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	row, err := s.loadTaskRow(ctx, id)
	if err != nil {
		return nil, err
	}

	// Track per-field from/to so the audit events carry the real transitions.
	type fieldChange struct {
		eventType string
		from, to  string
	}
	var changes []fieldChange
	dueChanged := false
	for _, field := range in.UpdateFields {
		switch field {
		case "summary":
			if in.Summary != nil {
				old := row.Summary
				row.Summary = *in.Summary
				changes = append(changes, fieldChange{model.EventSummaryChanged, old, *in.Summary})
			}
		case "description":
			old := row.Description
			row.Description = derefStr(in.Description)
			changes = append(changes, fieldChange{model.EventDescriptionChanged, old, row.Description})
		case "due":
			oldDue := ptrStringOrEmpty(row.DueAt)
			row.DueAt = in.DueAt
			newDue := ptrStringOrEmpty(row.DueAt)
			dueChanged = true
			changes = append(changes, fieldChange{model.EventDueChanged, oldDue, newDue})
		case "priority":
			// Priority always has a value (standard default); a nil pointer is
			// treated as a no-op to keep the existing value.
			if in.Priority != nil {
				newP := *in.Priority
				if newP == "" {
					newP = model.PriorityStandard
				}
				if !model.IsValidPriority(newP) {
					return nil, errors.New("invalid priority")
				}
				old := row.Priority
				row.Priority = newP
				changes = append(changes, fieldChange{model.EventPriorityChanged, old, newP})
			}
		case "is_all_day":
			row.IsAllDay = derefBool(in.IsAllDay)
		}
	}
	if len(changes) == 0 && !containsField(in.UpdateFields, "is_all_day") {
		return s.assembleTask(ctx, row)
	}

	now := nowFunc()
	row.UpdatedAt = now
	row.Priority = ensurePriority(row.Priority)

	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		updated, err := tx.UpdateTask(txCtx, *row)
		if err != nil {
			return err
		}
		row = &updated

		// A due change resets the reminder so it can fire under the new
		// deadline (if a reminder offset exists).
		if dueChanged {
			reminders, rErr := tx.ListReminders(txCtx, id)
			if rErr != nil {
				return rErr
			}
			if row.DueAt != nil && len(reminders) > 0 {
				if _, err := tx.SetReminder(txCtx, reminders[0].ID, id, reminders[0].OffsetMS); err != nil {
					return err
				}
			}
		}

		// Append one audit event per changed field, each with its from/to.
		for _, c := range changes {
			from, to := c.from, c.to
			ev := model.TaskEvent{
				ID:        taskutil.GenerateULID(),
				TaskID:    id,
				ActorID:   actorID,
				EventType: c.eventType,
				CreatedAt: now,
				FromValue: &from,
				ToValue:   &to,
			}
			if err := tx.AppendTaskEvent(txCtx, ev); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return s.assembleTask(ctx, row)
}

// ptrStringOrEmpty renders a *int64 as a decimal string, or "" when nil. Used
// to populate audit FromValue/ToValue for due changes.
func ptrStringOrEmpty(p *int64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatInt(*p, 10)
}

// containsField reports whether fields contains s. Thin wrapper over
// slices.Contains to keep the Patch no-op check readable.
func containsField(fields []string, s string) bool {
	return slices.Contains(fields, s)
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

// ptrString returns a pointer to s; used for TaskEvent.FromValue/ToValue which
// are *string.
func ptrString(s string) *string { return &s }

// ErrNotFound is returned by Get/Patch/Delete when the task id does not exist.
var ErrNotFound = errors.New("task not found")

// ErrParentNotFound is returned by Create when a subtask references a parent
// task id that does not exist.
var ErrParentNotFound = errors.New("parent task not found")

// AssignEvent describes the result of an assignee change so callers can fire
// the notification without re-reading the task. OldAssigneeID is empty when the
// task had no assignee before.
type AssignEvent struct {
	TaskID        string
	OldAssigneeID string
	NewAssigneeID string
	CreatorID     string
}

// Assign sets the task's single assignee to newAssigneeID. Because the assignee
// is a task_members row (not a column), SetAssignee replaces the KV store's
// DeleteIndex+SaveIndex pair in one atomic statement; the reminder row no
// longer needs resync because ListDueReminders JOINs the assignee at fire
// time. An "assigned"/"unassigned" audit event is appended in the same tx.
func (s *Service) Assign(actorID, id, newAssigneeID string) (*model.Task, AssignEvent, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	row, loadErr := s.loadTaskRow(ctx, id)
	if loadErr != nil {
		return nil, AssignEvent{}, loadErr
	}

	oldAssigneeID, _ := s.store.GetMemberByRole(ctx, id, model.MemberRoleAssignee)
	if oldAssigneeID == newAssigneeID {
		t, asmErr := s.assembleTask(ctx, row)
		if asmErr != nil {
			return nil, AssignEvent{}, asmErr
		}
		return t, AssignEvent{
			TaskID:        id,
			OldAssigneeID: oldAssigneeID,
			NewAssigneeID: newAssigneeID,
		}, nil
	}

	now := nowFunc()
	eventType := model.EventAssigned
	if newAssigneeID == "" {
		eventType = model.EventUnassigned
	}

	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		// Empty new assignee means "unassign": remove the assignee edge. Skip
		// the RemoveMember call when there is no current assignee — otherwise
		// RemoveMember("", ...) returns "user id is required" and the whole
		// assign call fails with a 500. An unassign on an already-unassigned
		// task is a no-op (still record the touch + event for auditability).
		if newAssigneeID == "" {
			if oldAssigneeID != "" {
				if err := tx.RemoveMember(txCtx, id, oldAssigneeID, model.MemberRoleAssignee); err != nil {
					return err
				}
			}
			if err := tx.TouchTaskUpdatedAt(txCtx, id, now); err != nil {
				return err
			}
			from, to := oldAssigneeID, ""
			return tx.AppendTaskEvent(txCtx, model.TaskEvent{
				ID:        taskutil.GenerateULID(),
				TaskID:    id,
				ActorID:   actorID,
				EventType: model.EventUnassigned,
				CreatedAt: now,
				FromValue: &from,
				ToValue:   &to,
			})
		}
		if err := tx.SetAssignee(txCtx, id, newAssigneeID); err != nil {
			return err
		}
		if err := tx.TouchTaskUpdatedAt(txCtx, id, now); err != nil {
			return err
		}
		return tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    id,
			ActorID:   actorID,
			EventType: eventType,
			CreatedAt: now,
			FromValue: ptrString(oldAssigneeID),
			ToValue:   ptrString(newAssigneeID),
		})
	}); err != nil {
		return nil, AssignEvent{}, err
	}

	creatorID, _ := s.store.GetMemberByRole(ctx, id, model.MemberRoleCreator)
	t, err := s.assembleTask(ctx, row)
	if err != nil {
		return nil, AssignEvent{}, err
	}
	return t, AssignEvent{
		TaskID:        id,
		OldAssigneeID: oldAssigneeID,
		NewAssigneeID: newAssigneeID,
		CreatorID:     creatorID,
	}, nil
}

// Delete hard-deletes the task and everything attached to it. The FK ON DELETE
// CASCADE on task_members/task_reminders/task_posts/task_comments/task_events
// and the self-FK on task_tasks (subtasks) remove all dependents in one call —
// the KV store's manual N-step cascade is gone.
func (s *Service) Delete(actorID, id string) error {
	ctx, cancel := s.ctx()
	defer cancel()
	// Load the task snapshot for the audit event's FromValue BEFORE deleting.
	// The event is appended inside the same tx as the delete so they commit
	// atomically. Note: task_events has ON DELETE CASCADE, so the event is
	// removed alongside the task by the same delete — it serves the immediate
	// WS broadcast / notification, not long-term retention of deletes. A
	// non-cascaded audit table is a future schema change if delete-history
	// retention is needed.
	row, loadErr := s.loadTaskRow(ctx, id)
	if loadErr != nil {
		return loadErr
	}
	snapshot := snapshotTaskJSON(row)

	txCtx, txCancel := s.ctx()
	defer txCancel()
	if err := s.store.WithTx(txCtx, func(tx store.Store) error {
		if err := tx.AppendTaskEvent(txCtx, model.TaskEvent{
			ID:        taskutil.GenerateULID(),
			TaskID:    id,
			ActorID:   actorID,
			EventType: model.EventDeleted,
			CreatedAt: nowFunc(),
			FromValue: &snapshot,
		}); err != nil {
			return err
		}
		if err := tx.DeleteTask(txCtx, id); err != nil {
			if errors.Is(err, store.ErrTaskNotFound) {
				return ErrNotFound
			}
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// snapshotTaskJSON renders a TaskRow as a compact JSON string for an audit
// event's FromValue/ToValue. Best-effort: a marshal error yields the empty
// string rather than failing the transition.
func snapshotTaskJSON(row *model.TaskRow) string {
	b, err := json.Marshal(row)
	if err != nil {
		return ""
	}
	return string(b)
}

// Scope enumerates the list result scopes. These mirror store.Scope so the
// service layer keeps its own read-friendly names while delegating to the
// store's enum. Two scopes are supported: channel (tasks of a channel) and
// direct (tasks shared between two DM participants).
type Scope string

const (
	ScopeChannel Scope = Scope(store.ScopeChannel)
	ScopeDirect  Scope = Scope(store.ScopeDirect)
)

// ListQuery is the filtered/paginated list request. It mirrors store.ListQuery;
// the service maps its loose Due string to a store.DueFilter.
type ListQuery struct {
	Scope         Scope
	UserID        string // required for ScopeDirect (one of the two DM users)
	ChannelID     string // required for ScopeChannel
	PartnerID     string // required for ScopeDirect (the other DM user)
	Status        string // optional: "", todo, in_progress, done, cancelled
	Priority      string // optional: "", standard, important, urgent
	DueAt         string // optional: "", overdue, today, week
	AfterOrderKey string // cursor: only tasks with OrderKey > this are returned
	Limit         int    // page size; <=0 defaults to DefaultLimit
}

// DefaultLimit is the default page size for list queries.
const DefaultLimit = 50

// List returns the tasks matching the query. The WHERE, ORDER BY, and LIMIT are
// all pushed to the database via store.ListTasks (replacing the KV store's
// load-all-candidates + filter-in-Go loop); the page rows are then assembled
// into model.Task entities.
func (s *Service) List(q ListQuery) ([]*model.Task, error) {
	if q.Limit <= 0 {
		q.Limit = DefaultLimit
	}
	ctx, cancel := s.ctx()
	defer cancel()

	page, err := s.store.ListTasks(ctx, store.ListQuery{
		Scope:         store.Scope(q.Scope),
		UserID:        q.UserID,
		ChannelID:     q.ChannelID,
		PartnerID:     q.PartnerID,
		Status:        q.Status,
		Priority:      q.Priority,
		Due:           mapDueFilter(q.DueAt),
		DueAsOf:       nowFunc(),
		AfterOrderKey: q.AfterOrderKey,
		Limit:         q.Limit,
	})
	if err != nil {
		return nil, err
	}

	out := make([]*model.Task, 0, len(page.Items))
	for _, item := range page.Items {
		row, ok := item.(*model.TaskRow)
		if !ok {
			continue
		}
		t, err := s.assembleTask(ctx, row)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	// Preserve the store's order_key ASC ordering (items are already sorted);
	// this sort is a defensive no-op if the store honoured the request.
	sort.SliceStable(out, func(i, j int) bool { return out[i].OrderKey < out[j].OrderKey })
	return out, nil
}

// mapDueFilter converts the loose Due string from the REST/command layer into a
// store.DueFilter. Unknown values map to DueAny (no filter).
func mapDueFilter(due string) store.DueFilter {
	switch strings.ToLower(due) {
	case "overdue":
		return store.DueOverdue
	case "today":
		return store.DueToday
	case "week":
		return store.DueWeek
	default:
		return store.DueAny
	}
}

// Search returns up to limit tasks whose Summary or Description contains
// keyword (case-insensitive). The ILIKE/LIKE is pushed to the database via
// store.SearchTasks, replacing the KV store's load-all + strings.Contains loop.
func (s *Service) Search(keyword string, limit int) ([]*model.Task, error) {
	if keyword == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	ctx, cancel := s.ctx()
	defer cancel()
	rows, err := s.store.SearchTasks(ctx, keyword, limit)
	if err != nil {
		return nil, err
	}
	out := make([]*model.Task, 0, len(rows))
	for i := range rows {
		t, err := s.assembleTask(ctx, &rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
