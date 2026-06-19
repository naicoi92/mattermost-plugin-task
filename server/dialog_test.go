package main

import (
	"strings"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

func TestBuildQuickListDialog_Structure(t *testing.T) {
	tasks := []taskmodel.Task{
		{ID: "T1", Summary: "First", Status: taskmodel.StatusTodo},
		{ID: "T2", Summary: "Second", Status: taskmodel.StatusDone},
	}
	dialog := buildQuickListDialog("u1", tasks)

	assert.Equal(t, dialogCallbackQuickList, dialog.CallbackId)
	assert.Equal(t, "u1", dialog.State)
	require.Len(t, dialog.Elements, 4)
	// scope / status / due / task pick.
	assert.Equal(t, dialogFieldScope, dialog.Elements[0].Name)
	assert.Equal(t, dialogFieldStatus, dialog.Elements[1].Name)
	assert.Equal(t, dialogFieldDue, dialog.Elements[2].Name)
	assert.Equal(t, dialogFieldTaskPick, dialog.Elements[3].Name)

	// Task pick options come from the tasks slice.
	require.Len(t, dialog.Elements[3].Options, 2)
	assert.Equal(t, "T1", dialog.Elements[3].Options[0].Value)
	assert.Contains(t, dialog.Elements[3].Options[0].Text, "First")
}

func TestBuildQuickListDialog_TruncatesLongSummary(t *testing.T) {
	long := strings.Repeat("x", 120)
	dialog := buildQuickListDialog("u1", []taskmodel.Task{{ID: "T1", Summary: long, Status: taskmodel.StatusTodo}})
	opt := dialog.Elements[3].Options[0].Text
	// Label is "<summary · status>"; the summary half is the first 59 chars + "…".
	assert.True(t, strings.HasSuffix(strings.SplitN(opt, " · ", 2)[0], "…"),
		"truncated summary ends with ellipsis: %q", opt)
	assert.Contains(t, opt, "To Do", "status half preserved")
}

func TestBuildTaskDetailDialog_EditableFields(t *testing.T) {
	due := int64(1_700_000_000_000)
	t0 := &taskmodel.Task{
		ID: "T1", Summary: "Review", Description: "desc", Status: taskmodel.StatusTodo,
		AssigneeID: "u-bob", Due: &due,
	}
	dialog := buildTaskDetailDialog(t0, 1, 3, nil)

	assert.Equal(t, dialogCallbackTaskDetail, dialog.CallbackId)
	assert.Equal(t, "T1", dialog.State)
	assert.Contains(t, dialog.IntroductionText, "**Subtasks:** 1/3")
	require.Len(t, dialog.Elements, 5)

	// Summary prefilled.
	assert.Equal(t, dialogFieldSummary, dialog.Elements[0].Name)
	assert.Equal(t, "Review", dialog.Elements[0].Default)
	// Assignee uses a user picker.
	var assignee *model.DialogElement
	for i := range dialog.Elements {
		if dialog.Elements[i].Name == dialogFieldAssignee {
			assignee = &dialog.Elements[i]
		}
	}
	require.NotNil(t, assignee)
	assert.Equal(t, "users", assignee.DataSource)
	// Due prefilled with the ms value.
	var dueEl *model.DialogElement
	for i := range dialog.Elements {
		if dialog.Elements[i].Name == dialogFieldTaskDue {
			dueEl = &dialog.Elements[i]
		}
	}
	require.NotNil(t, dueEl)
	assert.Equal(t, "1700000000000", dueEl.Default)
}

func TestBuildTaskDetailDialog_RecentComments(t *testing.T) {
	t0 := &taskmodel.Task{ID: "T1", Summary: "x", Status: taskmodel.StatusTodo}
	dialog := buildTaskDetailDialog(t0, 0, 0, []taskmodel.Comment{
		{ID: "c1", Content: "looks good"},
		{ID: "c2", Content: "needs fix"},
	})
	assert.Contains(t, dialog.IntroductionText, "Recent comments")
	assert.Contains(t, dialog.IntroductionText, "looks good")
	assert.Contains(t, dialog.IntroductionText, "needs fix")
}

func TestParseTaskDetailSubmission_AllChanged(t *testing.T) {
	current := &taskmodel.Task{ID: "T1", Summary: "old", Description: "d", Status: taskmodel.StatusTodo, AssigneeID: "u-old"}
	sub := map[string]any{
		dialogFieldSummary:     "new",
		dialogFieldDescription: "newdesc",
		dialogFieldStatus:      taskmodel.StatusInProgress,
		dialogFieldAssignee:    "u-new",
		dialogFieldTaskDue:     "1700000000000",
	}
	update, err := parseTaskDetailSubmission(sub, current)
	require.NoError(t, err)
	assert.Equal(t, []string{"summary", "description", "due"}, update.Patch.UpdateFields)
	require.NotNil(t, update.Patch.Summary)
	assert.Equal(t, "new", *update.Patch.Summary)
	assert.Equal(t, taskmodel.StatusInProgress, update.NewStatus)
	assert.Equal(t, "u-new", update.NewAssignee)
	assert.True(t, update.AssigneeSet)
	require.NotNil(t, update.Patch.Due)
	assert.Equal(t, int64(1_700_000_000_000), *update.Patch.Due)
}

func TestParseTaskDetailSubmission_InvalidStatus(t *testing.T) {
	current := &taskmodel.Task{ID: "T1", Status: taskmodel.StatusTodo}
	_, err := parseTaskDetailSubmission(map[string]any{dialogFieldStatus: "bogus"}, current)
	require.Error(t, err)
}

func TestParseTaskDetailSubmission_InvalidDue(t *testing.T) {
	current := &taskmodel.Task{ID: "T1", Status: taskmodel.StatusTodo}
	_, err := parseTaskDetailSubmission(map[string]any{dialogFieldTaskDue: "next week"}, current)
	require.Error(t, err)
}

func TestParseTaskDetailSubmission_ClearsDue(t *testing.T) {
	oldDue := int64(1_000)
	current := &taskmodel.Task{ID: "T1", Status: taskmodel.StatusTodo, Due: &oldDue}
	update, err := parseTaskDetailSubmission(map[string]any{dialogFieldTaskDue: ""}, current)
	require.NoError(t, err)
	assert.Equal(t, []string{"due"}, update.Patch.UpdateFields)
	assert.Nil(t, update.Patch.Due)
}

func TestParseTaskDetailSubmission_NoChanges(t *testing.T) {
	current := &taskmodel.Task{ID: "T1", Summary: "same", Description: "d", Status: taskmodel.StatusTodo}
	update, err := parseTaskDetailSubmission(map[string]any{
		dialogFieldSummary:     "same",
		dialogFieldDescription: "d",
		dialogFieldStatus:      taskmodel.StatusTodo,
	}, current)
	require.NoError(t, err)
	assert.Empty(t, update.Patch.UpdateFields)
	assert.Empty(t, update.NewStatus)
	assert.False(t, update.AssigneeSet, "no assignee change")
}

func TestParseTaskDetailSubmission_ClearsAssignee(t *testing.T) {
	// Clearing the assignee (empty string) must be distinguishable from
	// "unchanged" via AssigneeSet, so the dialog can unassign.
	current := &taskmodel.Task{ID: "T1", Summary: "x", Status: taskmodel.StatusTodo, AssigneeID: "u-old"}
	update, err := parseTaskDetailSubmission(map[string]any{
		dialogFieldAssignee: "",
	}, current)
	require.NoError(t, err)
	assert.True(t, update.AssigneeSet, "clearing the assignee sets the flag")
	assert.Empty(t, update.NewAssignee)
}

func TestTopNTasksDefault_Is20(t *testing.T) {
	assert.Equal(t, 20, topNTasksDefault, "issue #17: default top-N is 20")
}

// --- New Task dialog (#95) -------------------------------------------------

func TestBuildNewTaskDialog_PrefilledSummaryAndFields(t *testing.T) {
	dialog := buildNewTaskDialog("buy milk", "ch1", false)

	assert.Equal(t, dialogCallbackNewTask, dialog.CallbackId)
	assert.Equal(t, "New Task", dialog.Title)
	assert.Equal(t, "Create", dialog.SubmitLabel)
	assert.Equal(t, "ch1", dialog.State, "state carries the channel id for the submit handler")

	require.Len(t, dialog.Elements, 5, "summary, assignee, due, description, scope")
	assert.Equal(t, dialogFieldSummary, dialog.Elements[0].Name)
	assert.Equal(t, "buy milk", dialog.Elements[0].Default, "summary is prefilled")

	// Assignee uses the users data source (single user picker, optional).
	assert.Equal(t, dialogFieldAssignee, dialog.Elements[1].Name)
	assert.Equal(t, "users", dialog.Elements[1].DataSource)
	assert.True(t, dialog.Elements[1].Optional)

	// Description and due are optional.
	assert.Equal(t, dialogFieldTaskDue, dialog.Elements[2].Name)
	assert.True(t, dialog.Elements[2].Optional)
	assert.Equal(t, dialogFieldDescription, dialog.Elements[3].Name)
	assert.True(t, dialog.Elements[3].Optional)

	// Scope defaults to channel when a channel context exists.
	assert.Equal(t, dialogFieldNewScope, dialog.Elements[4].Name)
	assert.Equal(t, "channel", dialog.Elements[4].Default)
	require.Len(t, dialog.Elements[4].Options, 2, "personal + channel options when a channel context exists")
}

func TestBuildNewTaskDialog_PersonalScopeWhenNoChannel(t *testing.T) {
	// A DM with the bot (no channel id) forces personal scope: only the personal
	// option is offered (PLAN §5.1.A).
	dialog := buildNewTaskDialog("", "", true)
	scope := dialog.Elements[4]
	assert.Equal(t, "personal", scope.Default)
	require.Len(t, scope.Options, 1, "channel option hidden when there is no channel context")
	assert.Equal(t, "personal", scope.Options[0].Value)
}
