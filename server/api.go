package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/pkg/errors"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/notification"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// initRouter initializes the HTTP router for the plugin.
func (p *Plugin) initRouter() *mux.Router {
	router := mux.NewRouter()

	// Middleware to require that the user is logged in
	router.Use(p.MattermostAuthorizationRequired)

	apiRouter := router.PathPrefix("/api/v1").Subrouter()

	apiRouter.HandleFunc("/hello", p.HelloWorld).Methods(http.MethodGet)

	// Interactive task-card action callback (issue #15): Done/Cancel/Assign/
	// Subtask/Comment buttons POST here with context {action, task_id}.
	apiRouter.HandleFunc("/actions", p.handleCardAction).Methods(http.MethodPost)

	// Interactive Dialog submit callbacks + openers (issues #8, #17).
	apiRouter.HandleFunc("/dialogs/quicklist", p.submitQuickListDialog).Methods(http.MethodPost)
	apiRouter.HandleFunc("/dialogs/taskdetail", p.submitTaskDetailDialog).Methods(http.MethodPost)
	apiRouter.HandleFunc("/dialogs/open-task-detail", p.openTaskDetailDialog).Methods(http.MethodPost)

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
		Due:            req.Due,
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
		Due:           q.Get("due"),
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
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if t == nil {
		p.writeError(w, http.StatusNotFound, "task not found")
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

	updated, err := p.taskService.Patch(id, task.PatchInput{
		UpdateFields: req.UpdateFields,
		Summary:      req.Summary,
		Description:  req.Description,
		Due:          req.Due,
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
}

// deleteTask handles DELETE /tasks/:id (hard-delete cascade).
func (p *Plugin) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if err := p.taskService.Delete(id); err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

	updated, err := p.taskService.SetStatus(id, req.Status)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrNotFound):
			p.writeError(w, http.StatusNotFound, "task not found")
		case errors.Is(err, task.ErrInvalidStatus):
			p.writeError(w, http.StatusBadRequest, "invalid status")
		default:
			p.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Notify participants when a task reaches a terminal status (done/cancelled).
	// The actor is the authenticated user; they are excluded from the recipients.
	p.notifyTerminalStatus(updated, req.Status, currentUserID(r))

	// Refresh the interactive card (channel + DM) to reflect the new status.
	p.updateCard(updated.ChannelPostID, updated)
	p.updateCard(updated.DMPostID, updated)

	p.writeJSON(w, updated)
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

	updated, err := p.taskService.SetReminder(id, req.OffsetMS)
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
}

// deleteReminder handles DELETE /tasks/:id/reminder (turn reminders off).
func (p *Plugin) deleteReminder(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	updated, err := p.taskService.ClearReminder(id)
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

	updated, ev, err := p.taskService.Assign(id, req.UserID)
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
}

// deleteAssignee handles DELETE /tasks/:id/assignee (clears the assignee). No
// notification is sent on unassign.
func (p *Plugin) deleteAssignee(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	updated, _, err := p.taskService.Assign(id, "")
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	p.writeJSON(w, updated)
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
		updated, err := p.taskService.SetStatus(taskID, status)
		if err != nil {
			writeCardResponse(w, fmt.Sprintf("⚠️ Could not update task: %s", err.Error()))
			return
		}
		// Update the source post's card in place.
		p.updateCard(req.PostId, updated)
		p.updateCard(updated.ChannelPostID, updated)
		p.updateCard(updated.DMPostID, updated)
		p.notifyTerminalStatus(updated, status, req.UserId)
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
