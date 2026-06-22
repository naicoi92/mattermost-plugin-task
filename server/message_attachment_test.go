package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/task"
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
func buildCard(t *taskmodel.Task, nowMs int64, subtaskDone, subtaskTotal, commentCount int, creator, assignee userRef) mmmodel.SlackAttachment {
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
		userRef{mention: "@alice"}, userRef{mention: "@bob"})

	// No AuthorName row — people live in the Actions row as 👤 chips.
	assert.Empty(t, card.AuthorName)
	assert.Empty(t, card.AuthorIcon)
	assert.Empty(t, card.Pretext)

	// Title + description preview body.
	assert.Equal(t, "Review PR", card.Title)
	assert.Equal(t, "OAuth login flow", card.Text, "description preview lives in Text")
	assert.Empty(t, card.Fields)

	// Actions: 4 chips — Status, Priority (clickable), then 👤 creator, 👤 assignee (decorative).
	require.Len(t, card.Actions, 4)
	// [0] Status chip — clickable.
	assert.Equal(t, "To Do", card.Actions[0].Name, "chip shows the current value only")
	assert.Equal(t, "primary", card.Actions[0].Style)
	require.NotNil(t, card.Actions[0].Integration)
	assert.Equal(t, cardActionCallbackPath, card.Actions[0].Integration.URL)
	assert.Equal(t, "status", card.Actions[0].Integration.Context["action"])
	assert.Equal(t, "T1", card.Actions[0].Integration.Context["task_id"])
	assert.False(t, card.Actions[0].Disabled)
	// [1] Priority chip — clickable.
	assert.Equal(t, "Standard", card.Actions[1].Name)
	assert.Equal(t, "default", card.Actions[1].Style, "standard priority → default style chip")
	require.NotNil(t, card.Actions[1].Integration)
	assert.Equal(t, "priority", card.Actions[1].Integration.Context["action"])
	// [2] Creator chip — decorative.
	assert.Equal(t, "👤 @alice", card.Actions[2].Name)
	assert.Equal(t, "default", card.Actions[2].Style)
	assert.True(t, card.Actions[2].Disabled, "creator chip is decorative")
	// [3] Assignee chip — decorative.
	assert.Equal(t, "👤 @bob", card.Actions[3].Name)
	assert.True(t, card.Actions[3].Disabled, "assignee chip is decorative")

	// Footer: due + progress only — people are chips now.
	assert.NotContains(t, card.Footer, "👤", "people moved out of the footer")
	assert.Contains(t, card.Footer, "📅")
	assert.Contains(t, card.Footer, "✓ 1/3")
	assert.Contains(t, card.Footer, "💬 2")
}

func TestBuildTaskCard_SelfAssignedOnePeopleChip(t *testing.T) {
	// When creator == assignee, only one 👤 chip is rendered (no redundant
	// duplicate for the same person).
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0,
		userRef{mention: "@alice"}, userRef{mention: "@alice"})
	// 3 chips: Status + Priority + 1 creator (= assignee).
	require.Len(t, card.Actions, 3)
	assert.Equal(t, "👤 @alice", card.Actions[2].Name)
}

func TestBuildTaskCard_NoPeopleNoPeopleChips(t *testing.T) {
	// Personal task (no creator mention, no assignee) → no 👤 chips.
	task := sampleTask(taskmodel.StatusTodo, nil)
	card := buildCard(task, 0, 0, 0, 0, userRef{}, userRef{})
	// 2 chips: Status + Priority only.
	require.Len(t, card.Actions, 2)
}

func TestBuildTaskCard_NoPeopleInFooter(t *testing.T) {
	// Personal task (no creator mention, no assignee) → footer omits the people
	// line entirely and only carries due/progress.
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	card := buildCard(task, 1_500_000_000_000, 0, 0, 0, userRef{}, userRef{})
	assert.NotContains(t, card.Footer, "👤")
	assert.Contains(t, card.Footer, "📅")
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

func TestCardActions_MinimumTwoChipsWhenNoPeople(t *testing.T) {
	// Status + Priority are always present. People chips only when set.
	actions := cardActions(cardInput{
		task: &taskmodel.Task{TaskRow: taskmodel.TaskRow{
			ID: "T9", Status: taskmodel.StatusTodo, Priority: taskmodel.PriorityStandard,
		}},
	})
	require.Len(t, actions, 2)
	assert.NotNil(t, actions[0].Integration, "Status chip is clickable")
	assert.NotNil(t, actions[1].Integration, "Priority chip is clickable")
	assert.False(t, actions[0].Disabled)
	assert.False(t, actions[1].Disabled)
}

func TestCardActions_StatusChipsByStatus(t *testing.T) {
	cases := []struct {
		status string
		style  string
	}{
		{taskmodel.StatusTodo, "primary"},
		{taskmodel.StatusInProgress, "warning"},
		{taskmodel.StatusDone, "good"},
		{taskmodel.StatusCancelled, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			actions := cardActions(cardInput{
				task: &taskmodel.Task{TaskRow: taskmodel.TaskRow{
					Status: tc.status, Priority: taskmodel.PriorityStandard,
				}},
			})
			assert.Equal(t, tc.style, actions[0].Style)
		})
	}
}

func TestCardActions_PriorityChipStyles(t *testing.T) {
	cases := []struct {
		priority string
		style    string
	}{
		{taskmodel.PriorityUrgent, "danger"},
		{taskmodel.PriorityImportant, "warning"},
		{taskmodel.PriorityStandard, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.priority, func(t *testing.T) {
			actions := cardActions(cardInput{
				task: &taskmodel.Task{TaskRow: taskmodel.TaskRow{
					Status: taskmodel.StatusTodo, Priority: tc.priority,
				}},
			})
			assert.Equal(t, tc.style, actions[1].Style)
		})
	}
}

func TestNextStatus(t *testing.T) {
	assert.Equal(t, taskmodel.StatusInProgress, nextStatus(taskmodel.StatusTodo))
	assert.Equal(t, taskmodel.StatusDone, nextStatus(taskmodel.StatusInProgress))
	assert.Equal(t, taskmodel.StatusTodo, nextStatus(taskmodel.StatusDone))
	// Cancelled is terminal in the cycle.
	assert.Equal(t, taskmodel.StatusCancelled, nextStatus(taskmodel.StatusCancelled))
}

func TestNextPriority(t *testing.T) {
	assert.Equal(t, taskmodel.PriorityImportant, nextPriority(taskmodel.PriorityStandard))
	assert.Equal(t, taskmodel.PriorityUrgent, nextPriority(taskmodel.PriorityImportant))
	assert.Equal(t, taskmodel.PriorityStandard, nextPriority(taskmodel.PriorityUrgent))
}

func TestCardFooter_FullSet(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	footer := cardFooter(task, cardInput{
		task: task, nowMs: 1_500_000_000_000,
		creator:      userRef{mention: "@alice"},
		assignee:     userRef{mention: "@bob"},
		subtaskDone:  2, subtaskTotal: 5, commentCount: 3,
	})
	// Footer no longer carries people — only due + progress.
	assert.NotContains(t, footer, "👤")
	assert.NotContains(t, footer, "@")
	assert.Contains(t, footer, "📅")
	assert.Contains(t, footer, "✓ 2/5")
	assert.Contains(t, footer, "💬 3")
}

func TestCardFooter_SkipsEmptyItems(t *testing.T) {
	task := sampleTask(taskmodel.StatusTodo, nil)
	assert.Empty(t, cardFooter(task, cardInput{task: task}))
}

func TestCardFooter_OnlyDue(t *testing.T) {
	due := int64(1_800_000_000_000)
	task := sampleTask(taskmodel.StatusTodo, &due)
	footer := cardFooter(task, cardInput{task: task, nowMs: 1_500_000_000_000})
	assert.Contains(t, footer, "📅")
	assert.NotContains(t, footer, "👤")
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

func TestPriorityChipLabel(t *testing.T) {
	assert.Equal(t, "Standard", priorityChipLabel(taskmodel.PriorityStandard))
	assert.Equal(t, "🟠 Important", priorityChipLabel(taskmodel.PriorityImportant))
	assert.Equal(t, "🔴 Urgent", priorityChipLabel(taskmodel.PriorityUrgent))
}

func TestDueLabel_Overdue(t *testing.T) {
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
	assert.Equal(t, "", priorityActionStyle(taskmodel.PriorityStandard), "standard → no style (bare)")
}

func TestPriorityActionStyleOr(t *testing.T) {
	assert.Equal(t, "danger", priorityActionStyleOr(taskmodel.PriorityUrgent))
	assert.Equal(t, "warning", priorityActionStyleOr(taskmodel.PriorityImportant))
	assert.Equal(t, "default", priorityActionStyleOr(taskmodel.PriorityStandard), "standard → default (visible chip)")
}

// handleCardAction requires a full plugin (service + store) so it lives in
// api_test.go alongside the other HTTP handler tests. The pure helpers above
// (nextStatus, nextPriority, peopleLine, cardActions, cardFooter) are fully
// unit-tested here.

// cardActionRequest builds the JSON body for POST /actions.
func cardActionRequest(action, taskID string) string {
	body, _ := json.Marshal(map[string]any{
		"context":    map[string]any{"action": action, "task_id": taskID},
		"user_id":    "u1",
		"post_id":    "post-1",
		"team_id":    "team-1",
		"channel_id": "ch1",
	})
	return string(body)
}

func TestHandleCardAction_StatusCycle(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{
		Summary: "p", ChannelID: "ch1", CreatorID: "u1", AssigneeID: "u2",
	})

	// todo → in_progress on chip click.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions",
		bytes.NewReader([]byte(cardActionRequest("status", parent.ID))))
	req.Header.Set("Mattermost-User-ID", "u1")
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, req)

	got, err := p.taskService.Get(parent.ID)
	require.NoError(t, err)
	assert.Equal(t, taskmodel.StatusInProgress, got.Status, "todo → in_progress on chip click")
}

func TestHandleCardAction_PriorityCycle(t *testing.T) {
	p, _ := newTestPlugin(t)
	parent := createTaskViaService(t, p, task.CreateInput{
		Summary: "p", ChannelID: "ch1", CreatorID: "u1", AssigneeID: "u2",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/actions",
		bytes.NewReader([]byte(cardActionRequest("priority", parent.ID))))
	req.Header.Set("Mattermost-User-ID", "u1")
	w := httptest.NewRecorder()
	p.ServeHTTP(nil, w, req)

	got, err := p.taskService.Get(parent.ID)
	require.NoError(t, err)
	assert.Equal(t, taskmodel.PriorityImportant, got.Priority, "standard → important on chip click")
}
