package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
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

// cardFieldOrder is the canonical order of card fields, mirroring the Quick
// List task item's meta row (Status pill, Priority dot, Due chip, Assignee
// mention, then the aggregate counters). Conditional fields are simply
// omitted, so the visible order never changes.
const (
	cardFieldStatus    = "Status"
	cardFieldPriority  = "Priority"
	cardFieldDue       = "Due"
	cardFieldAssignee  = "Assignee"
	cardFieldSubtasks  = "Subtasks"
	cardFieldComments  = "Comments"
)

// cardInput is the resolved, fully-resolved payload the pure card builder
// consumes. The Plugin method renderCard resolves the assignee mention (an
// I/O step) and hands the rest off to buildTaskCard so the builder stays a
// pure, easily-tested function.
type cardInput struct {
	task            *taskmodel.Task
	nowMs           int64
	assigneeMention string
	subtaskDone     int
	subtaskTotal    int
	commentCount    int
}

// buildTaskCard builds the SlackAttachment that renders a task as an
// information-only message card matching the Quick List task item (PLAN.md
// section 6.3 / issue #15). The card shows the summary, a description preview,
// and a meta row of fields — Status, Priority, Due, Assignee, Subtasks,
// Comments — in that order. There are NO action buttons: like the Quick List
// row, all interactions happen in the Task Details panel (opened by clicking
// the card).
//
// The card is built on the server and rendered natively by Mattermost's
// SlackAttachment renderer, so it works on mobile too. nowMs lets the overdue
// and "due soon" checks be deterministic in tests; pass time.Now().UnixMilli().
func buildTaskCard(in cardInput) model.SlackAttachment {
	t := in.task
	fields := []*model.SlackAttachmentField{
		{Title: cardFieldStatus, Value: statusLabel(t.Status), Short: true},
	}
	if label := priorityLabel(t.Priority); label != "" {
		fields = append(fields, &model.SlackAttachmentField{
			Title: cardFieldPriority, Value: label, Short: true,
		})
	}
	if t.DueAt != nil {
		fields = append(fields, &model.SlackAttachmentField{
			Title: cardFieldDue, Value: dueLabel(*t.DueAt, in.nowMs, t.Status), Short: true,
		})
	}
	if in.assigneeMention != "" {
		fields = append(fields, &model.SlackAttachmentField{
			Title: cardFieldAssignee, Value: in.assigneeMention, Short: true,
		})
	}
	if in.subtaskTotal > 0 {
		fields = append(fields, &model.SlackAttachmentField{
			Title: cardFieldSubtasks, Value: fmt.Sprintf("%d/%d done", in.subtaskDone, in.subtaskTotal), Short: true,
		})
	}
	if in.commentCount > 0 {
		fields = append(fields, &model.SlackAttachmentField{
			Title: cardFieldComments, Value: fmt.Sprintf("%d", in.commentCount), Short: true,
		})
	}

	return model.SlackAttachment{
		Title:     cardTitle(t),
		Fallback:  cardTitle(t),
		Text:      descriptionPreview(t.Description),
		Color:     cardColor(t, in.nowMs),
		Fields:    fields,
		Timestamp: t.CreatedAt / 1000,
	}
}

// cardTitle renders the card title, struck through for terminal statuses —
// matches the Quick List row's line-through on done/cancelled summaries.
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

// priorityLabel returns a card-friendly priority label, or "" when the priority
// is the default (standard) — mirroring the Quick List's PriorityDot, which is
// not rendered for standard tasks.
func priorityLabel(priority string) string {
	switch priority {
	case taskmodel.PriorityUrgent:
		return "🔴 Urgent"
	case taskmodel.PriorityImportant:
		return "🟠 Important"
	default:
		return ""
	}
}

// dueLabel renders the due date as a short relative string, with an "Nd
// overdue" suffix when past and the task is still open. Mirrors the Quick
// List's formatDueRelative as closely as Go's time formatting allows: same-day
// → "Today, HH:MM", tomorrow → "Tomorrow", within 7 days → "Mon, 2 Jun", same
// year → "Mon, 15 Jun", other years → "Mon, 15 Jun 2027".
//
// nowMs lets the overdue check be deterministic in tests.
func dueLabel(dueMs, nowMs int64, status string) string {
	due := time.UnixMilli(dueMs).Local()
	now := time.UnixMilli(nowMs).Local()
	today := startOfDay(now)
	dueDay := startOfDay(due)
	dayDiff := int(dueDay.Sub(today).Hours() / 24)

	open := status == taskmodel.StatusTodo || status == taskmodel.StatusInProgress
	if open && dayDiff < 0 {
		return fmt.Sprintf("%d day%s overdue", -dayDiff, plural(-dayDiff))
	}
	switch dayDiff {
	case 0:
		return "Today, " + due.Format("15:04")
	case 1:
		return "Tomorrow"
	case -1:
		return "Yesterday"
	}
	if due.Year() == today.Year() {
		return due.Format("Mon, 2 Jan")
	}
	return due.Format("Mon, 2 Jan 2006")
}

// plural returns "s" when n != 1, "" otherwise — used for "1 day overdue" vs
// "3 days overdue".
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// startOfDay returns t clamped to local midnight, matching the TS helper used
// by formatDueRelative so dayDiff is computed on calendar days, not 24h
// windows.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// descriptionPreview collapses a description into a single short preview line
// for the card body, mirroring the Quick List's truncateDescription. An empty
// description yields "" (no Text body). Whitespace runs collapse to single
// spaces; the cut lands on a word boundary with an ellipsis when truncated.
const descriptionPreviewMax = 100

func descriptionPreview(text string) string {
	flat := strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if flat == "" {
		return ""
	}
	if len(flat) <= descriptionPreviewMax {
		return flat
	}
	slice := flat[:descriptionPreviewMax]
	if i := strings.LastIndex(slice, " "); i > 0 {
		slice = slice[:i]
	}
	return slice + "…"
}

// resolveMention returns a real "@username" mention for userID, falling back to
// the raw "@<id>" form when the user can't be resolved (deleted user, RPC
// error). The card previously rendered the raw id; resolving to the username
// lets Mattermost render a real mention.
func (p *Plugin) resolveMention(userID string) string {
	if userID == "" {
		return ""
	}
	if u, err := p.API.GetUser(userID); err == nil && u != nil && u.Username != "" {
		return "@" + u.Username
	}
	return "@" + userID
}

// renderCard builds the task card with the assignee mention resolved. Used by
// the post/update paths so the mention is always current; buildTaskCard itself
// stays a pure function for tests.
func (p *Plugin) renderCard(t *taskmodel.Task) model.SlackAttachment {
	done, total := p.subtaskProgress(t.ID)
	comments := p.commentCount(t.ID)
	return buildTaskCard(cardInput{
		task:            t,
		nowMs:           nowMillis(),
		assigneeMention: p.resolveMention(t.AssigneeID),
		subtaskDone:     done,
		subtaskTotal:    total,
		commentCount:    comments,
	})
}

// taskCardProps builds the post.Props for a task card: the attachment plus the
// task_id the webapp reads to open Task Details on click.
func taskCardProps(t *taskmodel.Task, attachment *model.SlackAttachment) map[string]any {
	return map[string]any{
		"attachments": []*model.SlackAttachment{attachment},
		"task_id":     t.ID,
	}
}

// postCard creates a post with the task card as an attachment in channelID
// (author = bot) and returns the post id. Used to post the card when a task is
// created in a channel.
func (p *Plugin) postCard(channelID string, t *taskmodel.Task) string {
	attachment := p.renderCard(t)
	post := &model.Post{
		UserId:    p.botUserID,
		ChannelId: channelID,
		Type:      "custom_task",
		Props:     taskCardProps(t, &attachment),
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
	if err != nil || channel == nil {
		// GetDirectChannel can return (nil, nil) during RPC shutdown; guard
		// against a nil-pointer on channel.Id below.
		if err != nil {
			p.API.LogError("Failed to open DM for task card", "assignee_id", assigneeID, "error", err)
		}
		return ""
	}
	return p.postCard(channel.Id, t)
}

// postCardReply posts the task card as a thread reply rooted at rootPostID in
// channelID, and returns the reply post id. Used to post a subtask inside its
// parent's thread so the parent's conversation groups the subtasks together.
// A task with no parent card (empty rootPostID) is posted top-level instead,
// matching the pre-subtask behaviour.
func (p *Plugin) postCardReply(rootPostID, channelID string, t *taskmodel.Task) string {
	attachment := p.renderCard(t)
	post := &model.Post{
		UserId:    p.botUserID,
		ChannelId: channelID,
		RootId:    rootPostID,
		Type:      "custom_task",
		Props:     taskCardProps(t, &attachment),
	}
	created, err := p.API.CreatePost(post)
	if err != nil {
		p.API.LogError("Failed to post task card reply",
			"root_id", rootPostID, "channel_id", channelID, "task_id", t.ID, "error", err)
		return ""
	}
	if created != nil {
		return created.Id
	}
	return ""
}

// updateCard re-renders the task card and updates the existing post (identified
// by postID) with the new attachment. No-op when postID is empty or the update
// fails (logged). Used when a task changes so the card stays in sync.
func (p *Plugin) updateCard(postID string, t *taskmodel.Task) {
	if postID == "" {
		return
	}
	post, err := p.API.GetPost(postID)
	if err != nil || post == nil {
		p.API.LogError("Failed to load post for card update", "post_id", postID, "error", err)
		return
	}
	attachment := p.renderCard(t)
	post.Props = taskCardProps(t, &attachment)
	if _, err := p.API.UpdatePost(post); err != nil {
		p.API.LogError("Failed to update task card", "post_id", postID, "error", err)
	}
}

// updateTaskCards refreshes EVERY tracked card for the task (channel, DM, and
// any future locations) by listing task_posts rather than hard-coding two
// columns. A task may be posted in several places, and a status/assignee change
// must update them all. A deleted post is skipped (defensive self-heal) so one
// stale card can't block the rest.
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
