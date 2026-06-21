package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// Dialog callback ids and field names for the Quick List, Task Detail and New
// Task Interactive Dialogs (issue #17; New Task added by #95). These dialogs
// are the mobile/fallback path for browsing, editing and creating tasks
// (PLAN.md section 6.4).
const (
	dialogCallbackQuickList  = "task_quick_list"
	dialogCallbackTaskDetail = "task_detail"
	dialogCallbackNewTask    = "task_new"

	dialogFieldScope    = "scope"
	dialogFieldStatus   = "status"
	dialogFieldDue      = "due_filter"
	dialogFieldTaskPick = "task_id"

	dialogFieldSummary     = "summary"
	dialogFieldDescription = "description"
	dialogFieldAssignee    = "assignee"
	dialogFieldTaskDue     = "due"
)

// topNTasksDefault is the default number of tasks shown in the Quick List
// dialog's task select when no config value is supplied. Issue #17: default 20.
const topNTasksDefault = 20

// buildQuickListDialog builds the Interactive Dialog for browsing tasks on
// mobile/fallback. It contains selects for scope, status, due filter, and a
// select listing the top N tasks (by OrderKey) for the user to pick; picking
// one opens the Task Detail dialog.
//
// state carries the user id so the submit handler can resolve "mine".
func buildQuickListDialog(userID string, tasks []*taskmodel.Task) model.Dialog {
	taskOptions := make([]*model.PostActionOptions, 0, len(tasks))
	for _, t := range tasks {
		label := t.Summary
		if len(label) > 60 {
			label = label[:59] + "…"
		}
		taskOptions = append(taskOptions, &model.PostActionOptions{
			Text:  fmt.Sprintf("%s · %s", label, statusLabel(t.Status)),
			Value: t.ID,
		})
	}

	return model.Dialog{
		CallbackId:  dialogCallbackQuickList,
		Title:       "Quick List",
		SubmitLabel: "Open task",
		State:       userID,
		Elements: []model.DialogElement{
			{
				DisplayName: "Scope",
				Name:        dialogFieldScope,
				Type:        "select",
				Default:     string(task.ScopeMine),
				Options: []*model.PostActionOptions{
					{Text: "My tasks", Value: string(task.ScopeMine)},
					{Text: "This channel", Value: string(task.ScopeChannel)},
					{Text: "All", Value: string(task.ScopeAll)},
				},
			},
			{
				DisplayName: "Status",
				Name:        dialogFieldStatus,
				Type:        "select",
				Optional:    true,
				Options: []*model.PostActionOptions{
					{Text: "To Do", Value: taskmodel.StatusTodo},
					{Text: "In Progress", Value: taskmodel.StatusInProgress},
					{Text: "Done", Value: taskmodel.StatusDone},
					{Text: "Cancelled", Value: taskmodel.StatusCancelled},
				},
			},
			{
				DisplayName: "Due",
				Name:        dialogFieldDue,
				Type:        "select",
				Optional:    true,
				Options: []*model.PostActionOptions{
					{Text: "Overdue", Value: "overdue"},
					{Text: "Today", Value: "today"},
					{Text: "This week", Value: "week"},
				},
			},
			{
				DisplayName: "Task",
				Name:        dialogFieldTaskPick,
				Type:        "select",
				Optional:    true,
				Options:     taskOptions,
				HelpText:    "Pick a task to open its details (top results only; use /task search for more).",
			},
		},
	}
}

// buildTaskDetailDialog builds the Interactive Dialog for viewing/editing a
// single task (mobile/fallback). Summary/description/status/assignee/due are
// editable; subtask progress and recent comments are shown read-only in the
// introduction text. Submit performs a PATCH.
//
// state carries the task id.
func buildTaskDetailDialog(t *taskmodel.Task, subtaskDone, subtaskTotal int, recentComments []string) model.Dialog {
	intro := fmt.Sprintf("**Status:** %s", statusLabel(t.Status))
	if subtaskTotal > 0 {
		intro += fmt.Sprintf("  ·  **Subtasks:** %d/%d", subtaskDone, subtaskTotal)
	}
	if len(recentComments) > 0 {
		parts := make([]string, 0, len(recentComments)+1)
		parts = append(parts, "", "**Recent comments:**")
		for _, line := range recentComments {
			if len(line) > 80 {
				line = line[:79] + "…"
			}
			parts = append(parts, fmt.Sprintf("• %s", line))
		}
		intro += "\n\n" + strings.Join(parts, "\n")
	}

	dueDefault := ""
	if t.DueAt != nil {
		dueDefault = fmt.Sprintf("%d", *t.DueAt)
	}

	return model.Dialog{
		CallbackId:       dialogCallbackTaskDetail,
		Title:            "Task detail",
		IntroductionText: intro,
		SubmitLabel:      "Save",
		State:            t.ID,
		Elements: []model.DialogElement{
			{
				DisplayName: "Summary",
				Name:        dialogFieldSummary,
				Type:        "text",
				Default:     t.Summary,
			},
			{
				DisplayName: "Status",
				Name:        dialogFieldStatus,
				Type:        "select",
				Default:     t.Status,
				Options: []*model.PostActionOptions{
					{Text: "To Do", Value: taskmodel.StatusTodo},
					{Text: "In Progress", Value: taskmodel.StatusInProgress},
					{Text: "Done", Value: taskmodel.StatusDone},
					{Text: "Cancelled", Value: taskmodel.StatusCancelled},
				},
			},
			{
				DisplayName: "Assignee",
				Name:        dialogFieldAssignee,
				Type:        "select",
				DataSource:  "users",
				Default:     t.AssigneeID,
				Optional:    true,
			},
			{
				DisplayName: "Due (ms timestamp)",
				Name:        dialogFieldTaskDue,
				Type:        "text",
				SubType:     "number",
				Default:     dueDefault,
				Optional:    true,
				Placeholder: "e.g. 1700000000000",
			},
			{
				DisplayName: "Description",
				Name:        dialogFieldDescription,
				Type:        "textarea",
				Default:     t.Description,
				Optional:    true,
			},
		},
	}
}

// parseTaskDetailSubmission extracts a PATCH-shaped update from a Task Detail
// dialog submission. status is validated; due is parsed when present (empty
// clears it). Returns the PatchInput, an optional new status ("" when
// unchanged), and the assignee change. NewAssignee/AssigneeSet together
// express the change: AssigneeSet=true means the field changed (including a
// clear to ""), so the dialog can unassign; false means unchanged.
type taskDetailUpdate struct {
	Patch       task.PatchInput
	NewStatus   string // "" => unchanged
	NewAssignee string
	AssigneeSet bool
}

func parseTaskDetailSubmission(sub map[string]any, current *taskmodel.Task) (taskDetailUpdate, error) {
	out := taskDetailUpdate{}

	// Summary.
	if s, ok := sub[dialogFieldSummary].(string); ok && s != current.Summary {
		trimmed := s
		out.Patch.UpdateFields = append(out.Patch.UpdateFields, "summary")
		out.Patch.Summary = &trimmed
	}

	// Description.
	if d, ok := sub[dialogFieldDescription].(string); ok && d != current.Description {
		v := d
		out.Patch.UpdateFields = append(out.Patch.UpdateFields, "description")
		out.Patch.Description = &v
	}

	// Status (handled via SetStatus, not Patch).
	if st, ok := sub[dialogFieldStatus].(string); ok && st != current.Status {
		if !taskmodel.IsValidStatus(st) {
			return out, fmt.Errorf("invalid status %q", st)
		}
		out.NewStatus = st
	}

	// Due. Use strconv.ParseInt (not fmt.Sscanf) so a trailing non-numeric
	// suffix is rejected rather than silently accepted as a prefix — see
	// TestParseTaskDetailSubmission_NumericPrefixSuffixRejected.
	if raw, ok := sub[dialogFieldTaskDue].(string); ok {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			if current.DueAt != nil {
				out.Patch.UpdateFields = append(out.Patch.UpdateFields, "due")
				out.Patch.DueAt = nil
			}
		} else {
			ms, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return out, fmt.Errorf("due must be a numeric millisecond timestamp")
			}
			if current.DueAt == nil || *current.DueAt != ms {
				out.Patch.UpdateFields = append(out.Patch.UpdateFields, "due")
				out.Patch.DueAt = &ms
			}
		}
	}

	// Assignee (handled via Assign, not Patch). A distinct AssigneeSet flag
	// distinguishes "user cleared the assignee" (empty string, set) from
	// "unchanged" (no submission), so the dialog can unassign.
	if a, ok := sub[dialogFieldAssignee].(string); ok {
		if a != current.AssigneeID {
			out.NewAssignee = a
			out.AssigneeSet = true
		}
	}

	return out, nil
}

// openTaskDetailDialogFor loads a task and opens the Task Detail dialog for the
// user (used by the Quick List "Open task" submit). It is best-effort: a missing
// task yields an error. The dialog's read-only intro shows live subtask progress
// and the most recent comments (issue #25).
func (p *Plugin) openTaskDetailDialogFor(triggerID, taskID string) error {
	t, err := p.taskService.Get(taskID)
	if err != nil || t == nil {
		return fmt.Errorf("task %s not found", taskID)
	}
	done, total := p.subtaskProgress(taskID)
	recent := p.recentComments(taskID, detailCommentsLimit)
	dialog := buildTaskDetailDialog(t, done, total, recent)
	return p.openDialog(triggerID, dialog)
}

// openQuickListDialogFor builds and opens the Quick List Interactive Dialog for
// the user, scoped/filtered per the command arguments (#97). It runs the same
// ListQuery the text fallback uses, then renders the top-N tasks as a select
// so the user can pick one to open in the Task Detail dialog. Returns an error
// when the dialog can't be opened (caller falls back to the text list).
func (p *Plugin) openQuickListDialogFor(triggerID, userID, scope, channelID, status, due string) error {
	tasks, err := p.taskService.List(task.ListQuery{
		Scope:     task.Scope(scope),
		UserID:    userID,
		ChannelID: channelID,
		Status:    status,
		DueAt:     due,
		Limit:     topNTasksDefault,
	})
	if err != nil {
		return err
	}
	dialog := buildQuickListDialog(userID, tasks)
	return p.openDialog(triggerID, dialog)
}

// detailCommentsLimit is the maximum number of recent comments shown in the Task
// Detail dialog introduction (kept short for the mobile/fallback view).
const detailCommentsLimit = 3

// openDialog wraps p.API.OpenInteractiveDialog, building the request with the
// plugin-relative callback URL for the dialog's callback id.
func (p *Plugin) openDialog(triggerID string, dialog model.Dialog) error {
	var callbackURL string
	switch dialog.CallbackId {
	case dialogCallbackQuickList:
		callbackURL = "/api/v1/dialogs/quicklist"
	case dialogCallbackTaskDetail:
		callbackURL = "/api/v1/dialogs/taskdetail"
	case dialogCallbackNewTask:
		callbackURL = "/api/v1/dialogs/newtask"
	default:
		return fmt.Errorf("unknown dialog callback id %q", dialog.CallbackId)
	}
	siteURL := ""
	if cfg := p.API.GetConfig(); cfg != nil && cfg.ServiceSettings.SiteURL != nil {
		siteURL = *cfg.ServiceSettings.SiteURL
	}
	req := model.OpenDialogRequest{
		TriggerId: triggerID,
		URL:       fmt.Sprintf("%s/plugins/%s%s", siteURL, manifestID, callbackURL),
		Dialog:    dialog,
	}
	if appErr := p.API.OpenInteractiveDialog(req); appErr != nil {
		return appErr
	}
	return nil
}

// submitQuickListDialog handles the Quick List dialog submit callback. When the
// user picked a task, it responds with an ephemeral hint to view/edit it (the
// detail dialog opens via a fresh command, since a submit callback carries no
// trigger id to open a new dialog directly).
func (p *Plugin) submitQuickListDialog(w http.ResponseWriter, r *http.Request) {
	var req model.SubmitDialogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid dialog submission", http.StatusBadRequest)
		return
	}
	if req.UserId == "" {
		// Submit callbacks are mounted without the auth middleware (#109); the
		// actor is trusted via SubmitDialogRequest.UserId, so an empty value
		// means the callback was not issued by the server and must be rejected.
		http.Error(w, "Not authorized", http.StatusUnauthorized)
		return
	}
	taskID, _ := req.Submission[dialogFieldTaskPick].(string)
	if taskID == "" {
		writeDialogResponse(w, "Pick a task to view it.")
		return
	}
	writeDialogResponse(w, fmt.Sprintf("Use `/task show %s` to view the task details.", taskID))
}

// submitTaskDetailDialog handles the Task Detail dialog submit callback. It
// applies the partial update (PATCH), status transition, and assignee change,
// responding with an inline error on validation failure.
func (p *Plugin) submitTaskDetailDialog(w http.ResponseWriter, r *http.Request) {
	var req model.SubmitDialogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid dialog submission", http.StatusBadRequest)
		return
	}
	if req.UserId == "" {
		// Submit callbacks are mounted without the auth middleware (#109); the
		// actor is trusted via SubmitDialogRequest.UserId, so an empty value
		// means the callback was not issued by the server and must be rejected.
		http.Error(w, "Not authorized", http.StatusUnauthorized)
		return
	}
	taskID := req.State
	current, err := p.taskService.Get(taskID)
	if err != nil || current == nil {
		writeDialogResponse(w, fmt.Sprintf("Task %s not found.", taskID))
		return
	}

	update, err := parseTaskDetailSubmission(req.Submission, current)
	if err != nil {
		writeDialogResponse(w, err.Error())
		return
	}

	// Apply the requested changes. These are separate service calls (KVStore has
	// no multi-key transaction), so we track each one and report partial success
	// transparently rather than claiming total failure after some fields saved.
	// Order: PATCH fields first (least likely to fail), then status, then
	// assignee. A failure short-circuits and reports what already succeeded.
	var applied []string
	var failed string

	if len(update.Patch.UpdateFields) > 0 {
		if _, err := p.taskService.Patch(req.UserId, taskID, update.Patch); err != nil {
			failed = "save fields"
		} else {
			applied = append(applied, "fields")
		}
	}
	if failed == "" && update.NewStatus != "" {
		if _, err := p.taskService.SetStatus(req.UserId, taskID, update.NewStatus); err != nil {
			failed = "change status"
		} else {
			applied = append(applied, "status")
		}
	}
	if failed == "" && update.AssigneeSet {
		if _, _, err := p.taskService.Assign(req.UserId, taskID, update.NewAssignee); err != nil {
			failed = "change assignee"
		} else {
			applied = append(applied, "assignee")
		}
	}

	if failed != "" {
		// Partial success: some fields saved before the failure. Be explicit so
		// the user isn't misled into thinking nothing changed.
		msg := fmt.Sprintf("Failed to %s.", failed)
		if len(applied) > 0 {
			msg += fmt.Sprintf(" Already applied: %s.", strings.Join(applied, ", "))
		}
		writeDialogResponse(w, msg)
		return
	}

	writeDialogResponse(w, "")
}

// writeDialogResponse responds with a SubmitDialogResponse; a non-empty message
// re-opens the dialog showing it inline, an empty one closes the dialog.
func writeDialogResponse(w http.ResponseWriter, message string) {
	resp := model.SubmitDialogResponse{Error: message}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// openTaskDetailDialogRequest is the body for POST /dialogs/open-task-detail.
type openTaskDetailDialogRequest struct {
	TriggerID string `json:"trigger_id"`
	TaskID    string `json:"task_id"`
}

// openTaskDetailDialog opens the Task Detail dialog for a given task. Used by
// the webapp/command layer that has a trigger id (e.g. /task show on desktop, a
// post-menu action) to surface the mobile/fallback detail dialog.
func (p *Plugin) openTaskDetailDialog(w http.ResponseWriter, r *http.Request) {
	var req openTaskDetailDialogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.TriggerID == "" || req.TaskID == "" {
		http.Error(w, "trigger_id and task_id are required", http.StatusBadRequest)
		return
	}
	if err := p.openTaskDetailDialogFor(req.TriggerID, req.TaskID); err != nil {
		// HTTP opener endpoint (not a dialog submit callback): return a plain
		// HTTP error rather than a SubmitDialogResponse body.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// manifestID is the plugin id from plugin.json, used to build dialog callback
// URLs.
const manifestID = "com.mattermost.plugin-task"

// --- New Task dialog (#95) -------------------------------------------------
//
// `/task add "<summary>"` opens this dialog pre-filled with the summary so the
// user can fill assignee / due / description / scope before the task is
// created (PLAN.md §5.2, review #9). When no trigger id is available the
// command still creates the task immediately with just the summary so the
// flow never dead-ends.

// dialogFieldNewScope is the scope toggle in the New Task dialog (personal vs
// channel). Distinct from dialogFieldScope (the Quick List filter) so the two
// dialogs don't share a field name.
const dialogFieldNewScope = "new_scope"

// buildNewTaskDialog builds the Interactive Dialog for creating a task. The
// summary is pre-filled when the user typed `/task add "<summary>"`. The scope
// toggle defaults to channel when a channel context is present, personal
// otherwise (or when opened from a DM with the bot — PLAN §5.1.A). The state
// carries the chosen channel id (empty for a personal task) so the submit
// handler doesn't have to re-derive scope.
//
// isPersonal is derived internally from channelID == "" so callers can't pass
// an inconsistent (channelID, isPersonal) pair.
func buildNewTaskDialog(prefillSummary, channelID string) model.Dialog {
	isPersonal := channelID == ""
	scopeDefault := "channel"
	if isPersonal {
		scopeDefault = "personal"
	}

	// Scope options: the channel option is only meaningful when a channel
	// context exists. When there is no channel (e.g. a DM with the bot), only
	// the personal option is offered. The default option is listed first so the
	// pre-selected value sits at the top of the dropdown (matches user
	// expectation).
	var scopeOptions []*model.PostActionOptions
	if channelID != "" {
		scopeOptions = []*model.PostActionOptions{
			{Text: "Channel", Value: "channel"},
			{Text: "Personal", Value: "personal"},
		}
	} else {
		scopeOptions = []*model.PostActionOptions{
			{Text: "Personal", Value: "personal"},
		}
	}

	return model.Dialog{
		CallbackId:  dialogCallbackNewTask,
		Title:       "New Task",
		SubmitLabel: "Create",
		State:       channelID,
		Elements: []model.DialogElement{
			{
				DisplayName: "Summary",
				Name:        dialogFieldSummary,
				Type:        "text",
				SubType:     "text",
				Default:     prefillSummary,
				Placeholder: "Task summary",
			},
			{
				DisplayName: "Assignee",
				Name:        dialogFieldAssignee,
				Type:        "select",
				DataSource:  "users",
				Optional:    true,
			},
			{
				DisplayName: "Due (ms timestamp)",
				Name:        dialogFieldTaskDue,
				Type:        "text",
				SubType:     "number",
				Optional:    true,
				Placeholder: "e.g. 1700000000000",
			},
			{
				DisplayName: "Description",
				Name:        dialogFieldDescription,
				Type:        "textarea",
				Optional:    true,
			},
			{
				DisplayName: "Scope",
				Name:        dialogFieldNewScope,
				Type:        "select",
				Default:     scopeDefault,
				Options:     scopeOptions,
			},
		},
	}
}

// openNewTaskDialogFor opens the New Task dialog for the user. prefillSummary is
// the text the user typed after `/task add`. channelID is the originating
// channel (empty for a DM with the bot, which forces personal scope).
func (p *Plugin) openNewTaskDialogFor(triggerID, prefillSummary, channelID string) error {
	dialog := buildNewTaskDialog(prefillSummary, channelID)
	return p.openDialog(triggerID, dialog)
}

// submitNewTaskDialog handles the New Task dialog submit callback. It creates
// the task with the submitted fields (reusing the same taskService.Create +
// card-post path as POST /tasks), so behavior is identical to the REST API.
// Validation errors (empty summary, invalid due) are surfaced inline.
func (p *Plugin) submitNewTaskDialog(w http.ResponseWriter, r *http.Request) {
	var req model.SubmitDialogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid dialog submission", http.StatusBadRequest)
		return
	}
	if req.UserId == "" {
		// Submit callbacks are mounted without the auth middleware (#109); the
		// actor is trusted via SubmitDialogRequest.UserId, so an empty value
		// means the callback was not issued by the server and must be rejected.
		http.Error(w, "Not authorized", http.StatusUnauthorized)
		return
	}

	summary, _ := req.Submission[dialogFieldSummary].(string)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		writeDialogResponse(w, "Summary is required.")
		return
	}

	// Scope: when the user picks "channel", use the channel id carried in the
	// dialog state (the originating channel). Personal leaves ChannelID empty.
	scope, _ := req.Submission[dialogFieldNewScope].(string)
	channelID := req.State
	if scope != "channel" {
		channelID = ""
	}

	in := task.CreateInput{
		Summary:   summary,
		CreatorID: req.UserId,
		ChannelID: channelID,
	}

	if assigneeID, ok := req.Submission[dialogFieldAssignee].(string); ok && assigneeID != "" {
		in.AssigneeID = assigneeID
	}
	if desc, ok := req.Submission[dialogFieldDescription].(string); ok {
		in.Description = desc
	}
	// Parse the due timestamp strictly: strconv.ParseInt rejects a trailing
	// non-numeric suffix that fmt.Sscanf("%d") would silently accept (e.g.
	// "1700000000000abc"). Trim whitespace first so a stray space is not a
	// validation failure.
	if dueRaw, ok := req.Submission[dialogFieldTaskDue].(string); ok {
		dueRaw = strings.TrimSpace(dueRaw)
		if dueRaw != "" {
			ms, err := strconv.ParseInt(dueRaw, 10, 64)
			if err != nil || ms <= 0 {
				writeDialogResponse(w, "Due must be a numeric millisecond timestamp.")
				return
			}
			in.DueAt = &ms
		}
	}

	created, err := p.taskService.Create(in)
	if err != nil {
		// Surface validation errors inline; log unexpected ones server-side.
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "parent") {
			writeDialogResponse(w, err.Error())
			return
		}
		p.API.LogError("Failed to create task from New Task dialog", "user_id", req.UserId, "error", err)
		writeDialogResponse(w, "Failed to create the task. Please try again.")
		return
	}

	// Post the interactive task card (channel + DM) and persist the post ids,
	// mirroring POST /tasks so the experience is identical.
	var channelPostID, dmPostID string
	if created.ChannelID != "" {
		channelPostID = p.postCard(created.ChannelID, created)
	}
	if created.AssigneeID != "" && created.AssigneeID != created.CreatorID {
		dmPostID = p.postCardDM(created.AssigneeID, created)
	}
	if channelPostID != "" || dmPostID != "" {
		if updated, err := p.taskService.SetPostIDs(created.ID, channelPostID, dmPostID); err != nil {
			p.API.LogError("Failed to persist task card post IDs from dialog",
				"task_id", created.ID, "error", err)
		} else if updated != nil {
			created = updated
		}
	}

	// Real-time: the new task is visible to Quick List / Kanban clients (#32).
	p.broadcastTaskUpdated(created, []string{"created"})

	// When the task has no channel card (personal task, no assignee ≠ creator),
	// the dialog closes with no visible feedback. Send a brief ephemeral
	// confirmation so the user knows the task was created. For channel/DM
	// tasks the posted card is the feedback, so no ephemeral is needed.
	if channelPostID == "" && dmPostID == "" {
		p.sendEphemeralTaskCreated(req.UserId, created)
	}

	// Empty error closes the dialog.
	writeDialogResponse(w, "")
}

// sendEphemeralTaskCreated posts a brief ephemeral confirmation to userID so a
// New Task dialog submit that produces no visible card (personal task with no
// assignee ≠ creator) still gives the user feedback that the task was created.
// Best-effort: a send failure is logged but does not change the dialog result.
//
// The ephemeral post is routed through the user's DM channel with the bot:
// SendEphemeralPost needs a valid channel context to reach the client, so we
// resolve the DM channel first rather than leaving ChannelId empty (which
// would silently return nil).
func (p *Plugin) sendEphemeralTaskCreated(userID string, t *taskmodel.Task) {
	if userID == "" || t == nil {
		return
	}
	dm, err := p.API.GetDirectChannel(userID, p.botUserID)
	if err != nil || dm == nil {
		p.API.LogError("Failed to open DM for task-created confirmation",
			"user_id", userID, "task_id", t.ID, "error", err)
		return
	}
	post := &model.Post{
		UserId:    p.botUserID,
		ChannelId: dm.Id,
		Message:   fmt.Sprintf("➕ Task created: **%s** (`%s`)", t.Summary, t.ID),
	}
	if created := p.API.SendEphemeralPost(userID, post); created == nil {
		p.API.LogError("Failed to send ephemeral task-created confirmation",
			"user_id", userID, "task_id", t.ID)
	}
}

// openNewTaskDialogRequest is the body for POST /dialogs/open-new-task, the
// opener endpoint used by callers with a trigger id (e.g. a post-menu action).
type openNewTaskDialogRequest struct {
	TriggerID      string `json:"trigger_id"`
	PrefillSummary string `json:"prefill_summary"`
	ChannelID      string `json:"channel_id"`
}

// openNewTaskDialog opens the New Task dialog for a caller with a trigger id.
func (p *Plugin) openNewTaskDialog(w http.ResponseWriter, r *http.Request) {
	var req openNewTaskDialogRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.TriggerID == "" {
		http.Error(w, "trigger_id is required", http.StatusBadRequest)
		return
	}
	if err := p.openNewTaskDialogFor(req.TriggerID, req.PrefillSummary, req.ChannelID); err != nil {
		// This is an HTTP opener endpoint, not a dialog submit callback, so
		// return a plain HTTP error rather than a SubmitDialogResponse body.
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
