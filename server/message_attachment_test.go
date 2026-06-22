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

func TestBuildTaskCard_TitleAndMetaLine(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	task.Description = "Small bugfix worth reviewing." // add a description so the rule is present
	card := buildCard(task, 1_500_000_000_000, 1, 3, 2, "@bob")

	assert.Equal(t, "Review PR", card.Title)
	// No Fields grid — the card uses a compact markdown Text body.
	assert.Empty(t, card.Fields, "card uses Text body, not a Fields grid")

	// Text body: description + "---" rule + inline meta line.
	body := card.Text
	assert.Contains(t, body, "---", "body has a horizontal rule before the meta line")

	// Meta line items appear in Quick List order, joined by " · ".
	meta := strings.Split(body, "---\n")[1]
	assert.Contains(t, meta, "To Do", "status label present")
	assert.Contains(t, meta, "📅", "due has a calendar emoji")
	assert.Contains(t, meta, "@bob", "assignee mention present")
	assert.Contains(t, meta, "✓ 1/3", "subtask progress present")
	assert.Contains(t, meta, "💬 2", "comment count present")
}

func TestBuildTaskCard_PriorityUrgentInMeta(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	task.Priority = taskmodel.PriorityUrgent
	card := buildCard(task, 1_500_000_000_000, 0, 0, 0, "")
	assert.Contains(t, card.Text, "🔴 Urgent", "urgent priority appears in meta line")
}

func TestBuildTaskCard_PriorityImportantInMeta(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.Priority = taskmodel.PriorityImportant
	task.AssigneeID = ""
	card := buildCard(task, 1_500_000_000_000, 0, 0, 0, "")
	assert.Contains(t, card.Text, "🟠 Important", "important priority appears in meta line")
}

func TestBuildTaskCard_PriorityStandardOmitted(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil) // standard by default
	task.AssigneeID = ""
	card := buildCard(task, 1_500_000_000_000, 0, 0, 0, "")
	assert.NotContains(t, card.Text, "Urgent")
	assert.NotContains(t, card.Text, "Important")
}

func TestBuildTaskCard_DescriptionPreview(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.Description = "This PR adds a login flow with OAuth and session refresh."
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.True(t, strings.HasPrefix(card.Text, task.Description), "description preview is the first body line")
	assert.Contains(t, card.Text, "---", "rule separates description from meta")
}

func TestBuildTaskCard_DescriptionTruncated(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	long := make([]rune, descriptionPreviewMax+50)
	for i := range long {
		long[i] = 'x'
	}
	task.Description = string(long)
	card := buildCard(task, 0, 0, 0, 0, "")
	firstLine := strings.Split(card.Text, "\n")[0]
	assert.Less(t, len(firstLine), len(task.Description), "long description is truncated in the body")
	assert.True(t, func() bool {
		r := []rune(firstLine)
		return len(r) > 0 && r[len(r)-1] == '…'
	}(), "truncated preview ends with ellipsis")
}

func TestBuildTaskCard_EmptyDescriptionHasNoDescriptionLine(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	// Body is just the meta line (no leading description, no rule).
	assert.NotContains(t, card.Text, "---", "no rule when there's no description")
	assert.Contains(t, card.Text, "To Do", "meta line still present")
}

func TestBuildTaskCard_SubtaskProgressOmittedWhenZero(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.NotContains(t, card.Text, "✓", "no subtask indicator when total is 0")
}

func TestBuildTaskCard_CommentCountOmittedWhenZero(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	task.AssigneeID = ""
	card := buildCard(task, 0, 0, 0, 0, "")
	assert.NotContains(t, card.Text, "💬", "no comment indicator when count is 0")
}

func TestBuildTaskCard_NoActions(t *testing.T) {
	// The card is information-only — no interactive buttons. All interactions
	// happen in the Task Details panel (opened by clicking the card).
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
	long := strings.Join(words, " ")
	got := descriptionPreview(long)
	assert.Less(t, len(got), len(long))
	r := []rune(got)
	assert.True(t, r[len(r)-1] == '…', "preview ends with ellipsis, got %q", got)
	assert.NotContains(t, string(r[:len(r)-1]), "  ", "no double spaces inside the preview")
}

// metaLine tests isolate the inline-meta builder from the full body so the
// join order and item omission can be asserted precisely.
func TestMetaLine_FullSet(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	task.Priority = taskmodel.PriorityUrgent
	got := metaLine(task, cardInput{
		task: task, nowMs: 1_500_000_000_000, assigneeMention: "@bob",
		subtaskDone: 2, subtaskTotal: 5, commentCount: 3,
	})
	parts := strings.Split(got, metaSeparator)
	require.Len(t, parts, 6, "all six meta items present in order")
	assert.Contains(t, parts[0], "To Do")
	assert.Contains(t, parts[1], "Urgent")
	assert.Contains(t, parts[2], "📅")
	assert.Equal(t, "@bob", parts[3])
	assert.Contains(t, parts[4], "2/5")
	assert.Contains(t, parts[5], "💬")
}
