package main

import (
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
	long := string(make([]byte, 100))
	for i := range long {
		long = long[:i] + "x" + long[i+1:]
	}
	dialog := buildQuickListDialog("u1", []taskmodel.Task{{ID: "T1", Summary: long, Status: taskmodel.StatusTodo}})
	opt := dialog.Elements[3].Options[0].Text
	assert.Less(t, len(opt), len(long)+30, "option label is truncated")
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
	assert.Empty(t, update.NewAssignee)
}

func TestTopNTasksDefault_Is20(t *testing.T) {
	assert.Equal(t, 20, topNTasksDefault, "issue #17: default top-N is 20")
}
