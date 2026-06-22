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
// defaults (resolved creator + assignee mentions, no subtasks/comments) so
// individual tests can override just the fields they care about.
func buildCard(t *taskmodel.Task, nowMs int64, subtaskDone, subtaskTotal, commentCount int, creator, assignee userRef) model.SlackAttachment {
	return buildTaskCard(cardInput{
		task:         t,
		nowMs:        nowMs,
		creator:      creator,
		assignee:     assignee,
		subtaskDone:  subtaskDone,
		subtaskTotal: subtaskTotal,
		commentCount: commentCount,
	})
}

func TestBuildTaskCard_FullCard(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	task.Description = "OAuth login flow"
	card := buildCard(task, 1_500_000_000_000, 1, 3, 2,
		userRef{mention: "@alice", avatarURL: "https://site/alice.png"},
		userRef{mention: "@bob", avatarURL: "https://site/bob.png"})

	// Pretext: lifecycle line above the card.
	assert.Equal(t, "@alice created a task", card.Pretext)

	// Author row: creator mention + avatar.
	assert.Equal(t, "@alice", card.AuthorName)
	assert.Equal(t, "https://site/alice.png", card.AuthorIcon)

	// Title + description preview body.
	assert.Equal(t, "Review PR", card.Title)
	assert.Equal(t, "OAuth login flow", card.Text, "description preview lives in Text")

	// No Fields grid.
	assert.Empty(t, card.Fields)

	// Actions: status chip only (standard priority).
	require.Len(t, card.Actions, 1)
	assert.Equal(t, "To Do", card.Actions[0].Name)
	assert.Equal(t, "primary", card.Actions[0].Style)
	assert.True(t, card.Actions[0].Disabled)

	// Footer: due + assignee + progress.
	assert.Contains(t, card.Footer, "📅")
	assert.Contains(t, card.Footer, "@bob")
	assert.Contains(t, card.Footer, "✓ 1/3")
	assert.Contains(t, card.Footer, "💬 2")
}

func TestBuildTaskCard_PretextLifecycleLine(t *testing.T) {
	// With a creator, the pretext reads "@creator created a task".
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0,
		userRef{mention: "@alice"}, userRef{})
	assert.Equal(t, "@alice created a task", card.Pretext)
}

func TestBuildTaskCard_NoPretextWithoutCreator(t *testing.T) {
	// An empty creator ref omits the pretext entirely.
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0, userRef{}, userRef{})
	assert.Empty(t, card.Pretext)
}

func TestBuildTaskCard_AuthorRow(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0,
		userRef{mention: "@alice", avatarURL: "https://site/a.png"}, userRef{})
	assert.Equal(t, "@alice", card.AuthorName)
	assert.Equal(t, "https://site/a.png", card.AuthorIcon)
}

func TestBuildTaskCard_TitleLink(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	in := cardInput{
		task:          task,
		creator:       userRef{mention: "@alice"},
		taskPermalink: "https://site/pl/task123",
	}
	card := buildTaskCard(in)
	assert.Equal(t, "https://site/pl/task123", card.TitleLink)
}

func TestBuildTaskCard_NoTitleLinkWhenEmpty(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0, userRef{mention: "@alice"}, userRef{})
	assert.Empty(t, card.TitleLink)
}

func TestBuildTaskCard_DescriptionPreview(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.Description = "Short description."
	card := buildCard(task, 0, 0, 0, 0, userRef{mention: "@a"}, userRef{})
	assert.Equal(t, "Short description.", card.Text)
}

func TestBuildTaskCard_EmptyDescription(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0, userRef{mention: "@a"}, userRef{})
	assert.Empty(t, card.Text)
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

func TestCardFooter_FullSet(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	footer := cardFooter(task, cardInput{
		task: task, nowMs: 1_500_000_000_000,
		assignee:     userRef{mention: "@bob"},
		subtaskDone:  2, subtaskTotal: 5, commentCount: 3,
	})
	assert.Contains(t, footer, "📅")
	assert.Contains(t, footer, "@bob")
	assert.Contains(t, footer, "✓ 2/5")
	assert.Contains(t, footer, "💬 3")
}

func TestCardFooter_SkipsEmptyItems(t *testing.T) {
	// No due, no assignee, no subtasks, no comments → empty footer.
	task := sampleTask(taskmodel.StatusTodo, nil)
	assert.Empty(t, cardFooter(task, cardInput{task: task}))
}

func TestCardFooter_OnlyDue(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	footer := cardFooter(task, cardInput{task: task, nowMs: 1_500_000_000_000})
	assert.Contains(t, footer, "📅")
	assert.NotContains(t, footer, "@")
	assert.NotContains(t, footer, "✓")
	assert.NotContains(t, footer, "💬")
}

func TestBuildTaskCard_DoneStrikesThroughTitle(t *testing.T) {
	card := buildCard(sampleTask(taskmodel.StatusDone, nil), 0, 0, 0, 0,
		userRef{mention: "@a"}, userRef{})
	assert.Equal(t, "~~Review PR~~", card.Title)
}

func TestBuildTaskCard_OverdueOpenTaskIsRed(t *testing.T) {
	pastDue := int64(1_000) // before now
	card := buildCard(sampleTask(taskmodel.StatusTodo, &pastDue), 2_000, 0, 0, 0,
		userRef{}, userRef{})
	assert.Equal(t, "#D92D20", card.Color)
}

func TestBuildTaskCard_OverdueDoneTaskNotRed(t *testing.T) {
	// A done task is not flagged overdue even if its due is in the past.
	pastDue := int64(1_000)
	card := buildCard(sampleTask(taskmodel.StatusDone, &pastDue), 2_000, 0, 0, 0,
		userRef{}, userRef{})
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
