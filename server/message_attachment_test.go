package main

import (
	"strings"
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

// bodyLines returns the Text body split into its individual lines, dropping
// the leading "---" rule so assertions can focus on the metadata lines.
func bodyLines(card model.SlackAttachment) []string {
	parts := strings.Split(card.Text, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "---" {
			out = append(out, p)
		}
	}
	return out
}

func TestBuildTaskCard_TitleAndBody(t *testing.T) {
	due := int64(1_800_000_000_000)
	card := buildCard(sampleTask(taskmodel.StatusTodo, &due), 1_500_000_000_000, 1, 3, 2, "@bob")

	assert.Equal(t, "Review PR", card.Title)
	// No Fields grid — the card uses a multi-line markdown Text body.
	assert.Empty(t, card.Fields, "card uses Text body, not a Fields grid")
	assert.Empty(t, card.Actions, "card has no action buttons")

	// Body = "---" rule + one bold-labeled line per metadata item, in order:
	// Status, Due, Assignee, Progress (subtasks + comments).
	lines := bodyLines(card)
	require.Len(t, lines, 4)
	assert.Contains(t, lines[0], "**Status**", "Status line is bold-labeled")
	assert.Contains(t, lines[0], "To Do")
	assert.Contains(t, lines[1], "**Due**")
	assert.Contains(t, lines[2], "**Assignee**")
	assert.Contains(t, lines[2], "@bob")
	assert.Contains(t, lines[3], "**Progress**")
	assert.Contains(t, lines[3], "1/3 done")
	assert.Contains(t, lines[3], "2 comments")
}

func TestBuildTaskCard_StatusLineFoldsPriority(t *testing.T) {
	// Urgent/important share the Status line instead of taking a separate row.
	task := sampleTask(taskmodel.StatusInProgress, nil)
	task.Priority = taskmodel.PriorityUrgent
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	lines := bodyLines(card)
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "In Progress")
	assert.Contains(t, lines[0], "🔴 Urgent")
}

func TestBuildTaskCard_PriorityStandardOmitted(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.NotContains(t, card.Text, "Urgent")
	assert.NotContains(t, card.Text, "Important")
}

func TestBuildTaskCard_MinimalBody(t *testing.T) {
	// A task with only a status still renders a body: the rule + Status line.
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	lines := bodyLines(card)
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "To Do")
}

func TestBuildTaskCard_ProgressOmittedWhenZero(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.NotContains(t, card.Text, "Progress", "no Progress line when no subtasks and no comments")
}

func TestBuildTaskCard_ProgressOnlyComments(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 3, "")
	lines := bodyLines(card)
	progressLine := lines[len(lines)-1]
	assert.Contains(t, progressLine, "3 comments")
	assert.NotContains(t, progressLine, "done", "no subtask part when total is 0")
}

func TestBuildTaskCard_NoDescription(t *testing.T) {
	// The card deliberately omits the description from the body — it lives in
	// Task Details. So the body never carries the description text.
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.Description = "This is a long description that should NOT appear in the card."
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.NotContains(t, card.Text, task.Description, "description is not rendered in the card body")
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

func TestStatusLine(t *testing.T) {
	t.Run("standard", func(t *testing.T) {
		s := statusLine(&taskmodel.Task{TaskRow: taskmodel.TaskRow{
			Status: taskmodel.StatusTodo, Priority: taskmodel.PriorityStandard,
		}})
		assert.Equal(t, "**Status**: To Do", s)
	})
	t.Run("urgent appends to status", func(t *testing.T) {
		s := statusLine(&taskmodel.Task{TaskRow: taskmodel.TaskRow{
			Status: taskmodel.StatusInProgress, Priority: taskmodel.PriorityUrgent,
		}})
		assert.Contains(t, s, "In Progress")
		assert.Contains(t, s, "🔴 Urgent")
	})
}

func TestProgressLine(t *testing.T) {
	t.Run("both zero", func(t *testing.T) {
		assert.Equal(t, "", progressLine(cardInput{}))
	})
	t.Run("subtasks only", func(t *testing.T) {
		s := progressLine(cardInput{subtaskDone: 2, subtaskTotal: 5, commentCount: 0})
		assert.Equal(t, "**Progress**: 2/5 done", s)
	})
	t.Run("comments only", func(t *testing.T) {
		s := progressLine(cardInput{subtaskDone: 0, subtaskTotal: 0, commentCount: 3})
		assert.Equal(t, "**Progress**: 3 comments", s)
	})
	t.Run("both", func(t *testing.T) {
		s := progressLine(cardInput{subtaskDone: 1, subtaskTotal: 4, commentCount: 2})
		assert.Equal(t, "**Progress**: 1/4 done · 2 comments", s)
	})
}
