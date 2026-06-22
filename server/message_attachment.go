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

// metaSeparator is the middot used to join inline meta items in the card body.
const metaSeparator = " · "

// userRef is the resolved display info for a user id: the @mention string and
// the avatar URL. Both are "" when the id is empty or unresolvable.
type userRef struct {
	mention   string // "@alice"
	avatarURL string // https://.../avatar
}

// statusActionStyle maps a task status to a SlackAttachment button style.
// These style the decorative status chip rendered as a disabled PostAction at
// the front of the Actions row: Mattermost maps good/warning/danger/primary/
// default to the active theme's semantic colors, so the chip reads at a glance
// without any bold text label.
func statusActionStyle(status string) string {
	switch status {
	case taskmodel.StatusDone:
		return "good" // green
	case taskmodel.StatusInProgress:
		return "warning" // amber
	case taskmodel.StatusCancelled:
		return "default" // grey
	case taskmodel.StatusTodo:
		fallthrough
	default:
		return "primary" // blue
	}
}

// priorityActionStyle maps an elevated priority to a button style, or "" when
// the priority is standard (no chip rendered). Urgent → danger (red), Important
// → warning (amber), mirroring the Quick List PriorityDot.
func priorityActionStyle(priority string) string {
	switch priority {
	case taskmodel.PriorityUrgent:
		return "danger"
	case taskmodel.PriorityImportant:
		return "warning"
	default:
		return ""
	}
}

// cardInput is the resolved payload the pure card builder consumes. The Plugin
// method renderCard resolves the user mentions (an I/O step) and hands the
// rest off to buildTaskCard so the builder stays a pure, easily-tested fn.
type cardInput struct {
	task          *taskmodel.Task
	nowMs         int64
	creator       userRef
	assignee      userRef
	taskPermalink string // absolute URL to the task; "" omits TitleLink
	subtaskDone   int
	subtaskTotal  int
	commentCount  int
}

// buildTaskCard builds the SlackAttachment that renders a task as a compact
// card. Both the creator and the assignee are shown in the footer, prefixed
// with a 👤 emoji so each reads as a small "author" entry. Status and Priority
// are colored chips in the Actions row that the user can click to cycle the
// value (todo→in_progress→done→todo; standard→important→urgent→standard).
//
//	Title     = task summary (struck through when done/cancelled)
//	TitleLink = task permalink (when site URL is configured)
//	Text      = description preview (muted, single line)
//	Actions   = [ Status chip ] [ Priority chip ] (clickable, cycle on click)
//	Footer    = "👤 @creator → 👤 @assignee · 📅 Tomorrow · ✓ 2/5 · 💬 3"
//
// Conditional items are skipped (no due, standard priority, no description),
// so a minimal task card stays compact. Other interactions happen in the Task
// Details panel (opened by clicking the card). The card is rendered natively
// by Mattermost's SlackAttachment renderer, so it works on mobile too. nowMs
// lets the overdue check be deterministic in tests.
func buildTaskCard(in cardInput) model.SlackAttachment {
	t := in.task
	card := model.SlackAttachment{
		Title:     cardTitle(t),
		Fallback:  cardTitle(t),
		Text:      descriptionPreview(t.Description),
		Color:     cardColor(t, in.nowMs),
		Actions:   cardActions(t),
		Timestamp: t.CreatedAt / 1000,
	}

	// Title is clickable so it visibly invites interaction. The webapp's
	// delegated click handler already opens Task Details for custom_task posts;
	// the link is a fallback for plain-text contexts (mobile push, search
	// results) where the JS handler doesn't run.
	if in.taskPermalink != "" {
		card.TitleLink = in.taskPermalink
	}

	// Footer carries the people and the metadata in a single small line at the
	// bottom: "👤 @creator → 👤 @assignee · 📅 Tomorrow · ✓ 2/5 · 💬 3". Both
	// the creator and assignee are prefixed with a 👤 emoji so each reads like
	// a small author entry (SlackAttachment only allows one real AuthorIcon, so
	// the footer uses emoji placeholders). Each part is skipped when empty.
	if footer := cardFooter(t, in); footer != "" {
		card.Footer = footer
	}

	return card
}

// cardFooter assembles the single-line footer. The people come first — the
// creator, then an arrow, then the assignee (when distinct) — so the chain of
// custody reads left-to-right. Due date, subtask progress, and comment count
// follow, each skipped when empty. Returns "" when nothing is set.
func cardFooter(t *taskmodel.Task, in cardInput) string {
	parts := make([]string, 0, 5)

	// People: "👤 @creator → 👤 @assignee". The arrow is dropped when the two
	// are the same (or the assignee is empty), so a self-assigned task reads
	// just "👤 @creator" with no redundant self-arrow.
	if in.creator.mention != "" || in.assignee.mention != "" {
		parts = append(parts, peopleLine(in.creator, in.assignee))
	}
	if t.DueAt != nil {
		parts = append(parts, "📅 "+dueLabel(*t.DueAt, in.nowMs, t.Status))
	}
	if in.subtaskTotal > 0 {
		parts = append(parts, fmt.Sprintf("✓ %d/%d", in.subtaskDone, in.subtaskTotal))
	}
	if in.commentCount > 0 {
		parts = append(parts, fmt.Sprintf("💬 %d", in.commentCount))
	}
	return strings.Join(parts, metaSeparator)
}

// peopleLine renders the creator → assignee chain for the footer. The arrow is
// omitted when the two mentions are equal (self-assigned) or when the assignee
// is empty, so the line never reads "👤 @alice → 👤 @alice". Returns "" when
// neither mention is set so the caller can skip the people part entirely.
func peopleLine(creator, assignee userRef) string {
	if creator.mention == "" && assignee.mention == "" {
		return ""
	}
	if assignee.mention == "" || creator.mention == assignee.mention {
		// Only the creator, or self-assigned → single entry.
		if creator.mention != "" {
			return "👤 " + creator.mention
		}
		return "👤 " + assignee.mention
	}
	if creator.mention == "" {
		return "👤 " + assignee.mention
	}
	return "👤 " + creator.mention + " → 👤 " + assignee.mention
}

// cardActionCallbackPath is the plugin-scoped URL the Status/Priority chips
// POST to. Mattermost requires PostActionIntegration URLs to use the
// /plugins/{plugin_id}/... form for routing + internal auth. The handler
// (handleCardAction) reads context.action + context.task_id and cycles the
// corresponding value.
const cardActionCallbackPath = "/plugins/com.mattermost.plugin-task/api/v1/actions"

// cardActionKind identifies which value a chip cycles: status or priority.
type cardActionKind string

const (
	actionStatus   cardActionKind = "status"
	actionPriority cardActionKind = "priority"
)

// cardActions builds the clickable chip row. Each chip shows the task's
// CURRENT value and, on click, advances to the next one in the cycle
// (todo→in_progress→done→todo; standard→important→urgent→standard). The label
// never telegraphs the next value — clicking is the affordance, and the card
// refreshes in place to reveal it. Both POST to /api/v1/actions with
// {action, task_id} in context.
func cardActions(t *taskmodel.Task) []*model.PostAction {
	return []*model.PostAction{
		{
			Name:        statusLabel(t.Status),
			Type:        "button",
			Style:       statusActionStyle(t.Status),
			Integration: cardIntegration(actionStatus, t.ID),
		},
		{
			Name:        priorityChipLabel(t.Priority),
			Type:        "button",
			Style:       priorityActionStyleOr(t.Priority),
			Integration: cardIntegration(actionPriority, t.ID),
		},
	}
}

// cardIntegration builds the PostActionIntegration pointing at the chip-action
// callback with the kind + task_id in context.
func cardIntegration(kind cardActionKind, taskID string) *model.PostActionIntegration {
	return &model.PostActionIntegration{
		URL: cardActionCallbackPath,
		Context: map[string]any{
			"action":  string(kind),
			"task_id": taskID,
		},
	}
}

// nextStatus returns the next status in the cycle todo→in_progress→done→todo.
// Cancelled is terminal in the cycle (clicking a Cancelled chip does nothing),
// matching the rule that reopening from cancelled must go via Task Details.
func nextStatus(status string) string {
	switch status {
	case taskmodel.StatusTodo:
		return taskmodel.StatusInProgress
	case taskmodel.StatusInProgress:
		return taskmodel.StatusDone
	case taskmodel.StatusDone:
		return taskmodel.StatusTodo
	default:
		return status
	}
}

// nextPriority returns the next priority in the cycle
// standard→important→urgent→standard.
func nextPriority(priority string) string {
	switch priority {
	case taskmodel.PriorityImportant:
		return taskmodel.PriorityUrgent
	case taskmodel.PriorityUrgent:
		return taskmodel.PriorityStandard
	default:
		return taskmodel.PriorityImportant
	}
}

// priorityChipLabel returns the label shown on the Priority chip for every
// priority (including Standard, which is rendered with a "default" style so
// the chip is always present and clickable).
func priorityChipLabel(priority string) string {
	switch priority {
	case taskmodel.PriorityUrgent:
		return "🔴 Urgent"
	case taskmodel.PriorityImportant:
		return "🟠 Important"
	default:
		return "Standard"
	}
}

// priorityActionStyleOr is like priorityActionStyle but returns "default" for
// standard priority instead of "", so the Standard chip still renders with a
// visible (neutral) style.
func priorityActionStyleOr(priority string) string {
	if s := priorityActionStyle(priority); s != "" {
		return s
	}
	return "default"
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

// resolveUser returns the display info for userID: the "@username" mention and
// the avatar URL. Both are "" when the id is empty. Falls back to the raw id
// for the mention and leaves the avatar empty when the user can't be resolved
// (deleted user, RPC error). Used by renderCard for the creator author row and
// the assignee footer.
func (p *Plugin) resolveUser(userID string) userRef {
	if userID == "" {
		return userRef{}
	}
	u, err := p.API.GetUser(userID)
	if err != nil || u == nil {
		return userRef{mention: "@" + userID}
	}
	mention := "@" + userID
	if u.Username != "" {
		mention = "@" + u.Username
	}
	return userRef{mention: mention, avatarURL: p.avatarURL(userID)}
}

// avatarURL builds the canonical profile-image URL for userID under the
// configured site URL. Returns "" when the site URL isn't configured, so the
// card renders without an avatar instead of a broken-image link.
func (p *Plugin) avatarURL(userID string) string {
	site := p.getSiteURL()
	if site == "" {
		return ""
	}
	return site + "/api/v4/users/" + userID + "/image"
}

// getSiteURL returns the configured site URL without a trailing slash. Used to
// build the profile-image URL and (later) the task permalink.
func (p *Plugin) getSiteURL() string {
	siteURL := *p.API.GetConfig().ServiceSettings.SiteURL
	if siteURL == "" {
		return ""
	}
	return strings.TrimRight(siteURL, "/")
}

// renderCard builds the task card with the creator + assignee mentions
// resolved. Used by the post/update paths so the footer stays current;
// buildTaskCard itself stays a pure function for tests.
func (p *Plugin) renderCard(t *taskmodel.Task) model.SlackAttachment {
	done, total := p.subtaskProgress(t.ID)
	comments := p.commentCount(t.ID)
	return buildTaskCard(cardInput{
		task:         t,
		nowMs:        nowMillis(),
		creator:      p.resolveUser(t.CreatorID),
		assignee:     p.resolveUser(t.AssigneeID),
		subtaskDone:  done,
		subtaskTotal: total,
		commentCount: comments,
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
