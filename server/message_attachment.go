package main

import (
	"context"
	"fmt"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
)

// cardActionCallbackPath is the plugin-scoped URL interactive card buttons POST
// to. Mattermost requires PostActionIntegration URLs to use the
// /plugins/{plugin_id}/... form for routing + internal auth (without it the
// callback is treated as an external request and fails). The handler
// (handleCardAction) reads context.action + context.task_id.
const cardActionCallbackPath = "/plugins/com.mattermost.plugin-task/api/v1/actions"

// cardAction is an interactive button on a task card.
type cardAction string

const (
	actionDone    cardAction = "done"
	actionCancel  cardAction = "cancel"
	actionAssign  cardAction = "assign"
	actionSubtask cardAction = "subtask"
	actionComment cardAction = "comment"
)

// statusColors map a task status to a SlackAttachment color (predefined styles
// or hex). Done/cancelled are de-emphasized; overdue tasks turn red via
// cardColor overriding this.
var statusColors = map[string]string{
	taskmodel.StatusTodo:       "#4391FE", // blue
	taskmodel.StatusInProgress: "#F1A93C", // amber
	taskmodel.StatusDone:       "#1A7140", // green
	taskmodel.StatusCancelled:  "#8A8A8A", // grey
}

// buildTaskCard builds the SlackAttachment that renders a task as an interactive
// message card (PLAN.md section 6.3 / issue #15). It shows summary, assignee,
// due (red when overdue), status, subtask progress, comment count, and the
// action buttons Done/Cancel/Assign/Subtask/Comment.
//
// nowMs lets the overdue check be deterministic in tests; pass time.Now().UnixMilli().
// subtaskDone/subtaskTotal render the "x/y" progress (pass 0/0 when none).
// commentCount renders a "Comments: N" indicator when > 0 (issue #25).
func buildTaskCard(t *taskmodel.Task, nowMs int64, subtaskDone, subtaskTotal, commentCount int) model.SlackAttachment {
	fields := []*model.SlackAttachmentField{
		{Title: "Status", Value: statusLabel(t.Status), Short: true},
	}
	if t.AssigneeID != "" {
		fields = append(fields, &model.SlackAttachmentField{
			Title: "Assignee", Value: mention(t.AssigneeID), Short: true,
		})
	}
	if t.DueAt != nil {
		fields = append(fields, &model.SlackAttachmentField{
			Title: "Due", Value: dueLabel(*t.DueAt, nowMs), Short: true,
		})
	}
	if subtaskTotal > 0 {
		fields = append(fields, &model.SlackAttachmentField{
			Title: "Subtasks", Value: fmt.Sprintf("%d/%d done", subtaskDone, subtaskTotal), Short: true,
		})
	}
	if commentCount > 0 {
		fields = append(fields, &model.SlackAttachmentField{
			Title: "Comments", Value: fmt.Sprintf("%d", commentCount), Short: true,
		})
	}

	return model.SlackAttachment{
		Title:     cardTitle(t),
		Fallback:  cardTitle(t),
		Color:     cardColor(t, nowMs),
		Fields:    fields,
		Actions:   taskCardActions(t.ID, t.Status),
		Timestamp: t.CreatedAt / 1000,
	}
}

// cardTitle renders the card title, struck through for terminal statuses.
func cardTitle(t *taskmodel.Task) string {
	switch t.Status {
	case taskmodel.StatusDone, taskmodel.StatusCancelled:
		return "~~" + t.Summary + "~~"
	default:
		return t.Summary
	}
}

// cardColor returns the attachment color: red for overdue open tasks, else the
// status color.
func cardColor(t *taskmodel.Task, nowMs int64) string {
	if t.DueAt != nil && *t.DueAt < nowMs &&
		(t.Status == taskmodel.StatusTodo || t.Status == taskmodel.StatusInProgress) {
		return "#D92D20" // red, overdue
	}
	if c, ok := statusColors[t.Status]; ok {
		return c
	}
	return statusColors[taskmodel.StatusTodo]
}

// statusLabel returns a human-friendly status label for the card.
func statusLabel(status string) string {
	switch status {
	case taskmodel.StatusTodo:
		return "To Do"
	case taskmodel.StatusInProgress:
		return "In Progress"
	case taskmodel.StatusDone:
		return "✅ Done"
	case taskmodel.StatusCancelled:
		return "🚫 Cancelled"
	default:
		return status
	}
}

// dueLabel renders the due date relative to now, with an "overdue" marker when
// past. We render the absolute ms timestamp as a readable marker; full date
// formatting by the client is a future enhancement.
func dueLabel(dueMs, nowMs int64) string {
	base := fmt.Sprintf("<!date^%d|due>", dueMs/1000)
	if dueMs < nowMs {
		return "🔴 " + base + " (overdue)"
	}
	return base
}

// mention renders an @mention marker for a user id. Mattermost renders the raw
// id in posts; a real @mention needs the username, but the card is built on the
// server where we only have the id — the client/user resolves it.
func mention(userID string) string {
	return "@" + userID
}

// taskCardActions builds the interactive buttons for a task card. Terminal
// statuses disable the Done/Cancel actions to reflect that the task is final.
func taskCardActions(taskID, status string) []*model.PostAction {
	terminal := status == taskmodel.StatusDone || status == taskmodel.StatusCancelled
	doneStyle, cancelStyle := "good", "danger"
	if terminal {
		doneStyle, cancelStyle = "default", "default"
	}
	return []*model.PostAction{
		{Name: "✓ Done", Type: "button", Style: doneStyle, Disabled: terminal && status == taskmodel.StatusDone, Integration: cardIntegration(actionDone, taskID)},
		{Name: "🚫 Cancel", Type: "button", Style: cancelStyle, Disabled: terminal && status == taskmodel.StatusCancelled, Integration: cardIntegration(actionCancel, taskID)},
		{Name: "👤 Assign", Type: "button", Style: "default", Integration: cardIntegration(actionAssign, taskID)},
		{Name: "➕ Subtask", Type: "button", Style: "default", Integration: cardIntegration(actionSubtask, taskID)},
		{Name: "💬 Comment", Type: "button", Style: "default", Integration: cardIntegration(actionComment, taskID)},
	}
}

// cardIntegration builds the PostActionIntegration pointing at the card-action
// callback with the action + task_id in context.
func cardIntegration(action cardAction, taskID string) *model.PostActionIntegration {
	return &model.PostActionIntegration{
		URL: cardActionCallbackPath,
		Context: map[string]any{
			"action":  string(action),
			"task_id": taskID,
		},
	}
}

// postCard creates a post with the task card as an attachment in channelID
// (author = bot) and returns the post id. Used to post the card when a task is
// created in a channel.
func (p *Plugin) postCard(channelID string, t *taskmodel.Task) string {
	done, total := p.subtaskProgress(t.ID)
	comments := p.commentCount(t.ID)
	attachment := buildTaskCard(t, nowMillis(), done, total, comments)
	post := &model.Post{
		UserId:    p.botUserID,
		ChannelId: channelID,
		Type:      "custom_task",
		Props: map[string]any{
			"attachments": []*model.SlackAttachment{&attachment},
		},
	}
	created, err := p.API.CreatePost(post)
	if err != nil {
		p.API.LogError("Failed to post task card", "channel_id", channelID, "task_id", t.ID, "error", err)
		return ""
	}
	if created != nil {
		return created.Id
	}
	return ""
}

// postCardDM posts the task card as a DM to assigneeID from the bot and returns
// the post id. Used to notify an assignee when a task is created/assigned.
func (p *Plugin) postCardDM(assigneeID string, t *taskmodel.Task) string {
	channel, err := p.API.GetDirectChannel(assigneeID, p.botUserID)
	if err != nil {
		p.API.LogError("Failed to open DM for task card", "assignee_id", assigneeID, "error", err)
		return ""
	}
	return p.postCard(channel.Id, t)
}

// updateCard re-renders the task card and updates the existing post (identified
// by postID) with the new attachment. No-op when postID is empty or the update
// fails (logged). Used when a task's status changes.
func (p *Plugin) updateCard(postID string, t *taskmodel.Task) {
	if postID == "" {
		return
	}
	post, err := p.API.GetPost(postID)
	if err != nil || post == nil {
		p.API.LogError("Failed to load post for card update", "post_id", postID, "error", err)
		return
	}
	done, total := p.subtaskProgress(t.ID)
	comments := p.commentCount(t.ID)
	attachment := buildTaskCard(t, nowMillis(), done, total, comments)
	post.Props["attachments"] = []*model.SlackAttachment{&attachment}
	if _, err := p.API.UpdatePost(post); err != nil {
		p.API.LogError("Failed to update task card", "post_id", postID, "error", err)
	}
}

// updateTaskCards refreshes EVERY tracked card for the task (channel, DM, and
// any future locations) by listing task_posts rather than hard-coding two
// columns. This is the post-migration card-refresh path: a task may be posted
// in several places, and a status/assignee change must update them all. A
// deleted post is skipped (defensive self-heal) so one stale card can't block
// the rest.
func (p *Plugin) updateTaskCards(t *taskmodel.Task) {
	if t == nil {
		return
	}
	posts := p.taskPosts(t.ID)
	for _, tp := range posts {
		p.updateCard(tp.PostID, t)
	}
}

// taskPosts returns the tracked card posts for taskID, or nil on error
// (best-effort — refreshing fewer cards beats failing the whole transition).
//
// p.taskStore is always set after OnActivate (the SQL store is wired before
// the router serves), so a nil store only occurs in degenerate test setups
// where card refresh isn't exercised. There is no fallback to the assembled
// Task's ChannelPostID/DMPostID: the normalized task_posts table is the single
// source of truth for card locations post-migration.
func (p *Plugin) taskPosts(taskID string) []taskmodel.TaskPost {
	if p.taskStore == nil {
		return nil
	}
	posts, err := p.taskStore.ListPosts(context.Background(), taskID)
	if err != nil {
		p.API.LogDebug("Failed to list task posts for card refresh", "task_id", taskID, "error", err)
		return nil
	}
	return posts
}

// commentCount returns the number of comments on taskID, or 0 on error
// (best-effort — a card without the indicator is better than no card). Used to
// render the "Comments: N" indicator (issue #25).
func (p *Plugin) commentCount(taskID string) int {
	if p.taskService == nil {
		return 0
	}
	ids, err := p.taskService.ListComments(taskID)
	if err != nil {
		p.API.LogDebug("Failed to count comments", "task_id", taskID, "error", err)
		return 0
	}
	return len(ids)
}

// recentComments returns up to limit most-recent comment bodies on taskID
// (creation order), or nil on error. Used to seed the Task Detail dialog's
// read-only comment preview (issue #25).
//
// In the comment-as-thread design the comment body lives in the Mattermost
// subtaskProgress returns (done, total) for the task's subtasks, or (0, 0) on
// error (best-effort — a card without progress is better than no card).
func (p *Plugin) subtaskProgress(taskID string) (done, total int) {
	if p.taskService == nil {
		return 0, 0
	}
	d, t, err := p.taskService.SubtaskProgress(taskID)
	if err != nil {
		p.API.LogDebug("Failed to compute subtask progress", "task_id", taskID, "error", err)
		return 0, 0
	}
	return d, t
}

// nowMillis returns the current time in ms; factored out for tests.
var nowMillis = func() int64 { return time.Now().UnixMilli() }
