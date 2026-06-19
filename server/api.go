package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/pkg/errors"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// initRouter initializes the HTTP router for the plugin.
func (p *Plugin) initRouter() *mux.Router {
	router := mux.NewRouter()

	// Middleware to require that the user is logged in
	router.Use(p.MattermostAuthorizationRequired)

	apiRouter := router.PathPrefix("/api/v1").Subrouter()

	apiRouter.HandleFunc("/hello", p.HelloWorld).Methods(http.MethodGet)

	// Task CRUD (issue #7).
	tasks := apiRouter.PathPrefix("/tasks").Subrouter()
	tasks.HandleFunc("", p.createTask).Methods(http.MethodPost)
	tasks.HandleFunc("", p.listTasks).Methods(http.MethodGet)
	tasks.HandleFunc("/{id:[^/]+}", p.getTask).Methods(http.MethodGet)
	tasks.HandleFunc("/{id:[^/]+}", p.patchTask).Methods(http.MethodPatch)
	tasks.HandleFunc("/{id:[^/]+}", p.deleteTask).Methods(http.MethodDelete)

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
		default:
			p.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
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
