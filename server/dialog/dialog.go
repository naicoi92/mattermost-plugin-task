// Package dialog builds Mattermost Interactive Dialogs (OpenInteractiveDialog)
// and handles their submit callbacks for the Task plugin. Interactive Dialogs
// are cross-platform (desktop + mobile), so they are the primary input path for
// task creation on mobile and a fallback on desktop (see PLAN.md section 6.4).
package dialog

import (
	"github.com/mattermost/mattermost/server/public/model"
)

// Callback IDs identify which dialog a submit callback belongs to.
const (
	CallbackTaskCreate = "task_create"
)

// Field names used in the New Task dialog elements and the submit submission map.
const (
	FieldSummary     = "summary"
	FieldDescription = "description"
	FieldAssignee    = "assignee"
	FieldDue         = "due"
	FieldScope       = "scope"
)

// NewTaskDialog builds the Interactive Dialog definition for creating a task.
// prefillSummary seeds the summary field (e.g. from /task add "<summary>" or a
// message action). The dialog collects summary (required), assignee (user
// picker), due (ms timestamp string), description, and scope.
//
// state carries the originating channel id so the submit handler knows where to
// scope the task (scope=channel) or to leave it personal (scope=personal).
func NewTaskDialog(prefillSummary, channelID string) model.Dialog {
	return model.Dialog{
		CallbackId:  CallbackTaskCreate,
		Title:       "New Task",
		SubmitLabel: "Create",
		Elements: []model.DialogElement{
			{
				DisplayName: "Summary",
				Name:        FieldSummary,
				Type:        "text",
				Default:     prefillSummary,
				Placeholder: "What needs to be done?",
			},
			{
				DisplayName: "Assignee",
				Name:        FieldAssignee,
				Type:        "select",
				DataSource:  "users",
				Optional:    true,
			},
			{
				DisplayName: "Due (ms timestamp)",
				Name:        FieldDue,
				Type:        "text",
				SubType:     "number",
				Optional:    true,
				Placeholder: "e.g. 1700000000000",
			},
			{
				DisplayName: "Description",
				Name:        FieldDescription,
				Type:        "textarea",
				Optional:    true,
			},
			{
				DisplayName: "Scope",
				Name:        FieldScope,
				Type:        "select",
				Optional:    true,
				Options: []*model.PostActionOptions{
					{Text: "Personal (just me)", Value: ScopePersonal},
					{Text: "This channel", Value: ScopeChannel},
				},
			},
		},
		State: channelID,
	}
}

// Scope values for the New Task dialog scope select.
const (
	ScopePersonal = "personal"
	ScopeChannel  = "channel"
)

// CreateSubmission is the parsed, validated payload extracted from a New Task
// dialog submission. AssigneeID/Due are nil when the user left them blank.
type CreateSubmission struct {
	Summary     string
	Description string
	AssigneeID  *string
	Due         *int64
	Personal    bool // true when scope=personal (no channel)
}
