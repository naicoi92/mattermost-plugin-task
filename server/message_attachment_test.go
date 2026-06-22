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

func TestBuildTaskCard_TitleTextAndChips(t *testing.T) {
	due := int64(1_800_000_000_000)
	card := buildCard(sampleTask(taskmodel.StatusTodo, &due), 1_500_000_000_000, 1, 3, 2, "@bob")

	assert.Equal(t, "Review PR", card.Title)
	// No Fields grid — metadata lives in Text + colored Action chips.
	assert.Empty(t, card.Fields, "card uses Text + Action chips, not a Fields grid")

	// Actions: exactly one Status chip (standard priority → no priority chip).
	require.Len(t, card.Actions, 1)
	assert.Equal(t, "To Do", card.Actions[0].Name)
	assert.Equal(t, "primary", card.Actions[0].Style, "todo status → primary/blue chip")
	assert.True(t, card.Actions[0].Disabled, "status chip is decorative")

	// Text body: one compact line joining due, assignee, subtasks, comments.
	assert.NotContains(t, card.Text, "**", "no bold meta-labels in the body")
	assert.Contains(t, card.Text, "📅", "due has a calendar emoji")
	assert.Contains(t, card.Text, "@bob", "assignee mention present")
	assert.Contains(t, card.Text, "1/3 subtasks", "subtask progress present")
	assert.Contains(t, card.Text, "2 comments", "comment count present")
}

func TestCardActions_StatusChipsByStatus(t *testing.T) {
	cases := []struct {
		status string
		style  string
		name   string
	}{
		{taskmodel.StatusTodo, "primary", "To Do"},
		{taskmodel.StatusInProgress, "warning", "In Progress"},
		{taskmodel.StatusDone, "good", "✅ Done"},
		{taskmodel.StatusCancelled, "default", "🚫 Cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			actions := cardActions(&taskmodel.Task{TaskRow: taskmodel.TaskRow{
				Status: tc.status, Priority: taskmodel.PriorityStandard,
			}})
			require.Len(t, actions, 1, "standard priority → only status chip")
			assert.Equal(t, tc.style, actions[0].Style)
			assert.Equal(t, tc.name, actions[0].Name)
			assert.True(t, actions[0].Disabled, "chip is decorative")
		})
	}
}

func TestCardActions_PriorityChipWhenElevated(t *testing.T) {
	// Urgent → danger chip appended after the status chip.
	actions := cardActions(&taskmodel.Task{TaskRow: taskmodel.TaskRow{
		Status: taskmodel.StatusTodo, Priority: taskmodel.PriorityUrgent,
	}})
	require.Len(t, actions, 2)
	assert.Equal(t, "primary", actions[0].Style, "status chip first")
	assert.Equal(t, "danger", actions[1].Style, "urgent → danger/red chip")
	assert.Equal(t, "🔴 Urgent", actions[1].Name)
	assert.True(t, actions[1].Disabled)

	// Important → warning chip.
	actions = cardActions(&taskmodel.Task{TaskRow: taskmodel.TaskRow{
		Status: taskmodel.StatusTodo, Priority: taskmodel.PriorityImportant,
	}})
	require.Len(t, actions, 2)
	assert.Equal(t, "warning", actions[1].Style)
	assert.Equal(t, "🟠 Important", actions[1].Name)

	// Standard → no priority chip.
	actions = cardActions(&taskmodel.Task{TaskRow: taskmodel.TaskRow{
		Status: taskmodel.StatusTodo, Priority: taskmodel.PriorityStandard,
	}})
	require.Len(t, actions, 1, "standard priority → no priority chip")
}

func TestMetaBody_SkipsEmptyItems(t *testing.T) {
	// No due, no assignee, no subtasks, no comments → empty body.
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	body := metaBody(task, cardInput{})
	assert.Empty(t, body, "empty task yields an empty meta line")
}

func TestMetaBody_PartialItems(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	body := metaBody(task, cardInput{assigneeMention: "@alice"})
	assert.Equal(t, "@alice", body, "only the assignee line when nothing else is set")
}

func TestMetaBody_JoinsWithMiddot(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	body := metaBody(task, cardInput{
		assigneeMention: "@bob", subtaskDone: 1, subtaskTotal: 3, commentCount: 2,
	})
	parts := strings.Split(body, metaSeparator)
	require.Len(t, parts, 4, "due + assignee + subtasks + comments")
	assert.Contains(t, parts[0], "📅")
	assert.Equal(t, "@bob", parts[1])
	assert.Contains(t, parts[2], "1/3 subtasks")
	assert.Contains(t, parts[3], "2 comments")
}

func TestBuildTaskCard_NoDescription(t *testing.T) {
	// The description is never rendered in the card — it lives in Task Details.
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.Description = "Long description that must NOT appear in the card body."
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.NotContains(t, card.Text, task.Description)
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

func TestStatusActionStyle(t *testing.T) {
	assert.Equal(t, "primary", statusActionStyle(taskmodel.StatusTodo))
	assert.Equal(t, "warning", statusActionStyle(taskmodel.StatusInProgress))
	assert.Equal(t, "good", statusActionStyle(taskmodel.StatusDone))
	assert.Equal(t, "default", statusActionStyle(taskmodel.StatusCancelled))
	assert.Equal(t, "primary", statusActionStyle("unknown"), "unknown falls back to primary")
}

func TestPriorityActionStyle(t *testing.T) {
	assert.Equal(t, "danger", priorityActionStyle(taskmodel.PriorityUrgent))
	assert.Equal(t, "warning", priorityActionStyle(taskmodel.PriorityImportant))
	assert.Equal(t, "", priorityActionStyle(taskmodel.PriorityStandard), "standard → no chip")
}
