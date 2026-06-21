package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

func sampleTask(status string, due *int64) *taskmodel.Task {
	return &taskmodel.Task{TaskRow: taskmodel.TaskRow{ID: "T1", Summary: "Review PR", Due: due, Status: status, CreatedAt: 1_700_000_000_000}, CreatorID: "u-creator", AssigneeID: "u-bob"}
}

func TestBuildTaskCard_FieldsAndTitle(t *testing.T) {
	due := int64(1_800_000_000_000)
	card := buildTaskCard(sampleTask(taskmodel.StatusTodo, &due), 1_500_000_000_000, 1, 3, 2)

	assert.Equal(t, "Review PR", card.Title)
	// Fields: status, assignee, due, subtasks, comments.
	require.Len(t, card.Fields, 5)
	assert.Equal(t, "Status", card.Fields[0].Title)
	assert.Contains(t, card.Fields[0].Value, "To Do")
	assert.Equal(t, "Assignee", card.Fields[1].Title)
	assert.Equal(t, "@u-bob", card.Fields[1].Value)
	assert.Equal(t, "Due", card.Fields[2].Title)
	assert.NotContains(t, card.Fields[2].Value, "overdue", "due is in the future relative to now")
	assert.Equal(t, "Subtasks", card.Fields[3].Title)
	assert.Equal(t, "1/3 done", card.Fields[3].Value)
	assert.Equal(t, "Comments", card.Fields[4].Title)
	assert.Equal(t, "2", card.Fields[4].Value)
}

func TestBuildTaskCard_NoCommentIndicatorWhenZero(t *testing.T) {
	due := int64(1_800_000_000_000)
	card := buildTaskCard(sampleTask(taskmodel.StatusTodo, &due), 1_500_000_000_000, 0, 0, 0)
	for _, f := range card.Fields {
		assert.NotEqual(t, "Comments", f.Title, "no Comments field when count is 0")
		assert.NotEqual(t, "Subtasks", f.Title, "no Subtasks field when total is 0")
	}
}

func TestBuildTaskCard_DoneStrikesThroughTitle(t *testing.T) {
	card := buildTaskCard(sampleTask(taskmodel.StatusDone, nil), 0, 0, 0, 0)
	assert.Equal(t, "~~Review PR~~", card.Title)
}

func TestBuildTaskCard_OverdueOpenTaskIsRed(t *testing.T) {
	pastDue := int64(1_000) // before now
	card := buildTaskCard(sampleTask(taskmodel.StatusTodo, &pastDue), 2_000, 0, 0, 0)
	assert.Equal(t, "#D92D20", card.Color)
}

func TestBuildTaskCard_OverdueDoneTaskNotRed(t *testing.T) {
	// A done task is not flagged overdue even if its due is in the past.
	pastDue := int64(1_000)
	card := buildTaskCard(sampleTask(taskmodel.StatusDone, &pastDue), 2_000, 0, 0, 0)
	assert.NotEqual(t, "#D92D20", card.Color)
	assert.Equal(t, "#1A7140", card.Color, "done status color")
}

func TestBuildTaskCard_StatusColors(t *testing.T) {
	assert.Equal(t, "#4391FE", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusTodo}}, 0))
	assert.Equal(t, "#F1A93C", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusInProgress}}, 0))
	assert.Equal(t, "#1A7140", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusDone}}, 0))
	assert.Equal(t, "#8A8A8A", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusCancelled}}, 0))
}

func TestTaskCardActions_AllFivePresent(t *testing.T) {
	actions := taskCardActions("T1", taskmodel.StatusTodo)
	require.Len(t, actions, 5)
	names := []string{actions[0].Name, actions[1].Name, actions[2].Name, actions[3].Name, actions[4].Name}
	assert.Equal(t, []string{"✓ Done", "🚫 Cancel", "👤 Assign", "➕ Subtask", "💬 Comment"}, names)
}

func TestTaskCardActions_EachHasContext(t *testing.T) {
	for _, a := range taskCardActions("T1", taskmodel.StatusTodo) {
		require.NotNil(t, a.Integration)
		assert.Equal(t, "/plugins/com.mattermost.plugin-task/api/v1/actions", a.Integration.URL)
		assert.Equal(t, "T1", a.Integration.Context["task_id"])
		assert.NotEmpty(t, a.Integration.Context["action"])
	}
}

func TestTaskCardActions_TerminalDisablesMatchingButton(t *testing.T) {
	done := taskCardActions("T1", taskmodel.StatusDone)
	// Done button disabled when already done.
	assert.True(t, done[0].Disabled)
	assert.False(t, done[1].Disabled, "cancel not disabled on a done task")

	cancelled := taskCardActions("T1", taskmodel.StatusCancelled)
	assert.True(t, cancelled[1].Disabled)
	assert.False(t, cancelled[0].Disabled, "done not disabled on a cancelled task")
}

func TestStatusLabel(t *testing.T) {
	assert.Equal(t, "To Do", statusLabel(taskmodel.StatusTodo))
	assert.Equal(t, "In Progress", statusLabel(taskmodel.StatusInProgress))
	assert.Equal(t, "✅ Done", statusLabel(taskmodel.StatusDone))
	assert.Equal(t, "🚫 Cancelled", statusLabel(taskmodel.StatusCancelled))
}

func TestDueLabel_Overdue(t *testing.T) {
	past := int64(1_000)
	lbl := dueLabel(past, 2_000)
	assert.Contains(t, lbl, "overdue")
	assert.Contains(t, lbl, "🔴")
}

func TestDueLabel_Future(t *testing.T) {
	future := int64(5_000)
	lbl := dueLabel(future, 1_000)
	assert.NotContains(t, lbl, "overdue")
}
