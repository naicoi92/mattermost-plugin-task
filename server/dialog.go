package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
)

// Dialog callback ids and field names for the Quick List and Task Detail
// Interactive Dialogs (issue #17). These dialogs are the mobile/fallback path
// for browsing and editing tasks (PLAN.md section 6.4).
const (
	dialogCallbackQuickList  = "task_quick_list"
	dialogCallbackTaskDetail = "task_detail"

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
func buildQuickListDialog(userID string, tasks []taskmodel.Task) model.Dialog {
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
func buildTaskDetailDialog(t *taskmodel.Task, subtaskDone, subtaskTotal int, recentComments []taskmodel.Comment) model.Dialog {
	intro := fmt.Sprintf("**Status:** %s", statusLabel(t.Status))
	if subtaskTotal > 0 {
		intro += fmt.Sprintf("  ·  **Subtasks:** %d/%d", subtaskDone, subtaskTotal)
	}
	if len(recentComments) > 0 {
		parts := make([]string, 0, len(recentComments)+1)
		parts = append(parts, "", "**Recent comments:**")
		for _, c := range recentComments {
			line := c.Content
			if len(line) > 80 {
				line = line[:79] + "…"
			}
			parts = append(parts, fmt.Sprintf("• %s", line))
		}
		intro += "\n\n" + strings.Join(parts, "\n")
	}

	dueDefault := ""
	if t.Due != nil {
		dueDefault = fmt.Sprintf("%d", *t.Due)
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

	// Due.
	if raw, ok := sub[dialogFieldTaskDue].(string); ok {
		if raw == "" {
			if current.Due != nil {
				out.Patch.UpdateFields = append(out.Patch.UpdateFields, "due")
				out.Patch.Due = nil
			}
		} else {
			var ms int64
			if _, err := fmt.Sscanf(raw, "%d", &ms); err != nil {
				return out, fmt.Errorf("due must be a numeric millisecond timestamp")
			}
			if current.Due == nil || *current.Due != ms {
				out.Patch.UpdateFields = append(out.Patch.UpdateFields, "due")
				out.Patch.Due = &ms
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
// task yields an error.
func (p *Plugin) openTaskDetailDialogFor(triggerID, taskID string) error {
	t, err := p.taskService.Get(taskID)
	if err != nil || t == nil {
		return fmt.Errorf("task %s not found", taskID)
	}
	dialog := buildTaskDetailDialog(t, 0, 0, nil)
	return p.openDialog(triggerID, dialog)
}

// openDialog wraps p.API.OpenInteractiveDialog, building the request with the
// plugin-relative callback URL for the dialog's callback id.
func (p *Plugin) openDialog(triggerID string, dialog model.Dialog) error {
	var callbackURL string
	switch dialog.CallbackId {
	case dialogCallbackQuickList:
		callbackURL = "/api/v1/dialogs/quicklist"
	case dialogCallbackTaskDetail:
		callbackURL = "/api/v1/dialogs/taskdetail"
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
		if _, err := p.taskService.Patch(taskID, update.Patch); err != nil {
			failed = "save fields"
		} else {
			applied = append(applied, "fields")
		}
	}
	if failed == "" && update.NewStatus != "" {
		if _, err := p.taskService.SetStatus(taskID, update.NewStatus); err != nil {
			failed = "change status"
		} else {
			applied = append(applied, "status")
		}
	}
	if failed == "" && update.AssigneeSet {
		if _, _, err := p.taskService.Assign(taskID, update.NewAssignee); err != nil {
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
		writeDialogResponse(w, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// manifestID is the plugin id from plugin.json, used to build dialog callback
// URLs.
const manifestID = "com.mattermost.plugin-task"
