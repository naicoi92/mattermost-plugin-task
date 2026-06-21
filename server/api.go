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

	// Interactive task-card action callback (issue #15): Done/Cancel/Assign/
	// Subtask/Comment buttons POST here with context {action, task_id}.
	apiRouter.HandleFunc("/actions", p.handleCardAction).Methods(http.MethodPost)

	// Dialog openers are HTTP endpoints called from clients that hold a
	// browser session, so they stay authenticated (issues #8, #17, #95).
	apiRouter.HandleFunc("/dialogs/open-task-detail", p.openTaskDetailDialog).Methods(http.MethodPost)
	apiRouter.HandleFunc("/dialogs/open-new-task", p.openNewTaskDialog).Methods(http.MethodPost)

	// Dialog SUBMIT callbacks arrive as internal server→plugin HTTP requests
	// issued by the Mattermost server when a user submits an Interactive
	// Dialog. They do NOT carry the Mattermost-User-ID header (that header is
	// only set on browser-session requests), so they cannot be mounted under
	// the auth middleware or they are rejected with 401 before the handler
	// runs (#109). The actor is trusted via model.SubmitDialogRequest.UserId
	// in the body, and each handler validates that field non-empty as a
	// hardening check.
	dialogCallbacks := router.PathPrefix("/api/v1/dialogs").Subrouter()
	dialogCallbacks.HandleFunc("/quicklist", p.submitQuickListDialog).Methods(http.MethodPost)
	dialogCallbacks.HandleFunc("/taskdetail", p.submitTaskDetailDialog).Methods(http.MethodPost)
	dialogCallbacks.HandleFunc("/newtask", p.submitNewTaskDialog).Methods(http.MethodPost)

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
	Summary        string `json:"summary"`
	Description    string `json:"description"`
	ChannelID      string `json:"channel_id"`
	AssigneeID     string `json:"assignee_id"`
	Due            *int64 `json:"due"`
	IsAllDay       bool   `json:"is_all_day"`
	ParentTaskID   string `json:"parent_task_id"`
	ReminderOffset *int64 `json:"reminder_offset"`
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
	})
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "required"):
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

	// Post the interactive task card: in the originating channel (if any) and
	// as a DM to the assignee (if any, and not the creator). Record the post
	// ids so the card can be updated on later status changes (issue #15).
	//
	// Atomicity strategy (M4-2): the task is committed FIRST (service.Create
	// is itself atomic — task + members + reminder + event in one tx). Card
	// posts and their task_posts linkage happen AFTER. A crash between Create
	// and the card posts leaves the task intact with no card (acceptable — the
	// task is the source of truth; cards are rebuildable). A crash after a
	// post but before AddPost leaves an orphan card pointing nowhere (also
	// acceptable — it can't @mention or act). This is preferred over wrapping
	// Create in an outer tx because CreatePost is a server RPC that can't
	// participate in a DB transaction.
	var channelPostID, dmPostID string
	if created.ChannelID != "" {
		channelPostID = p.postCard(created.ChannelID, created)
	}
	if created.AssigneeID != "" && created.AssigneeID != created.CreatorID {
		dmPostID = p.postCardDM(created.AssigneeID, created)
	}
	if channelPostID != "" || dmPostID != "" {
		updated, err := p.taskService.SetPostIDs(created.ID, channelPostID, dmPostID)
		if err != nil {
			// Card posts exist but the task linkage didn't persist; log so later
			// status transitions can't refresh the cards (investigatable).
			p.API.LogError("Failed to persist task card post IDs",
				"task_id", created.ID,
				"channel_post_id", channelPostID,
				"dm_post_id", dmPostID,
				"error", err)
		} else if updated != nil {
			created = updated
		}
	}

	w.WriteHeader(http.StatusCreated)
	p.writeJSON(w, created)

	// Real-time: a new task is visible to Quick List / Kanban clients (#32).
	p.broadcastTaskUpdated(created, []string{"created"})
}

// listTasks handles GET /tasks with scope/status/due/cursor query params.
func (p *Plugin) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	query := task.ListQuery{
		Scope:         task.Scope(q.Get("scope")),
		UserID:        currentUserID(r),
		ChannelID:     q.Get("channel_id"),
		Status:        q.Get("status"),
		DueAt:         q.Get("due"),
		AfterOrderKey: q.Get("after_order_key"),
		Limit:         limit,
	}

	if query.Scope == task.ScopeChannel && query.ChannelID == "" {
		p.writeError(w, http.StatusBadRequest, "channel_id is required when scope=channel")
		return
	}
	if query.Status != "" && !taskmodel.IsValidStatus(query.Status) {
		p.writeError(w, http.StatusBadRequest, "invalid status")
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
}

// patchTask handles PATCH /tasks/:id.
func (p *Plugin) patchTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
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
	})
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.writeJSON(w, updated)

	// Real-time: summary/description/due/is_all_day changed (#32).
	p.broadcastTaskUpdated(updated, req.UpdateFields)
}

// deleteTask handles DELETE /tasks/:id (hard-delete cascade).
func (p *Plugin) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	// Snapshot the task before deleting so the real-time event can target the
	// right recipients (creator/assignee/channel) and clients can drop it.
	snapshot, _ := p.taskService.Get(id)

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
		case errors.As(err, &task.ErrOpenSubtasks{}):
			// Parent-done guard: clear, actionable message listing the open subtasks.
			p.writeError(w, http.StatusConflict, err.Error())
		default:
			p.writeError(w, http.StatusInternalServerError, err.Error())
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

	// DM the newly assigned user (skipped when assignee == creator). No DM on
	// unassign, and no DM to the previous assignee (PLAN §7).
	if p.notifier != nil {
		p.notifier.NotifyAssigned(ev.NewAssigneeID, ev.CreatorID, notification.TaskSummary{
			ID: updated.ID, Summary: updated.Summary,
		})
	}
	p.writeJSON(w, updated)

	// Real-time: assignee changed — Quick List "My Tasks" + avatars refresh (#32).
	p.broadcastTaskUpdated(updated, []string{"assignee_id"})
}

// deleteAssignee handles DELETE /tasks/:id/assignee (clears the assignee). No
// notification is sent on unassign.
func (p *Plugin) deleteAssignee(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
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
	if !permission.CanUserModifyTask(actorID, parent) {
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
			p.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Post the subtask's own card in the inherited channel and DM the assignee
	// when distinct from the creator. The subtask card is independent so its own
	// status changes can update it later.
	var channelPostID, dmPostID string
	if created.ChannelID != "" {
		channelPostID = p.postCard(created.ChannelID, created)
	}
	if created.AssigneeID != "" && created.AssigneeID != created.CreatorID {
		dmPostID = p.postCardDM(created.AssigneeID, created)
	}
	if channelPostID != "" || dmPostID != "" {
		updated, uerr := p.taskService.SetPostIDs(created.ID, channelPostID, dmPostID)
		if uerr != nil {
			p.API.LogError("Failed to persist subtask card post IDs",
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
	subs, err := p.taskService.ListSubtasks(parentID)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.writeJSON(w, subs)
}

// createCommentRequest is the JSON body for POST /tasks/:id/comments.
type createCommentRequest struct {
	Content string `json:"content"`
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

	// Comment-as-thread: create the reply post in the task's card thread, then
	// link the post to the task. The post roots under the task's channel card
	// (or DM card as fallback) so replies thread under the card.
	rootID := t.ChannelPostID
	if rootID == "" {
		rootID = t.DMPostID
	}
	channelID := t.ChannelID
	if channelID == "" {
		channelID = t.AssigneeID // personal task: post in the assignee's context
	}
	commentPost := &mmmodel.Post{
		UserId:    p.botUserID,
		ChannelId: channelID,
		RootId:    rootID,
		Message:   req.Content,
	}
	created, appErr := p.API.CreatePost(commentPost)
	if appErr != nil {
		p.writeError(w, appErr.StatusCode, "failed to create comment post: "+appErr.Error())
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
	p.writeJSON(w, comment)

	// Real-time: a new comment arrived — Task Detail comment list refreshes (#32).
	// Reload the task: AddComment bumped UpdatedAt, so the pre-comment snapshot `t`
	// has a stale seq that the webapp would drop as out-of-order.
	fresh, _ := p.taskService.Get(taskID)
	if fresh == nil {
		fresh = t
	}
	p.broadcastTaskUpdated(fresh, []string{"comment"})
}

// listComments handles GET /tasks/:id/comments, returning comments sorted by
// creation time. Access-controlled: the caller must be permitted to view the
// task (CanUserCommentTask follows the view rule), so a personal task's thread
// can't be read by an outsider who only knows the id.
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
	// Enforce ordering defensively: the service returns ULID (creation) order,
	// but this handler guarantees it regardless of the underlying contract by
	// sorting by CreatedAt with the comment ID as a deterministic tie-breaker.
	sort.Slice(comments, func(i, j int) bool {
		if comments[i].CreatedAt != comments[j].CreatedAt {
			return comments[i].CreatedAt < comments[j].CreatedAt
		}
		return comments[i].ID < comments[j].ID
	})
	p.writeJSON(w, comments)
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

// handleCardAction handles the interactive task-card button callback
// (POST /api/v1/actions). Mattermost sends a PostActionIntegrationRequest with
// the user, channel, post, and context {action, task_id}.
//
// Done/Cancel apply a status transition; Assign/Subtask/Comment respond with an
// ephemeral hint (their full dialog flows land with #8/#17). The response is a
// JSON body Mattermost interprets to update the source post or show ephemeral
// text.
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

	switch action {
	case string(actionDone), string(actionCancel):
		status := taskmodel.StatusDone
		if action == string(actionCancel) {
			status = taskmodel.StatusCancelled
		}
		updated, err := p.taskService.SetStatus(actorID, taskID, status)
		if err != nil {
			writeCardResponse(w, fmt.Sprintf("⚠️ Could not update task: %s", err.Error()))
			return
		}
		// Update the source post's card in place, then refresh all tracked
		// cards (channel/DM/any future location) so every copy stays current.
		p.updateCard(req.PostId, updated)
		p.updateTaskCards(updated)
		p.notifyTerminalStatus(updated, status, req.UserId)
		// Real-time: status changed via the interactive card (#32).
		p.broadcastTaskUpdated(updated, []string{"status"})
		// Empty ephemeral => Mattermost updates the post and shows nothing extra.
		writeCardResponse(w, "")
	case string(actionAssign), string(actionSubtask), string(actionComment):
		writeCardResponse(w, "Use the /task command for this action (interactive dialogs arrive soon).")
	default:
		writeCardResponse(w, "Unknown action.")
	}
}

// writeCardResponse responds with the JSON body Mattermost expects from an
// interactive action callback: {ephemeral_text}. An empty string updates the
// post without extra text.
func writeCardResponse(w http.ResponseWriter, ephemeralText string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ephemeral_text": ephemeralText})
}
