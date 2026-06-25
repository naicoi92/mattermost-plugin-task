package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/pkg/errors"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/notification"
	"github.com/naicoi92/mattermost-plugin-task/server/permission"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// initRouter initializes the HTTP router for the plugin.
func (p *Plugin) initRouter() *mux.Router {
	router := mux.NewRouter()

	// Authenticated sub-router: endpoints reached from the browser (task CRUD,
	// card actions, dialog openers). The Mattermost host sets the
	// Mattermost-User-ID header on these via the session cookie proxy, so the
	// auth middleware rejects anything without it.
	apiRouter := router.PathPrefix("/api/v1").Subrouter()
	apiRouter.Use(p.MattermostAuthorizationRequired)

	apiRouter.HandleFunc("/hello", p.HelloWorld).Methods(http.MethodGet)

	// Interactive chip callback (Status/Priority cycles): the card's chips
	// POST here with context {action, task_id}; handleCardAction cycles the
	// corresponding value and refreshes the card.
	apiRouter.HandleFunc("/actions", p.handleCardAction).Methods(http.MethodPost)

	// Task CRUD (issue #7).
	tasks := apiRouter.PathPrefix("/tasks").Subrouter()
	tasks.HandleFunc("", p.createTask).Methods(http.MethodPost)
	tasks.HandleFunc("", p.listTasks).Methods(http.MethodGet)
	tasks.HandleFunc("/{id:[^/]+}", p.getTask).Methods(http.MethodGet)
	tasks.HandleFunc("/{id:[^/]+}", p.patchTask).Methods(http.MethodPatch)
	tasks.HandleFunc("/{id:[^/]+}", p.deleteTask).Methods(http.MethodDelete)
	tasks.HandleFunc("/{id:[^/]+}/status", p.patchTaskStatus).Methods(http.MethodPatch)
	tasks.HandleFunc("/{id:[^/]+}/reminder", p.setReminder).Methods(http.MethodPost)
	tasks.HandleFunc("/{id:[^/]+}/reminder", p.deleteReminder).Methods(http.MethodDelete)
	tasks.HandleFunc("/{id:[^/]+}/assignee", p.setAssignee).Methods(http.MethodPost)
	tasks.HandleFunc("/{id:[^/]+}/assignee", p.deleteAssignee).Methods(http.MethodDelete)

	// Subtasks (issue #21).
	tasks.HandleFunc("/{id:[^/]+}/subtasks", p.createSubtask).Methods(http.MethodPost)
	tasks.HandleFunc("/{id:[^/]+}/subtasks", p.listSubtasks).Methods(http.MethodGet)

	// Comments (issue #24).
	tasks.HandleFunc("/{id:[^/]+}/comments", p.createComment).Methods(http.MethodPost)
	tasks.HandleFunc("/{id:[^/]+}/comments", p.listComments).Methods(http.MethodGet)

	// Audit trail / activity timeline (M4-4).
	tasks.HandleFunc("/{id:[^/]+}/events", p.listTaskEvents).Methods(http.MethodGet)

	// Share an existing task's card into a channel (issue #159).

	return router
}

// ServeHTTP demonstrates a plugin that handles HTTP requests by greeting the world.
// The root URL is currently <siteUrl>/plugins/com.mattermost.plugin-task/api/v1/.
func (p *Plugin) ServeHTTP(c *plugin.Context, w http.ResponseWriter, r *http.Request) {
	p.router.ServeHTTP(w, r)
}

func (p *Plugin) MattermostAuthorizationRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("Mattermost-User-ID")
		if userID == "" {
			http.Error(w, "Not authorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (p *Plugin) HelloWorld(w http.ResponseWriter, r *http.Request) {
	if _, err := w.Write([]byte("Hello, world!")); err != nil {
		p.API.LogError("Failed to write response", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// currentUserID extracts the authenticated user id from the request. The
// MattermostAuthorizationRequired middleware guarantees it is non-empty by the
// time handlers run.
func currentUserID(r *http.Request) string {
	return r.Header.Get("Mattermost-User-ID")
}

// writeJSON encodes v as JSON with a 200 status. Errors are logged and surfaced
// as a 500.
func (p *Plugin) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		p.API.LogError("Failed to encode JSON response", "error", err)
	}
}

// writeError responds with a plain-text error and status code, logging
// server-side errors for debugging.
func (p *Plugin) writeError(w http.ResponseWriter, status int, msg string) {
	if status >= 500 {
		p.API.LogError("HTTP handler error", "status", status, "error", msg)
	}
	http.Error(w, msg, status)
}

// createTaskRequest is the JSON body for POST /tasks.
type createTaskRequest struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	// ChannelID is required: the home channel of the task. The client always
	// has a channel context (team channel, DM, or self-DM), so it sends the
	// real channel id here. There is no longer a separate post_channel_id —
	// the card is posted into ChannelID.
	ChannelID      string `json:"channel_id"`
	AssigneeID     string `json:"assignee_id"`
	Due            *int64 `json:"due"`
	IsAllDay       bool   `json:"is_all_day"`
	ParentTaskID   string `json:"parent_task_id"`
	ReminderOffset *int64 `json:"reminder_offset"`
	Priority       string `json:"priority"`
}

// createTask handles POST /tasks.
func (p *Plugin) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	created, err := p.taskService.Create(task.CreateInput{
		Summary:        strings.TrimSpace(req.Summary),
		Description:    req.Description,
		ChannelID:      req.ChannelID,
		CreatorID:      currentUserID(r),
		AssigneeID:     req.AssigneeID,
		DueAt:          req.Due,
		IsAllDay:       req.IsAllDay,
		ParentTaskID:   req.ParentTaskID,
		ReminderOffset: req.ReminderOffset,
		Priority:       req.Priority,
	})
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "required"):
			p.writeError(w, http.StatusBadRequest, err.Error())
		case strings.Contains(err.Error(), "invalid priority"):
			// An invalid priority value is a client error, not a server error.
			p.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, task.ErrParentNotFound):
			// A missing parent is a client error (bad parent_task_id), not a
			// server error.
			p.writeError(w, http.StatusBadRequest, "parent task not found")
		default:
			p.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Post the interactive task card into the task's home channel (always set
	// now). The card is the single interactive surface; assignee notification is
	// event-driven (Notifier.NotifyAssigned), not a second card here.
	//
	// Atomicity strategy (M4-2): the task is committed FIRST (service.Create is
	// itself atomic). Card posts and their task_posts linkage happen AFTER. A
	// crash between Create and the card post leaves the task intact with no
	// card (acceptable — the task is the source of truth; cards are rebuildable).
	var channelPostID string
	if created.ChannelID != "" {
		channelPostID = p.postCard(created.ChannelID, created)
		if channelPostID == "" {
			p.API.LogError("createTask: postCard failed; no card preview",
				"task_id", created.ID, "channel_id", created.ChannelID)
		}
	}
	if channelPostID != "" {
		updated, err := p.taskService.SetPostIDs(created.ID, channelPostID)
		if err != nil {
			p.API.LogError("Failed to persist task card post ID",
				"task_id", created.ID,
				"channel_post_id", channelPostID,
				"error", err)
		} else if updated != nil {
			created = updated
		}
	}

	w.WriteHeader(http.StatusCreated)
	p.writeJSON(w, created)

	// Notify the assignee (event-driven; covers create-with-assignee and
	// includes self-assign as an acknowledgment under the all-channel model).
	if created.AssigneeID != "" && p.notifier != nil {
		p.notifier.NotifyAssigned(created.AssigneeID, created.CreatorID, notification.TaskSummary{
			ID:      created.ID,
			Summary: created.Summary,
		})
	}

	// Real-time: a new task is visible to Quick List / Kanban clients (#32).
	p.broadcastTaskUpdated(created, []string{"created"})
}

// listTasks handles GET /tasks with scope/status/due/cursor query params.
//
// Under the all-channel model there is one list view: scope=channel with
// channel_id = the home channel (team channel, DM, or self-DM). The legacy
// scope=direct (JOIN task_members) path has been removed.
//
// Membership defense: for scope=channel the caller must be a member of the
// channel they ask about, otherwise the request is rejected as 403 (this
// prevents a user from enumerating another channel's tasks by guessing the
// channel id).
func (p *Plugin) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	scope := task.Scope(q.Get("scope"))
	channelID := q.Get("channel_id")

	if scope != task.ScopeChannel {
		p.writeError(w, http.StatusBadRequest, "scope must be 'channel'")
		return
	}

	query := task.ListQuery{
		Scope:         scope,
		ChannelID:     channelID,
		Status:        q.Get("status"),
		DueAt:         q.Get("due"),
		Priority:      q.Get("priority"),
		AfterOrderKey: q.Get("after_order_key"),
		Limit:         limit,
	}

	if query.ChannelID == "" {
		p.writeError(w, http.StatusBadRequest, "channel_id is required")
		return
	}
	if query.Status != "" && !taskmodel.IsValidStatus(query.Status) {
		p.writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	if query.Priority != "" && !taskmodel.IsValidPriority(query.Priority) {
		p.writeError(w, http.StatusBadRequest, "invalid priority")
		return
	}

	tasks, err := p.taskService.List(query)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.writeJSON(w, tasks)
}

// getTask handles GET /tasks/:id.
func (p *Plugin) getTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	t, err := p.taskService.Get(id)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permission.CanUserViewTask(currentUserID(r), t, p.channelMembership()) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to view this task")
		return
	}
	p.writeJSON(w, t)
}

// patchTaskRequest is the JSON body for PATCH /tasks/:id. Only fields listed in
// UpdateFields are modified.
type patchTaskRequest struct {
	UpdateFields []string `json:"update_fields"`
	Summary      *string  `json:"summary"`
	Description  *string  `json:"description"`
	Due          *int64   `json:"due"`
	IsAllDay     *bool    `json:"is_all_day"`
	Priority     *string  `json:"priority"`
}

// patchTask handles PATCH /tasks/:id.
func (p *Plugin) patchTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, ok := p.loadTaskForManage(w, id, currentUserID(r)); !ok {
		return
	}
	var req patchTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	updated, err := p.taskService.Patch(currentUserID(r), id, task.PatchInput{
		UpdateFields: req.UpdateFields,
		Summary:      req.Summary,
		Description:  req.Description,
		DueAt:        req.Due,
		IsAllDay:     req.IsAllDay,
		Priority:     req.Priority,
	})
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			p.writeError(w, http.StatusNotFound, "task not found")
		case strings.Contains(err.Error(), "invalid priority"):
			p.writeError(w, http.StatusBadRequest, err.Error())
		default:
			p.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	p.writeJSON(w, updated)

	// Refresh the interactive card (channel + DM) so summary/description/due/
	// priority edits stay in sync with the DB — previously only status/subtask/
	// comment changes refreshed it, so patched fields drifted.
	p.updateTaskCards(updated)

	// Real-time: summary/description/due/is_all_day changed (#32).
	p.broadcastTaskUpdated(updated, req.UpdateFields)
}

// deleteTask handles DELETE /tasks/:id (hard-delete cascade).
func (p *Plugin) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	// Load the task both to authorize the delete and to snapshot it for the
	// real-time event. The error MUST be handled: silently ignoring it would
	// skip the permission guard on a transient failure and let a non-creator
	// delete a task that actually exists.
	snapshot, err := p.taskService.Get(id)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permission.CanUserDeleteTask(currentUserID(r), snapshot) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to delete this task")
		return
	}

	if err := p.taskService.Delete(currentUserID(r), id); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)

	// Real-time: clients remove the task from their caches (#32).
	p.broadcastTaskDeleted(snapshot)
}

// patchTaskStatusRequest is the JSON body for PATCH /tasks/:id/status.
type patchTaskStatusRequest struct {
	Status string `json:"status"`
}

// patchTaskStatus handles PATCH /tasks/:id/status. Sets the status via the
// canonical state machine; done/cancelled stop reminders and cancel cascades to
// open subtasks.
func (p *Plugin) patchTaskStatus(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, ok := p.loadTaskForManage(w, id, currentUserID(r)); !ok {
		return
	}
	var req patchTaskStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !taskmodel.IsValidStatus(req.Status) {
		p.writeError(w, http.StatusBadRequest, "invalid status")
		return
	}

	updated, err := p.taskService.SetStatus(currentUserID(r), id, req.Status)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			p.writeError(w, http.StatusNotFound, "task not found")
		case errors.Is(err, task.ErrInvalidStatus):
			p.writeError(w, http.StatusBadRequest, "invalid status")
		default:
			p.API.LogError("Failed to set task status",
				"task_id", id, "status", req.Status, "actor", currentUserID(r), "error", err.Error())
			p.writeError(w, http.StatusInternalServerError, "failed to update status")
		}
		return
	}

	// Notify participants when a task reaches a terminal status (done/cancelled).
	// The actor is the authenticated user; they are excluded from the recipients.
	p.notifyTerminalStatus(updated, req.Status, currentUserID(r))

	// Refresh the interactive card (channel + DM) to reflect the new status.
	p.updateTaskCards(updated)

	p.writeJSON(w, updated)

	// Real-time: status changed — Kanban column + Quick List badges refresh (#32).
	p.broadcastTaskUpdated(updated, []string{"status"})

	// When a subtask reaches a terminal status, refresh the parent's card so its
	// "x/y done" progress reflects the change (SetStatus cascade-cancels open
	// siblings too, so a single refresh keeps the parent consistent).
	if updated.ParentTaskID != "" {
		if parent, gErr := p.taskService.Get(updated.ParentTaskID); gErr == nil && parent != nil {
			p.updateTaskCards(parent)
			p.broadcastTaskUpdated(parent, []string{"subtasks"})
		}
	}
}

// notifyTerminalStatus fires the done/cancelled DM to creator + assignee (minus
// the actor). No-op for non-terminal statuses or when the notifier is unset.
func (p *Plugin) notifyTerminalStatus(t *taskmodel.Task, status, actorID string) {
	if p.notifier == nil || t == nil {
		return
	}
	summary := notification.TaskSummary{ID: t.ID, Summary: t.Summary}
	switch status {
	case taskmodel.StatusDone:
		p.notifier.NotifyCompleted(summary, actorID, t.CreatorID, t.AssigneeID)
	case taskmodel.StatusCancelled:
		p.notifier.NotifyCancelled(summary, actorID, t.CreatorID, t.AssigneeID)
	}
}

// setReminderRequest is the JSON body for POST /tasks/:id/reminder.
type setReminderRequest struct {
	OffsetMS int64 `json:"offset_ms"`
}

// setReminder handles POST /tasks/:id/reminder.
func (p *Plugin) setReminder(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, ok := p.loadTaskForManage(w, id, currentUserID(r)); !ok {
		return
	}
	var req setReminderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	updated, err := p.taskService.SetReminder(currentUserID(r), id, req.OffsetMS)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			p.writeError(w, http.StatusNotFound, "task not found")
		case errors.Is(err, task.ErrReminderNeedsDue):
			p.writeError(w, http.StatusBadRequest, "task has no due date")
		case req.OffsetMS <= 0:
			// Invalid offset is a client error.
			p.writeError(w, http.StatusBadRequest, "offset_ms must be positive")
		default:
			// Unexpected service/store failures are server errors; don't echo the
			// raw error text to the client.
			p.writeError(w, http.StatusInternalServerError, "failed to set reminder")
		}
		return
	}
	p.writeJSON(w, updated)

	// Real-time: reminder offset changed (#32).
	p.broadcastTaskUpdated(updated, []string{"reminder_offset"})
}

// deleteReminder handles DELETE /tasks/:id/reminder (turn reminders off).
func (p *Plugin) deleteReminder(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, ok := p.loadTaskForManage(w, id, currentUserID(r)); !ok {
		return
	}
	updated, err := p.taskService.ClearReminder(currentUserID(r), id)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		// Don't leak internal error text; match the set-reminder handler.
		p.writeError(w, http.StatusInternalServerError, "failed to clear reminder")
		return
	}
	p.writeJSON(w, updated)

	// Real-time: reminder cleared (#32).
	p.broadcastTaskUpdated(updated, []string{"reminder_offset"})
}

// setAssigneeRequest is the JSON body for POST /tasks/:id/assignee.
type setAssigneeRequest struct {
	UserID string `json:"user_id"`
}

// setAssignee handles POST /tasks/:id/assignee. Replaces the current assignee
// and DMs the new assignee (unless they are the creator).
func (p *Plugin) setAssignee(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, ok := p.loadTaskForManage(w, id, currentUserID(r)); !ok {
		return
	}
	var req setAssigneeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		p.writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}

	updated, ev, err := p.taskService.Assign(currentUserID(r), id, req.UserID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Under the all-channel model a DM-scoped task follows its assignee: when
	// reassigned, move the card (and its comments) into the DM between creator
	// and new assignee. Team-channel tasks stay put (shared surface).
	if ev.OldAssigneeID != "" && ev.NewAssigneeID != "" && ev.OldAssigneeID != ev.NewAssigneeID {
		if moved, mErr := p.moveTaskChannelIfDM(updated, ev.NewAssigneeID); mErr != nil {
			p.API.LogWarn("reassign: move channel failed; task stays in old DM",
				"task_id", updated.ID, "error", mErr)
		} else if moved != nil {
			updated = moved
		}
	}

	// Notify the newly assigned user. Under the all-channel model this
	// fires for every non-empty new assignee (self-assign included).
	if p.notifier != nil {
		p.notifier.NotifyAssigned(ev.NewAssigneeID, ev.CreatorID, notification.TaskSummary{
			ID: updated.ID, Summary: updated.Summary,
		})
	}
	p.writeJSON(w, updated)

	// Refresh the interactive card so the Assignee field stays in sync.
	p.updateTaskCards(updated)

	// Real-time: assignee changed — Quick List "My Tasks" + avatars refresh (#32).
	p.broadcastTaskUpdated(updated, []string{"assignee_id"})
}

// moveTaskChannelIfDM relocates a DM-scoped task to the DM between its creator
// and newAssigneeID when the task's current ChannelID is a direct channel. It:
//  1. resolves the new DM channel via GetDirectChannel(creator, newAssignee);
//  2. posts a fresh card into the new DM;
//  3. copies existing comments (as new posts under the new card, preserving
//     author and original timestamp);
//  4. updates task.ChannelID + channel_post_id;
//  5. deletes the old card post (the old DM keeps its history but loses the
//     live card).
//
// Team-channel tasks (type O/P/G) are NOT moved — the channel is a shared
// surface and every assignee sees the card there. Returns (nil, nil) when the
// task is not DM-scoped (no move needed).
func (p *Plugin) moveTaskChannelIfDM(t *taskmodel.Task, newAssigneeID string) (*taskmodel.Task, error) {
	if t == nil || t.ChannelID == "" {
		return nil, nil
	}
	ch, err := p.API.GetChannel(t.ChannelID)
	if err != nil || ch == nil {
		return nil, fmt.Errorf("move channel: get channel %s: %w", t.ChannelID, err)
	}
	if ch.Type != mmmodel.ChannelTypeDirect {
		return nil, nil // team/group channel: shared surface, do not move
	}
	dm, err := p.API.GetDirectChannel(t.CreatorID, newAssigneeID)
	if err != nil || dm == nil {
		return nil, fmt.Errorf("move channel: open DM(%s,%s): %w", t.CreatorID, newAssigneeID, err)
	}
	if dm.Id == t.ChannelID {
		return nil, nil // already in the right DM
	}

	// Post a fresh card into the new DM.
	newPostID := p.postCard(dm.Id, t)
	if newPostID == "" {
		return nil, errors.New("move channel: post card failed")
	}

	// Copy comments under the new card root (best-effort: a failed copy is
	// logged but does not abort the move).
	p.copyCommentsUnderCard(t.ID, newPostID, dm.Id)

	// Update task: new home channel + new card post id.
	updated, uErr := p.taskService.UpdateChannel(t.ID, dm.Id, newPostID)
	if uErr != nil {
		return nil, fmt.Errorf("move channel: update task: %w", uErr)
	}

	// Delete the old card so the old DM stops rendering a live card; the old
	// comment posts (if any) remain in the old DM as history.
	if t.ChannelPostID != nil && *t.ChannelPostID != "" {
		if dErr := p.API.DeletePost(*t.ChannelPostID); dErr != nil {
			p.API.LogWarn("move channel: failed to delete old card",
				"task_id", t.ID, "old_post_id", *t.ChannelPostID, "error", dErr)
		}
	}
	return updated, nil
}

// copyCommentsUnderCard re-creates the task's existing comments as new posts
// threaded under newCardPostID in newChannelID. Each copy preserves the
// original author and created-at timestamp (plugin posts run system-context,
// so impersonating the author is allowed). Best-effort: per-copy failures are
// logged and skipped so one bad comment can't block the move.
func (p *Plugin) copyCommentsUnderCard(taskID, newCardPostID, newChannelID string) {
	comments, err := p.taskService.ListComments(taskID)
	if err != nil {
		p.API.LogWarn("move channel: list comments failed", "task_id", taskID, "error", err)
		return
	}
	for _, c := range comments {
		old, gErr := p.API.GetPost(c.PostID)
		if gErr != nil || old == nil {
			p.API.LogDebug("move channel: skip unreadable comment", "post_id", c.PostID)
			continue
		}
		post := &mmmodel.Post{
			UserId:    c.AuthorID,
			ChannelId: newChannelID,
			RootId:    newCardPostID,
			Message:   old.Message,
			CreateAt:  old.CreateAt,
			Type:      mmmodel.PostTypeDefault,
		}
		if _, pErr := p.API.CreatePost(post); pErr != nil {
			p.API.LogWarn("move channel: copy comment failed",
				"task_id", taskID, "old_post_id", c.PostID, "error", pErr)
		}
	}
}

// deleteAssignee handles DELETE /tasks/:id/assignee (clears the assignee). No
// notification is sent on unassign.
func (p *Plugin) deleteAssignee(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if _, ok := p.loadTaskForManage(w, id, currentUserID(r)); !ok {
		return
	}
	updated, _, err := p.taskService.Assign(currentUserID(r), id, "")
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.writeJSON(w, updated)

	// Refresh the interactive card so the Assignee field is cleared.
	p.updateTaskCards(updated)

	// Real-time: assignee cleared (#32).
	p.broadcastTaskUpdated(updated, []string{"assignee_id"})
}

// createSubtaskRequest is the JSON body for POST /tasks/:id/subtasks.
type createSubtaskRequest struct {
	Summary    string `json:"summary"`
	AssigneeID string `json:"assignee_id"`
	Due        *int64 `json:"due"`
}

// createSubtask handles POST /tasks/:id/subtasks. The subtask inherits the
// parent's ChannelID and (as default) the parent's assignee; an explicit
// assignee_id or due overrides the inherited defaults. Requires modify
// permission on the parent (creator or current assignee). After creation the
// parent's interactive card is refreshed to reflect the new subtask count.
func (p *Plugin) createSubtask(w http.ResponseWriter, r *http.Request) {
	parentID := mux.Vars(r)["id"]
	actorID := currentUserID(r)

	parent, err := p.taskService.Get(parentID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permission.CanUserManageTask(actorID, parent) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to add subtasks to this task")
		return
	}

	var req createSubtaskRequest
	if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	created, err := p.taskService.Create(task.CreateInput{
		Summary:      strings.TrimSpace(req.Summary),
		CreatorID:    actorID,
		AssigneeID:   req.AssigneeID,
		DueAt:        req.Due,
		ParentTaskID: parentID,
	})
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "required"):
			p.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, task.ErrParentNotFound):
			p.writeError(w, http.StatusNotFound, "parent task not found")
		default:
			p.API.LogError("Failed to create subtask",
				"parent_id", parentID, "actor", actorID, "error", err.Error())
			p.writeError(w, http.StatusInternalServerError, "failed to create subtask")
		}
		return
	}

	// Post the subtask's card as a thread reply inside the parent's card
	// thread. Under the all-channel model the parent's single card lives in
	// parent.ChannelPostID (the home channel). A parent with no tracked card
	// yet falls back to a top-level card in the inherited channel.
	var subtaskPostID string
	switch {
	case parent.ChannelPostID != nil && *parent.ChannelPostID != "":
		subtaskPostID = p.postCardReply(*parent.ChannelPostID, parent.ChannelID, created)
	default:
		if created.ChannelID != "" {
			subtaskPostID = p.postCard(created.ChannelID, created)
		}
	}
	if subtaskPostID != "" {
		updated, uerr := p.taskService.SetPostIDs(created.ID, subtaskPostID)
		if uerr != nil {
			p.API.LogError("Failed to persist subtask card post ID",
				"task_id", created.ID, "error", uerr)
		} else if updated != nil {
			created = updated
		}
	}

	// Refresh the parent's card so its subtask progress reflects the new child.
	p.updateTaskCards(parent)

	w.WriteHeader(http.StatusCreated)
	p.writeJSON(w, created)

	// Real-time: the new subtask appears in Quick List, and the parent's progress
	// badge updates (#32).
	p.broadcastTaskUpdated(created, []string{"created"})
	p.broadcastTaskUpdated(parent, []string{"subtasks"})
}

// listSubtasks handles GET /tasks/:id/subtasks, returning the parent's direct
// subtasks sorted by creation order.
func (p *Plugin) listSubtasks(w http.ResponseWriter, r *http.Request) {
	parentID := mux.Vars(r)["id"]
	parent, err := p.taskService.Get(parentID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permission.CanUserListTask(currentUserID(r), parent, p.channelMembership()) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to view this task")
		return
	}
	subs, err := p.taskService.ListSubtasks(parentID)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.writeJSON(w, subs)
}

// createCommentRequest is the JSON body for POST /tasks/:id/comments.
type createCommentRequest struct {
	Content   string `json:"content"`
	ChannelID string `json:"channel_id"` // Change B: the channel the viewer is acting from, so the comment threads under the card IN that channel (shared-task fix).
}

// createComment handles POST /tasks/:id/comments. The authenticated user is the
// commenter; anyone who may view the task may comment on it. A new comment DMs
// the task participants (creator + current assignee), excluding the commenter.
func (p *Plugin) createComment(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	actorID := currentUserID(r)

	// View permission gates commenting: anyone who can view may comment.
	t, err := p.taskService.Get(taskID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permission.CanUserCommentTask(actorID, t, p.channelMembership()) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to comment on this task")
		return
	}

	var req createCommentRequest
	if err = json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		p.writeError(w, http.StatusBadRequest, "comment content is required")
		return
	}

	// Comment-as-thread: the comment is a reply in the task's card thread.
	// Resolve the card post that backs this task (its root + channel) and reply
	// under it. The card post id is already tracked in task_posts / on the task
	// (ChannelPostID); we just need ONE card the commenter can post
	// into. Prefer a card whose channel the commenter is a member of; otherwise
	// fall back to the first tracked card. The comment goes into THAT card's
	// channel, so channel + root stay consistent and the reply is always in the
	// fetched thread.
	rootID, channelID, ok, err := p.commentRoot(taskID, t, actorID, req.ChannelID)
	if err != nil {
		p.API.LogError("createComment: transient error resolving card root", "task_id", taskID, "error", err)
		p.writeError(w, http.StatusInternalServerError, "failed to resolve task card")
		return
	}
	if !ok {
		p.API.LogWarn("createComment: task has no card root; rejecting comment",
			"task_id", taskID, "requested_channel_id", req.ChannelID)
		p.writeError(w, http.StatusBadRequest, "task has no card to comment on")
		return
	}
	commentPost := &mmmodel.Post{
		// Change A: the comment post is authored by the HUMAN commenter, not the
		// bot. This fixes channel attribution (the real person appears as the
		// sender, not task_bot). Card posts (postCard) STAY bot-owned;
		// only comment posts become human-authored. AuthorID is still snapshotted
		// on task_comments (equals post.UserId now).
		UserId:    actorID,
		ChannelId: channelID,
		RootId:    rootID,
		Message:   req.Content,
	}
	created, appErr := p.API.CreatePost(commentPost)
	if appErr != nil {
		p.API.LogError("createComment: CreatePost failed",
			"task_id", taskID, "actor_id", actorID,
			"root_id", rootID, "post_channel_id", channelID,
			"user_id", actorID, "error", appErr.Error(),
			"status_code", appErr.StatusCode)
		p.writeError(w, appErr.StatusCode, "failed to create comment post: "+appErr.Error())
		return
	}
	if created == nil {
		// Defensive: the plugin API contract returns a non-nil post on success,
		// but a nil post with a nil error has been observed in production (the
		// caller would otherwise dereference created.Id below and panic the
		// plugin process, surfacing as an RPC EOF / crash loop). Log loudly and
		// fail the request cleanly instead of crashing.
		p.API.LogError("createComment: CreatePost returned a nil post with no error",
			"task_id", taskID, "actor_id", actorID, "channel_id", channelID, "root_id", rootID)
		p.writeError(w, http.StatusInternalServerError, "failed to create comment post")
		return
	}

	comment, ev, err := p.taskService.LinkComment(taskID, created.Id, actorID)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			p.writeError(w, http.StatusNotFound, "task not found")
		default:
			p.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// DM the task participants (creator + assignee), excluding the commenter.
	if p.notifier != nil {
		p.notifier.NotifyCommented(notification.TaskSummary{ID: t.ID, Summary: t.Summary},
			ev.AuthorID, ev.CreatorID, ev.AssigneeID)
	}

	// Refresh the card so a "comments" indicator stays current.
	p.updateTaskCards(t)

	w.WriteHeader(http.StatusCreated)
	p.writeJSON(w, toCommentResponse(comment, created.Message, false))

	// Real-time: a new comment arrived — Task Detail comment list refreshes (#32).
	// Reload the task: AddComment bumped UpdatedAt, so the pre-comment snapshot `t`
	// has a stale seq that the webapp would drop as out-of-order.
	fresh, _ := p.taskService.Get(taskID)
	if fresh == nil {
		fresh = t
	}
	p.broadcastTaskUpdated(fresh, []string{"comment"})
}

// commentResponse is the transport-only shape returned by listComments: the DB
// row fields (id/task_id/post_id/author_id/created_at) plus content resolved
// from the backing post's Message and a deleted flag for out-of-band deleted
// posts. These extra fields are NOT persisted in task_comments (Hướng A:
// post.Message is the single source of truth for content).
type commentResponse struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	PostID    string `json:"post_id"`
	AuthorID  string `json:"author_id"`
	CreatedAt int64  `json:"created_at"`
	Content   string `json:"content"`
	Deleted   bool   `json:"deleted"`
}

// toCommentResponse builds the transport commentResponse from a task_comments
// row snapshot plus content/deleted resolved from the backing post. Shared by
// listComments (content from a batched thread fetch) and createComment
// (content from the just-created post). AuthorID/ID/... come from the row
// snapshot, NOT re-derived from the post, so audit survives post deletion.
func toCommentResponse(c taskmodel.TaskComment, content string, deleted bool) commentResponse {
	return commentResponse{
		ID:        c.ID,
		TaskID:    c.TaskID,
		PostID:    c.PostID,
		AuthorID:  c.AuthorID,
		CreatedAt: c.CreatedAt,
		Content:   content,
		Deleted:   deleted,
	}
}

// listComments handles GET /tasks/:id/comments, returning comments newest-first
// with content resolved from the backing thread post and a deleted flag for
// comments whose post was removed out-of-band. Access-controlled: the caller
// must be permitted to view the task (CanUserCommentTask follows the view
// rule), so a personal task's thread can't be read by an outsider who only
// knows the id.
func (p *Plugin) listComments(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	actorID := currentUserID(r)

	t, err := p.taskService.Get(taskID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permission.CanUserCommentTask(actorID, t, p.channelMembership()) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to view this task's comments")
		return
	}

	comments, err := p.taskService.ListComments(taskID)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Resolve each comment's content directly from its backing post via GetPost.
	// The comment-as-thread design stores only the post_id link in task_comments;
	// the content lives in the Mattermost post. A direct GetPost per comment is
	// used (NOT GetPostThread over card roots): it is robust to root/channel
	// resolution mismatches and to thread-cache lag — the post is always
	// retrievable by its id.
	//
	// Error handling distinguishes a truly-missing post (deleted out-of-band;
	// AppError 404) from a transient backend failure (5xx/network). Only the
	// former yields deleted:true; a transient error aborts the whole request so
	// the caller can retry instead of silently showing comments as deleted.
	out := make([]commentResponse, len(comments))
	for i, c := range comments {
		post, pErr := p.API.GetPost(c.PostID)
		switch {
		case pErr == nil && post != nil:
			out[i] = toCommentResponse(c, post.Message, false)
		case pErr != nil && pErr.StatusCode == http.StatusNotFound:
			// The backing post was deleted out-of-band. Mark deleted but keep the
			// row so the feed shows the placeholder.
			out[i] = toCommentResponse(c, "", true)
		default:
			// Transient backend error (or an unexpected nil post without error).
			// Fail the request instead of rendering a live comment as deleted.
			p.API.LogError("listComments: transient error resolving comment post",
				"task_id", taskID, "comment_id", c.ID, "post_id", c.PostID,
				"status_code", func() int {
					if pErr != nil {
						return pErr.StatusCode
					}
					return 0
				}())
			p.writeError(w, http.StatusInternalServerError, "failed to load comments")
			return
		}
	}

	// Newest-first: descending created_at, descending id (ULID is monotonic) as
	// the deterministic tie-breaker. Reverses the store's ASC order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})

	p.writeJSON(w, out)
}

// listTaskEvents handles GET /tasks/:id/events, returning the task's audit
// trail (activity timeline) newest-first. Permission-gated by the view rule
// (same as comments): only a user who may view the task may read its history.
// The ?limit query caps the page (default 50).
func (p *Plugin) listTaskEvents(w http.ResponseWriter, r *http.Request) {
	taskID := mux.Vars(r)["id"]
	actorID := currentUserID(r)

	t, err := p.taskService.Get(taskID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permission.CanUserCommentTask(actorID, t, p.channelMembership()) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to view this task's events")
		return
	}

	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, parseErr := strconv.Atoi(q); parseErr == nil && n > 0 {
			limit = n
		}
	}

	events, err := p.taskService.ListTaskEvents(taskID, limit)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if events == nil {
		events = []taskmodel.TaskEvent{}
	}
	p.writeJSON(w, events)
}

// channelMembership returns a permission.ChannelMembershipChecker backed by the
// plugin API, or nil when membership checks cannot be performed (the permission
// helpers treat a nil checker as "not a member", so personal tasks stay private
// and channel tasks fall back to creator/assignee).
func (p *Plugin) channelMembership() permission.ChannelMembershipChecker {
	return channelMembershipChecker{api: p.API}
}

// loadTaskForManage fetches a task and enforces the "manage" permission
// (creator or assignee). It is presentation-layer plumbing shared by the write
// handlers (patch/status/assign/reminder/subtask). On error it writes the HTTP
// response and returns ok=false; the caller must return immediately.
func (p *Plugin) loadTaskForManage(w http.ResponseWriter, id, actorID string) (*taskmodel.Task, bool) {
	t, err := p.taskService.Get(id)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return nil, false
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	if !permission.CanUserManageTask(actorID, t) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to modify this task")
		return nil, false
	}
	return t, true
}

// handleCardAction handles the interactive chip callback (POST /api/v1/actions).
// Mattermost sends a PostActionIntegrationRequest with the user id, the source
// post id, and the context {action, task_id} the chip was built with.
//
// "status" cycles todo→in_progress→done→todo. "priority" cycles
// standard→important→urgent→standard. The source card is updated in the DB and
// every tracked post copy is refreshed. The HTTP response carries an `update`
// field with the freshly-rendered props so the Mattermost client re-renders
// the source post immediately — without it, the post in the user's view only
// refreshes on the next WebSocket event, which can lag.
func (p *Plugin) handleCardAction(w http.ResponseWriter, r *http.Request) {
	var req mmmodel.PostActionIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid action request")
		return
	}

	action, _ := req.Context["action"].(string)
	taskID, _ := req.Context["task_id"].(string)
	actorID := req.UserId
	if actorID == "" {
		p.writeError(w, http.StatusUnauthorized, "not authorized")
		return
	}
	if taskID == "" || action == "" {
		p.writeError(w, http.StatusBadRequest, "missing action or task_id")
		return
	}

	var (
		updated *taskmodel.Task
		errMsg  string
	)
	switch action {
	case string(actionStatus):
		t, err := p.taskService.Get(taskID)
		if err != nil {
			errMsg = "⚠️ Could not find task."
			break
		}
		if !permission.CanUserManageTask(actorID, t) {
			writeCardResponse(w, "You do not have permission to change this task's status.", nil)
			return
		}
		next := nextStatus(t.Status)
		if next == t.Status {
			// Cancelled is terminal in the cycle — nothing to do.
			writeCardResponse(w, "Reopen the task from Task Details.", nil)
			return
		}
		updated, err = p.taskService.SetStatus(actorID, taskID, next)
		if err != nil {
			errMsg = "⚠️ Could not update status: " + err.Error()
			break
		}
		// Refresh every tracked card copy (channel + DM) so all stay current.
		p.updateTaskCards(updated)
		if next == taskmodel.StatusDone || next == taskmodel.StatusCancelled {
			p.notifyTerminalStatus(updated, next, actorID)
		}
		p.broadcastTaskUpdated(updated, []string{"status"})

	case string(actionPriority):
		t, err := p.taskService.Get(taskID)
		if err != nil {
			errMsg = "⚠️ Could not find task."
			break
		}
		if !permission.CanUserManageTask(actorID, t) {
			writeCardResponse(w, "You do not have permission to change this task's priority.", nil)
			return
		}
		next := nextPriority(t.Priority)
		updated, err = p.taskService.Patch(actorID, taskID, task.PatchInput{
			UpdateFields: []string{"priority"},
			Priority:     &next,
		})
		if err != nil {
			errMsg = "⚠️ Could not update priority: " + err.Error()
			break
		}
		p.updateTaskCards(updated)
		p.broadcastTaskUpdated(updated, []string{"priority"})

	default:
		writeCardResponse(w, "Unknown action.", nil)
		return
	}

	if errMsg != "" {
		writeCardResponse(w, errMsg, nil)
		return
	}

	// Build the refreshed attachment for the response `update`. Mattermost's
	// action callback accepts {update: {props: {attachments: [...]}}} and
	// patches the source post's props in the client view immediately.
	attachment := p.renderCard(updated)
	writeCardResponse(w, "", map[string]any{
		"props": taskCardProps(updated, &attachment),
	})
}

// writeCardResponse responds with the JSON body Mattermost expects from an
// interactive action callback: {ephemeral_text, update}. ephemeralText empty
// keeps the interaction silent; update (when non-nil) is the {props: ...}
// patch Mattermost applies to the source post so it re-renders immediately.
func writeCardResponse(w http.ResponseWriter, ephemeralText string, update map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"ephemeral_text": ephemeralText}
	if update != nil {
		body["update"] = update
	}
	_ = json.NewEncoder(w).Encode(body)
}
