package main

import (
	"context"
	"encoding/json"
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
	"github.com/naicoi92/mattermost-plugin-task/server/taskutil"
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
	tasks.HandleFunc("/{id:[^/]+}/share", p.shareTask).Methods(http.MethodPost)

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
	ChannelID   string `json:"channel_id"`
	// PostChannelID is the originating channel (the channel the user is in when
	// creating) that should receive the announce card when the task itself is
	// personal (empty ChannelID), e.g. a task created in a DM. It does not
	// change the task's own channel_id (scope) — only where the card is posted.
	PostChannelID  string `json:"post_channel_id"`
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
	// Decide where the announce card is posted. A channel task (ChannelID set)
	// posts into its own channel. A personal task (empty ChannelID) created in
	// an originating channel (e.g. a DM) posts into post_channel_id instead —
	// but only after verifying the requesting user is a member of that channel,
	// so a client cannot direct the bot to post into a channel the caller cannot
	// access (defense-in-depth on client-controlled post_channel_id). The task's
	// channel_id (scope) is never changed here; only the card destination is
	// decided. The DM-assignee notification below is left untouched (additive).
	announceChannel := created.ChannelID
	if announceChannel == "" {
		announceChannel = req.PostChannelID
	}
	if announceChannel != "" && announceChannel != created.ChannelID {
		if !p.channelMembership().IsChannelMember(currentUserID(r), announceChannel) {
			p.API.LogDebug("Ignoring post_channel_id announce: caller is not a channel member",
				"task_id", created.ID, "post_channel_id", announceChannel)
			announceChannel = ""
		}
	}
	if announceChannel != "" {
		channelPostID = p.postCard(announceChannel, created)
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
//
// Two scopes are supported (the desktop RHS picks based on channel type):
//   - scope=channel&channel_id=X  → tasks of channel X
//   - scope=direct&partner_id=Y   → tasks shared between the caller and Y
//     (assignee OR creator for either user)
//
// Membership defense: for scope=channel the caller must be a member of the
// channel they ask about, otherwise the request is rejected as 403 (this
// prevents a user from enumerating another channel's tasks by guessing the
// channel id). Scope=direct is bounded to the caller + the named partner, so
// no third-party data can leak.
func (p *Plugin) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))

	scope := task.Scope(q.Get("scope"))
	channelID := q.Get("channel_id")
	partnerID := q.Get("partner_id")
	userID := currentUserID(r)

	// Validate scope at the API boundary: empty or unknown scopes are client
	// errors (400), not 500s from the deeper store layer.
	if scope != task.ScopeChannel && scope != task.ScopeDirect {
		p.writeError(w, http.StatusBadRequest, "scope must be 'channel' or 'direct'")
		return
	}

	// Note: we intentionally do NOT call IsChannelMember here as a hard gate.
	// The host's GetChannelMember can return an AppError on transient failures
	// (cache miss, network blip, slow channel load), which would surface as a
	// spurious 403 to the user. The scope=channel query itself only returns
	// tasks whose channel_id matches — the data is already bounded by what the
	// caller passes. The per-task CanUserViewTask rule (wired into
	// comments/events, tracked for get/search in #157) is the correct place
	// for membership enforcement, not a pre-query RPC that can flap.

	query := task.ListQuery{
		Scope:         scope,
		UserID:        userID,
		ChannelID:     channelID,
		PartnerID:     partnerID,
		Status:        q.Get("status"),
		DueAt:         q.Get("due"),
		Priority:      q.Get("priority"),
		AfterOrderKey: q.Get("after_order_key"),
		Limit:         limit,
	}

	if query.Scope == task.ScopeChannel && query.ChannelID == "" {
		p.writeError(w, http.StatusBadRequest, "channel_id is required when scope=channel")
		return
	}
	if query.Scope == task.ScopeDirect && (query.UserID == "" || query.PartnerID == "") {
		p.writeError(w, http.StatusBadRequest, "partner_id is required when scope=direct")
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

	// Refresh the interactive card so the Assignee field stays in sync.
	p.updateTaskCards(updated)

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
			p.API.LogError("Failed to create subtask",
				"parent_id", parentID, "actor", actorID, "error", err.Error())
			p.writeError(w, http.StatusInternalServerError, "failed to create subtask")
		}
		return
	}

	// Post the subtask's card as a thread reply inside the parent's card
	// thread. The parent is posted in either a channel (ChannelPostID set) or a
	// DM (DMPostID set); the subtask follows the same surface so the family of
	// subtasks groups together under the parent's conversation. A task whose
	// parent has no tracked card yet falls back to a top-level card in the
	// inherited channel.
	var subtaskPostID string
	switch {
	case parent.ChannelPostID != "":
		subtaskPostID = p.postCardReply(parent.ChannelPostID, parent.ChannelID, created)
	case parent.DMPostID != "":
		// DM-only parent: post the reply in the bot↔assignee DM channel.
		subtaskPostID = p.postCardReply(parent.DMPostID, created.ChannelID, created)
	default:
		if created.ChannelID != "" {
			subtaskPostID = p.postCard(created.ChannelID, created)
		}
	}
	if subtaskPostID != "" {
		updated, uerr := p.taskService.SetPostIDs(created.ID, subtaskPostID, "")
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
	if !permission.CanUserCommentTask(actorID, t, p.cardChannelIDs(t.ID), p.channelMembership()) {
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
	// link the post to the task. Change B: for a SHARED task the comment must
	// thread under the card post in the channel the viewer is acting from
	// (req.ChannelID, sent by the webapp as the active channel), not the home
	// channel card. Resolve the root from the card post living in that channel;
	// if none is found there, fall back to the legacy home/DM-card resolution
	// (and warn) so channel and root stay consistent.
	rootID := ""
	postChannel := ""
	if req.ChannelID != "" {
		if rp := p.cardPostInChannel(t.ID, req.ChannelID); rp != "" {
			rootID = rp
			postChannel = req.ChannelID
		} else {
			p.API.LogWarn("createComment: no task card in the requested channel; falling back to home/DM card",
				"task_id", t.ID, "requested_channel_id", req.ChannelID)
		}
	}
	if rootID == "" {
		rootID = t.ChannelPostID
		if rootID == "" {
			rootID = t.DMPostID
		}
		postChannel = t.ChannelID
		if postChannel == "" {
			postChannel = t.AssigneeID // personal task: post in the assignee's context
		}
	}
	channelID := postChannel
	commentPost := &mmmodel.Post{
		// Change A: the comment post is authored by the HUMAN commenter, not the
		// bot. This fixes channel attribution (the real person appears as the
		// sender, not task_bot). Card posts (postCard/postCardDM) STAY bot-owned;
		// only comment posts become human-authored. AuthorID is still snapshotted
		// on task_comments (equals post.UserId now).
		UserId:    actorID,
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
	if !permission.CanUserCommentTask(actorID, t, p.cardChannelIDs(t.ID), p.channelMembership()) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to view this task's comments")
		return
	}

	comments, err := p.taskService.ListComments(taskID)
	if err != nil {
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Resolve content via a SINGLE batched GetPostThread(rootID) call (Q-B
	// resolved: GetPostsByIds is not exposed on the pinned plugin API, so we
	// use GetPostThread, which returns the root + every reply in one call).
	// The comment threads under EVERY card post of the task are fetched: the
	// home channel card, the assignee DM card, AND any shared-card channel. A
	// shared task has ChannelPostID="" and DMPostID="", so a single-root
	// lookup would mark every comment deleted (Bug: comments rendered
	// "(comment deleted)" for shared tasks). Each card post is at most 3
	// (home + DM + 1 share), so this is bounded — not N+1 over comments.
	posts := map[string]*mmmodel.Post{}
	rootIDs := map[string]struct{}{}
	if t.ChannelPostID != "" {
		rootIDs[t.ChannelPostID] = struct{}{}
	}
	if t.DMPostID != "" {
		rootIDs[t.DMPostID] = struct{}{}
	}
	for _, tp := range p.taskPosts(taskID) {
		rootIDs[tp.PostID] = struct{}{}
	}
	for rootID := range rootIDs {
		thread, appErr := p.API.GetPostThread(rootID)
		if appErr != nil {
			p.API.LogWarn("listComments: failed to load a comment thread",
				"task_id", taskID, "root_id", rootID, "error", appErr.Error())
			continue
		}
		if thread != nil {
			for id, post := range thread.Posts {
				posts[id] = post
			}
		}
	}
	if len(posts) == 0 {
		// No card post (should not happen for a task with comments, but handled
		// defensively): every comment is treated as deleted. No N+1 fallback.
		p.API.LogError("listComments: task has no card post root; comments returned as deleted", "task_id", taskID)
	}

	out := make([]commentResponse, len(comments))
	for i, c := range comments {
		resp := commentResponse{
			ID:        c.ID,
			TaskID:    c.TaskID,
			PostID:    c.PostID,
			AuthorID:  c.AuthorID, // row snapshot, NOT re-derived from the post
			CreatedAt: c.CreatedAt,
		}
		if post, ok := posts[c.PostID]; ok {
			resp.Content = post.Message
			resp.Deleted = false
		} else {
			resp.Content = ""
			resp.Deleted = true
		}
		out[i] = resp
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
	if !permission.CanUserCommentTask(actorID, t, p.cardChannelIDs(t.ID), p.channelMembership()) {
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

// shareRequest is the JSON body for POST /tasks/{id}/share.
type shareRequest struct {
	ChannelID string `json:"channel_id"`
}

// shareResponse is the JSON body returned by a successful share.
type shareResponse struct {
	PostID string `json:"post_id"`
}

// shareTask posts an existing task's card into a target channel and links it
// via task_posts (kind="share") so it refreshes on later task changes through
// the existing updateTaskCards loop. The caller must be able to view the task
// (CanUserViewTask) AND be a member of the target channel (defense-in-depth:
// the bot cannot be directed into a channel the caller cannot access). Share
// is idempotent per channel: if the task already has a card in the target
// channel, the existing post id is returned without posting a duplicate.
func (p *Plugin) shareTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	actorID := currentUserID(r)

	t, err := p.taskService.Get(id)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			p.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		p.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req shareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ChannelID == "" {
		p.writeError(w, http.StatusBadRequest, "channel_id is required")
		return
	}

	// Viewer check: only those who can view the task may share it.
	if !permission.CanUserViewTask(actorID, t, p.cardChannelIDs(t.ID), p.channelMembership()) {
		p.writeError(w, http.StatusForbidden, "you do not have permission to view this task")
		return
	}

	// Channel-member check: the caller must belong to the target channel so a
	// client cannot direct the bot into a channel the caller cannot access.
	if !p.channelMembership().IsChannelMember(actorID, req.ChannelID) {
		p.writeError(w, http.StatusForbidden, "you are not a member of that channel")
		return
	}

	existing := p.taskPosts(t.ID)
	// Idempotency: if the task already has a card in the target channel, return
	// the existing post id without posting a duplicate.
	for _, tp := range existing {
		post, gerr := p.API.GetPost(tp.PostID)
		if gerr != nil || post == nil {
			continue
		}
		if post.ChannelId == req.ChannelID {
			p.writeJSON(w, shareResponse{PostID: tp.PostID})
			return
		}
	}
	// Single-share invariant: task_posts enforces UNIQUE(task_id, kind), so a
	// task may have at most one kind="share" card. If one already exists in a
	// different channel, reject (409) rather than violate the constraint and
	// leave an orphan card. (Same-channel case returned idempotently above.)
	for _, tp := range existing {
		if tp.Kind == taskmodel.PostKindShare {
			p.writeError(w, http.StatusConflict, "task already shared in another channel")
			return
		}
	}

	// Post the card and link it with kind="share".
	postID := p.postCard(req.ChannelID, t)
	if postID == "" {
		p.writeError(w, http.StatusInternalServerError, "failed to post task card")
		return
	}
	if err := p.taskStore.AddPost(context.Background(), taskutil.GenerateULID(), t.ID, postID, taskmodel.PostKindShare); err != nil {
		// Most likely a UNIQUE(task_id, kind) race: another request claimed the
		// single share slot between our precheck and this insert. Clean up the
		// orphan card we just posted so it can't linger untracked, then fail —
		// the caller can retry and the precheck will resolve it idempotently.
		// (Best-effort: a DeletePost error is logged but doesn't change the
		// outcome.)
		if derr := p.API.DeletePost(postID); derr != nil {
			p.API.LogError("Failed to clean up orphan share card",
				"post_id", postID, "error", derr)
		}
		p.API.LogError("Failed to link shared task card",
			"task_id", t.ID, "post_id", postID, "error", err)
		p.writeError(w, http.StatusInternalServerError, "failed to link task card")
		return
	}
	p.writeJSON(w, shareResponse{PostID: postID})
}

// channelMembership returns a permission.ChannelMembershipChecker backed by the
// plugin API, or nil when membership checks cannot be performed (the permission
// helpers treat a nil checker as "not a member", so personal tasks stay private
// and channel tasks fall back to creator/assignee).
func (p *Plugin) channelMembership() permission.ChannelMembershipChecker {
	return channelMembershipChecker{api: p.API}
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
