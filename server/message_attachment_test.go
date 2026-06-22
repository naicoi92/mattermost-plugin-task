package main

import (
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

func sampleTask(status string, due *int64) *taskmodel.Task {
	return &taskmodel.Task{TaskRow: taskmodel.TaskRow{
		ID:        "T1",
		Summary:   "Review PR",
		DueAt:     due,
		Status:    status,
		Priority:  taskmodel.PriorityStandard,
		CreatedAt: 1_700_000_000_000,
	}, CreatorID: "u-creator", AssigneeID: "u-bob"}
}

// buildCard is a thin test helper that assembles a cardInput with the common
// defaults (resolved assignee mention, no subtasks/comments) so individual
// tests can override just the fields they care about.
func buildCard(t *taskmodel.Task, nowMs int64, subtaskDone, subtaskTotal, commentCount int, assigneeMention string) model.SlackAttachment {
	return buildTaskCard(cardInput{
		task:            t,
		nowMs:           nowMs,
		assigneeMention: assigneeMention,
		subtaskDone:     subtaskDone,
		subtaskTotal:    subtaskTotal,
		commentCount:    commentCount,
	})
}

func TestBuildTaskCard_FieldsAndTitle(t *testing.T) {
	due := int64(1_800_000_000_000)
	card := buildCard(sampleTask(taskmodel.StatusTodo, &due), 1_500_000_000_000, 1, 3, 2, "@bob")

	assert.Equal(t, "Review PR", card.Title)
	// Fields in Quick List meta-row order: Status, Priority(absent=standard),
	// Due, Assignee, Subtasks, Comments.
	require.Len(t, card.Fields, 5)
	assert.Equal(t, "Status", card.Fields[0].Title)
	assert.Contains(t, card.Fields[0].Value, "To Do")
	assert.Equal(t, "Due", card.Fields[1].Title)
	assert.NotContains(t, card.Fields[1].Value, "overdue", "due is in the future relative to now")
	assert.Equal(t, "Assignee", card.Fields[2].Title)
	assert.Equal(t, "@bob", card.Fields[2].Value)
	assert.Equal(t, "Subtasks", card.Fields[3].Title)
	assert.Equal(t, "1/3 done", card.Fields[3].Value)
	assert.Equal(t, "Comments", card.Fields[4].Title)
	assert.Equal(t, "2", card.Fields[4].Value)
}

func TestBuildTaskCard_PriorityFieldUrgent(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	task.Priority = taskmodel.PriorityUrgent
	task.AssigneeID = "" // isolate Priority: no Due/Assignee/Subtasks/Comments
	task.DueAt = nil
	card := buildCard(task, 1_500_000_000_000, 0, 0, 0, "")
	require.Len(t, card.Fields, 2) // Status + Priority only
	assert.Equal(t, "Priority", card.Fields[1].Title)
	assert.Equal(t, "🔴 Urgent", card.Fields[1].Value)
}

func TestBuildTaskCard_PriorityFieldImportant(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.Priority = taskmodel.PriorityImportant
	task.AssigneeID = ""
	card := buildCard(task, 1_500_000_000_000, 0, 0, 0, "")
	assert.Equal(t, "Priority", card.Fields[1].Title)
	assert.Equal(t, "🟠 Important", card.Fields[1].Value)
}

func TestBuildTaskCard_PriorityHiddenWhenStandard(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due) // standard by default
	card := buildCard(task, 1_500_000_000_000, 0, 0, 0, "")
	for _, f := range card.Fields {
		assert.NotEqual(t, "Priority", f.Title, "Priority field hidden when standard")
	}
}

func TestBuildTaskCard_DescriptionPreview(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.Description = "This PR adds a login flow with OAuth and session refresh."
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.Equal(t, task.Description, card.Text, "short description fits in the body verbatim")
}

func TestBuildTaskCard_DescriptionTruncated(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	long := make([]rune, descriptionPreviewMax+50)
	for i := range long {
		long[i] = 'x'
	}
	task.Description = string(long)
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.Less(t, len(card.Text), len(task.Description), "long description is truncated")
	assert.True(t, func() bool {
		r := []rune(card.Text)
		return len(r) > 0 && r[len(r)-1] == '…'
	}(), "truncated preview ends with ellipsis")
}

func TestBuildTaskCard_EmptyDescriptionHasNoText(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.Empty(t, card.Text)
}

func TestBuildTaskCard_NoCommentIndicatorWhenZero(t *testing.T) {
	due := int64(1_800_000_000_000)
	card := buildCard(sampleTask(taskmodel.StatusTodo, &due), 1_500_000_000_000, 0, 0, 0, "@bob")
	for _, f := range card.Fields {
		assert.NotEqual(t, "Comments", f.Title, "no Comments field when count is 0")
		assert.NotEqual(t, "Subtasks", f.Title, "no Subtasks field when total is 0")
	}
}

func TestBuildTaskCard_NoActions(t *testing.T) {
	// The redesigned card is information-only — no interactive buttons. All
	// interactions happen in the Task Details panel (opened by clicking the
	// card), matching the Quick List row.
	due := int64(1_800_000_000_000)
	card := buildCard(sampleTask(taskmodel.StatusTodo, &due), 1_500_000_000_000, 0, 0, 0, "@bob")
	assert.Empty(t, card.Actions, "card has no action buttons")
}

func TestBuildTaskCard_DoneStrikesThroughTitle(t *testing.T) {
	card := buildCard(sampleTask(taskmodel.StatusDone, nil), 0, 0, 0, 0, "")
	assert.Equal(t, "~~Review PR~~", card.Title)
}

func TestBuildTaskCard_OverdueOpenTaskIsRed(t *testing.T) {
	pastDue := int64(1_000) // before now
	card := buildCard(sampleTask(taskmodel.StatusTodo, &pastDue), 2_000, 0, 0, 0, "")
	assert.Equal(t, "#D92D20", card.Color)
}

func TestBuildTaskCard_OverdueDoneTaskNotRed(t *testing.T) {
	// A done task is not flagged overdue even if its due is in the past.
	pastDue := int64(1_000)
	card := buildCard(sampleTask(taskmodel.StatusDone, &pastDue), 2_000, 0, 0, 0, "")
	assert.NotEqual(t, "#D92D20", card.Color)
	assert.Equal(t, "#1A7140", card.Color, "done status color")
}

func TestBuildTaskCard_StatusColors(t *testing.T) {
	assert.Equal(t, "#4391FE", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusTodo}}, 0))
	assert.Equal(t, "#F1A93C", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusInProgress}}, 0))
	assert.Equal(t, "#1A7140", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusDone}}, 0))
	assert.Equal(t, "#8A8A8A", cardColor(&taskmodel.Task{TaskRow: taskmodel.TaskRow{Status: taskmodel.StatusCancelled}}, 0))
}

func TestStatusLabel(t *testing.T) {
	assert.Equal(t, "To Do", statusLabel(taskmodel.StatusTodo))
	assert.Equal(t, "In Progress", statusLabel(taskmodel.StatusInProgress))
	assert.Equal(t, "✅ Done", statusLabel(taskmodel.StatusDone))
	assert.Equal(t, "🚫 Cancelled", statusLabel(taskmodel.StatusCancelled))
}

func TestDueLabel_Overdue(t *testing.T) {
	// Due 3 days before now, open task → "3 days overdue".
	now := time.UnixMilli(10_000_000).Local()
	past := startOfDay(now).AddDate(0, 0, -3).Add(2 * time.Hour).UnixMilli()
	lbl := dueLabel(past, now.UnixMilli(), taskmodel.StatusTodo)
	assert.Equal(t, "3 days overdue", lbl)
}

func TestDueLabel_OverdueSingular(t *testing.T) {
	now := time.UnixMilli(10_000_000).Local()
	past := startOfDay(now).AddDate(0, 0, -1).Add(2 * time.Hour).UnixMilli()
	lbl := dueLabel(past, now.UnixMilli(), taskmodel.StatusTodo)
	assert.Equal(t, "1 day overdue", lbl)
}

func TestDueLabel_OverdueHiddenWhenDone(t *testing.T) {
	// A terminal task with a past due renders an absolute date, not "overdue".
	now := time.UnixMilli(10_000_000).Local()
	past := startOfDay(now).AddDate(0, 0, -3).Add(2 * time.Hour).UnixMilli()
	lbl := dueLabel(past, now.UnixMilli(), taskmodel.StatusDone)
	assert.NotContains(t, lbl, "overdue")
}

func TestDueLabel_Today(t *testing.T) {
	now := time.UnixMilli(10_000_000).Local()
	due := startOfDay(now).Add(17*time.Hour + 30*time.Minute).UnixMilli()
	lbl := dueLabel(due, now.UnixMilli(), taskmodel.StatusTodo)
	assert.Equal(t, "Today, 17:30", lbl)
}

func TestDueLabel_Tomorrow(t *testing.T) {
	now := time.UnixMilli(10_000_000).Local()
	due := startOfDay(now).AddDate(0, 0, 1).Add(9 * time.Hour).UnixMilli()
	lbl := dueLabel(due, now.UnixMilli(), taskmodel.StatusTodo)
	assert.Equal(t, "Tomorrow", lbl)
}

func TestDueLabel_FutureSameYear(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.Local)
	due := time.Date(2026, 6, 20, 9, 0, 0, 0, time.Local).UnixMilli()
	lbl := dueLabel(due, now.UnixMilli(), taskmodel.StatusTodo)
	assert.Equal(t, "Sat, 20 Jun", lbl)
}

func TestDueLabel_FutureOtherYear(t *testing.T) {
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.Local)
	due := time.Date(2027, 1, 4, 9, 0, 0, 0, time.Local).UnixMilli()
	lbl := dueLabel(due, now.UnixMilli(), taskmodel.StatusTodo)
	assert.Equal(t, "Mon, 4 Jan 2027", lbl)
}

func TestDescriptionPreview(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \n\t  ", ""},
		{"short", "hello world", "hello world"},
		{"collapses whitespace", "hello   \n\nworld", "hello world"},
		{"trims leading/trailing", "  hello  ", "hello"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, descriptionPreview(tc.in))
		})
	}
}

func TestDescriptionPreview_TruncatesAtWordBoundary(t *testing.T) {
	// Build a description well over the limit, with word breaks, so the preview
	// cuts at a word boundary and ends with an ellipsis.
	words := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		words = append(words, "word")
	}
	long := ""
	for _, w := range words {
		if long != "" {
			long += " "
		}
		long += w
	}
	got := descriptionPreview(long)
	assert.Less(t, len(got), len(long))
	r := []rune(got)
	assert.True(t, r[len(r)-1] == '…', "preview ends with ellipsis, got %q", got)
	assert.NotContains(t, string(r[:len(r)-1]), "  ", "no double spaces inside the preview")
}
