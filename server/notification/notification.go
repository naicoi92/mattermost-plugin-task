// Package notification centralizes direct-message notifications for task
// events, sent from the plugin bot and translated via the server i18n helper
// using the recipient's locale (PLAN.md section 7).
//
// All task-event DMs route through this package so the rules are consistent.
// Messages are plain text (no message attachment / custom post type) and carry
// a consistent context block: clickable task name, short id, status, due date,
// and the actor's display name. The bot never @-mentions anyone in a DM 1-1 —
// the recipient is implicit and mentioning the actor is noise (see change
// notification-overdue-and-context, specs/task-notification).
package notification

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
)

// API is the subset of the Mattermost plugin API the notifier needs. Declaring
// it locally keeps the package unit-testable with a fake and decoupled from the
// concrete plugin.API shape.
type API interface {
	// GetUser returns the user with id (used to read Locale for i18n and the
	// display name for message rendering).
	GetUser(userID string) (*model.User, error)
	// GetDirectChannel returns the DM channel between the two users, creating
	// it if necessary.
	GetDirectChannel(userID1, userID2 string) (*model.Channel, error)
	// CreatePost posts a message as the bot.
	CreatePost(post *model.Post) (*model.Post, error)
	// LogError logs an error for debugging.
	LogError(message string, keyValuePairs ...any)
}

// Translator localizes a message key for a locale.
type Translator interface {
	T(locale, key string, args ...any) string
}

// defaultLocale is used when a recipient has no locale preference.
const defaultLocale = "en"

// shortIDLen is the number of leading ULID characters shown as a short task
// reference. ULIDs carry a millisecond timestamp prefix, so 8 chars stay unique
// in practice (collision only within the same millisecond).
const shortIDLen = 8

// PreviewMaxRunes is the maximum length (in runes) of a comment preview shown
// in a commented notification before truncation.
const PreviewMaxRunes = 100

// Notifier sends task-event DMs from the bot, translating text to each
// recipient's locale.
type Notifier struct {
	api        API
	translator Translator
	botUserID  string
	// siteURL is the absolute host URL without a trailing slash, used to build
	// the /plug/<plugin-id>/task/<id> deep-link so the task name in a DM is
	// clickable. Empty means the site URL isn't configured: the task name falls
	// back to plain text (graceful degradation).
	siteURL string
	// pluginID is the plugin id used in the deep-link path.
	pluginID string
	// nowMs returns the current time in ms UTC; used to compute the due band for
	// the DM emoji prefix (⚠ warning, 🔴 danger). Indirected so tests can pin
	// time without touching the clock.
	nowMs func() int64
}

// New returns a Notifier that posts DMs as botUserID, translating via t. The
// siteURL and pluginID enable clickable task-name deep-links; pass empty
// siteURL when ServiceSettings.SiteURL is unset.
func New(api API, t Translator, botUserID, siteURL, pluginID string) *Notifier {
	return &Notifier{
		api:        api,
		translator: t,
		botUserID:  botUserID,
		siteURL:    strings.TrimRight(siteURL, "/"),
		pluginID:   pluginID,
		nowMs:      func() int64 { return time.Now().UnixMilli() },
	}
}

// localeFor returns the user's locale or the default when unknown/unavailable.
func (n *Notifier) localeFor(userID string) string {
	if n.api == nil {
		return defaultLocale
	}
	user, err := n.api.GetUser(userID)
	if err != nil || user == nil {
		return defaultLocale
	}
	if user.Locale != "" {
		return user.Locale
	}
	return defaultLocale
}

// postDM posts an already-localized message to recipientID. Best-effort: a
// failure to open the DM channel or create the post is logged but not returned,
// so notifications never break the originating task operation.
func (n *Notifier) postDM(recipientID, message string) {
	if recipientID == "" || n.botUserID == "" {
		return
	}
	channel, err := n.api.GetDirectChannel(recipientID, n.botUserID)
	if err != nil || channel == nil {
		// GetDirectChannel can return (nil, nil) or (nil, err) when the RPC
		// connection to the host is shutting down. A nil channel here would
		// panic on channel.Id below — bail out instead.
		if err != nil {
			n.api.LogError("Failed to open DM channel", "recipient", recipientID, "error", err)
		}
		return
	}
	post := &model.Post{
		UserId:    n.botUserID,
		ChannelId: channel.Id,
		Message:   message,
		Type:      model.PostTypeDefault,
	}
	if _, err := n.api.CreatePost(post); err != nil {
		n.api.LogError("Failed to post notification DM", "recipient", recipientID, "error", err)
	}
}

// TaskSummary is a minimal view of a task for notification rendering.
type TaskSummary struct {
	ID      string
	Summary string
	// Status is the current task status (todo/in_progress/done/cancelled),
	// localized into a label at render time. Not rendered for the completed and
	// cancelled events (the verb already conveys terminal status).
	Status string
	// DueAt is the due timestamp in ms UTC; nil means no due date (the due
	// clause is omitted entirely rather than rendered as empty).
	DueAt *int64
	// IsAllDay marks the due as date-only for formatting.
	IsAllDay bool
	// CommentPreview is the plain-text preview of a comment's content, used only
	// by the commented event. Empty string omits the preview clause.
	CommentPreview string
}

// shortID returns the leading characters of the task id as a short reference.
func shortID(id string) string {
	if utf8.RuneCountInString(id) >= shortIDLen {
		return id[:shortIDLen]
	}
	return id
}

// renderTaskNameLink returns the task summary as a markdown link to the plugin
// deep-link route, so the recipient can click the task name to open Task
// Details. Returns plain text when the site URL or plugin id is unset (graceful
// degradation instead of a broken link).
func (n *Notifier) renderTaskNameLink(taskID, summary string) string {
	if n.siteURL == "" || n.pluginID == "" {
		return summary
	}
	permalink := n.siteURL + "/plug/" + n.pluginID + "/task/" + taskID
	return fmt.Sprintf("[%s](%s)", escapeMarkdown(summary), permalink)
}

// escapeMarkdown escapes the bracket and paren characters that would otherwise
// break an inline markdown link's [label](url) syntax.
func escapeMarkdown(s string) string {
	r := strings.NewReplacer(
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
	)
	return r.Replace(s)
}

// formatDue formats a due timestamp (ms UTC) for the recipient's locale.
// isAllDay renders a date-only string. UTC is used because the plugin does not
// know the recipient's timezone (design D6).
func formatDue(locale string, dueMs int64, isAllDay bool) string {
	t := time.UnixMilli(dueMs).UTC()
	layout := "2006-01-02"
	if !isAllDay {
		layout = "2006-01-02 15:04"
	}
	if locale == "vi" {
		if isAllDay {
			layout = "02/01/2006"
		} else {
			layout = "02/01/2006 15:04"
		}
	}
	return t.Format(layout)
}

// overdueDays returns the whole number of days a task is overdue, floored to a
// minimum of 1 so a task even a few hours past due still reads "1 day".
func overdueDays(nowMs, dueMs int64) int {
	const msPerDay = int64(24*time.Hour) / int64(time.Millisecond)
	days := max(int((nowMs-dueMs)/msPerDay), 1)
	return days
}

// formatOverdueDuration renders the localized "N days overdue" duration string.
func (n *Notifier) formatOverdueDuration(locale string, nowMs, dueMs int64) string {
	return n.translator.T(locale, "notification.overdue.duration", overdueDays(nowMs, dueMs))
}

// markdownLinkRe matches an inline markdown link, capturing its label text so
// stripMarkdown can replace [text](url) with just text for a plain preview.
var markdownLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// stripMarkdown flattens markdown into plain text for a comment preview: link
// labels resolve to their text and the common emphasis markers are dropped.
func stripMarkdown(s string) string {
	s = markdownLinkRe.ReplaceAllString(s, "$1")
	r := strings.NewReplacer(
		"*", "",
		"_", "",
		"~", "",
		"`", "",
		"#", "",
		">", "",
	)
	return r.Replace(s)
}

// TruncateForPreview reduces content to a plain-text, single-line preview of at
// most max runes, appending "…" when truncated. Returns "" for blank input so
// the caller can omit the preview clause entirely.
func TruncateForPreview(content string, max int) string {
	content = strings.TrimSpace(stripMarkdown(content))
	content = strings.Join(strings.Fields(content), " ")
	if content == "" || max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(content) <= max {
		return content
	}
	return string([]rune(content)[:max]) + "…"
}

// statusLabel returns the localized label for a task status, or "" when the
// status is empty (some events omit status entirely).
func (n *Notifier) statusLabel(locale, status string) string {
	if status == "" {
		return ""
	}
	return n.translator.T(locale, "task.status."+status)
}

// renderMessage composes a localized notification body: the core template,
// followed by the optional due clause (when DueAt != nil) and the optional
// comment-preview clause (when preview != "", commented event only). Trailing
// whitespace is trimmed so omitted clauses leave no dangling separator.
// bandEmoji prefixes a DM message with a warning/danger emoji based on the
// task's due band, so a DM-only reader gets the same urgency cue as the card /
// sidebar (which tint via CSS). Muted band → no prefix. Overdue template
// already carries 🔴 in its own wording, so this is skipped when coreKey is the
// overdue key (avoid double emoji).
func (n *Notifier) bandEmoji(coreKey string, task TaskSummary) string {
	if coreKey == "notification.overdue" {
		return ""
	}
	dueMs := int64(0)
	if task.DueAt != nil {
		dueMs = *task.DueAt
	}
	switch dueBandLocal(dueMs, n.nowMs(), task.Status) {
	case "danger":
		return "🔴 "
	case "warning":
		return "⚠ "
	default:
		return ""
	}
}

// dueBandLocal mirrors server/due_band.go's dueBand so this package stays
// self-contained (notification is a separate package; importing main would
// create a cycle). Thresholds MUST stay in sync with the main-package helper
// (change due-color-and-scheduled-notify, design D1).
func dueBandLocal(dueMs, nowMs int64, status string) string {
	if dueMs == 0 {
		return "muted"
	}
	if status == "done" || status == "cancelled" {
		return "muted"
	}
	const (
		msPerHour = int64(60 * 60 * 1000)
		dangerMs  = 24 * msPerHour
		warningMs = 72 * msPerHour
	)
	delta := dueMs - nowMs
	switch {
	case delta < dangerMs:
		return "danger"
	case delta <= warningMs:
		return "warning"
	default:
		return "muted"
	}
}

func (n *Notifier) renderMessage(locale, coreKey string, coreArgs []any, task TaskSummary, preview string) string {
	msg := n.bandEmoji(coreKey, task) + n.translator.T(locale, coreKey, coreArgs...)
	if task.DueAt != nil {
		msg += n.translator.T(locale, "notification.due.suffix", formatDue(locale, *task.DueAt, task.IsAllDay))
	}
	if preview != "" {
		msg += n.translator.T(locale, "notification.preview.suffix", preview)
	}
	return strings.TrimSpace(msg)
}

// NotifyAssigned DMs the newly assigned user. Under the all-channel model the
// assign event fires for every non-empty assignee, INCLUDING self-assign
// (creator assigning to themselves) — the DM serves as an acknowledgment.
// Only an empty assignee is a no-op.
func (n *Notifier) NotifyAssigned(assigneeID, actorID string, task TaskSummary) {
	if assigneeID == "" {
		return
	}
	locale := n.localeFor(assigneeID)
	msg := n.renderMessage(locale, "notification.assigned", []any{
		n.displayName(actorID),
		n.renderTaskNameLink(task.ID, task.Summary),
		shortID(task.ID),
		n.statusLabel(locale, task.Status),
	}, task, "")
	n.postDM(assigneeID, msg)
}

// NotifyCompleted DMs the creator and assignee when a task is marked done. The
// user who performed the action (actorID) is excluded from the recipients so
// they don't get a notification about their own action. Status is intentionally
// not rendered — the verb already conveys the terminal status.
func (n *Notifier) NotifyCompleted(task TaskSummary, actorID, creatorID, assigneeID string) {
	actor := n.displayName(actorID)
	for _, recipient := range uniqueRecipients(actorID, creatorID, assigneeID) {
		locale := n.localeFor(recipient)
		msg := n.renderMessage(locale, "notification.completed", []any{
			n.renderTaskNameLink(task.ID, task.Summary),
			shortID(task.ID),
			actor,
		}, task, "")
		n.postDM(recipient, msg)
	}
}

// NotifyCancelled DMs the creator and assignee when a task is cancelled. Same
// exclusion rule and status omission as NotifyCompleted.
func (n *Notifier) NotifyCancelled(task TaskSummary, actorID, creatorID, assigneeID string) {
	actor := n.displayName(actorID)
	for _, recipient := range uniqueRecipients(actorID, creatorID, assigneeID) {
		locale := n.localeFor(recipient)
		msg := n.renderMessage(locale, "notification.cancelled", []any{
			n.renderTaskNameLink(task.ID, task.Summary),
			shortID(task.ID),
			actor,
		}, task, "")
		n.postDM(recipient, msg)
	}
}

// NotifyCommented DMs the task participants (creator + assignee) when a comment
// is added, excluding the commenter (actorID). When CommentPreview is non-empty
// it is appended as a quoted preview clause after the due clause.
func (n *Notifier) NotifyCommented(task TaskSummary, actorID, creatorID, assigneeID string) {
	actor := n.displayName(actorID)
	for _, recipient := range uniqueRecipients(actorID, creatorID, assigneeID) {
		locale := n.localeFor(recipient)
		msg := n.renderMessage(locale, "notification.commented", []any{
			actor,
			n.renderTaskNameLink(task.ID, task.Summary),
			shortID(task.ID),
			n.statusLabel(locale, task.Status),
		}, task, task.CommentPreview)
		n.postDM(recipient, msg)
	}
}

// NotifyReminder DMs the assignee that a task is due soon. Unlike the
// event notifications (assign/done/comment) this one is invoked by the
// background scheduler, which retries on failure — so it returns an error when
// the DM could not be delivered, letting the caller decide whether to mark the
// reminder fired. An empty assignee is a no-op (no error). There is no actor.
func (n *Notifier) NotifyReminder(assigneeID string, task TaskSummary) error {
	if assigneeID == "" || n.botUserID == "" {
		return nil
	}
	locale := n.localeFor(assigneeID)
	msg := n.renderMessage(locale, "notification.reminder", []any{
		n.renderTaskNameLink(task.ID, task.Summary),
		shortID(task.ID),
		n.statusLabel(locale, task.Status),
	}, task, "")
	channel, err := n.api.GetDirectChannel(assigneeID, n.botUserID)
	if err != nil || channel == nil {
		// GetDirectChannel can return (nil, nil) during RPC shutdown;
		// guard against a nil-pointer on channel.Id below.
		if err != nil {
			return err
		}
		return errors.New("notification: GetDirectChannel returned nil channel")
	}
	post := &model.Post{
		UserId:    n.botUserID,
		ChannelId: channel.Id,
		Message:   msg,
		Type:      model.PostTypeDefault,
	}
	if _, err := n.api.CreatePost(post); err != nil {
		return err
	}
	return nil
}

// NotifyOverdue DMs the creator and assignee that a task is overdue. Unlike
// the event notifications this is scheduler-fired (no human actor), so neither
// recipient is excluded. The message includes the due date and how many days
// the task is overdue. Best-effort: a delivery failure is logged but not
// returned — the daily job retries naturally the next day (design D7).
func (n *Notifier) NotifyOverdue(task TaskSummary, nowMs int64, creatorID, assigneeID string) {
	for _, recipient := range uniqueRecipients("", creatorID, assigneeID) {
		locale := n.localeFor(recipient)
		dueStr := ""
		if task.DueAt != nil {
			dueStr = formatDue(locale, *task.DueAt, task.IsAllDay)
		}
		msg := strings.TrimSpace(n.translator.T(locale, "notification.overdue",
			n.renderTaskNameLink(task.ID, task.Summary),
			shortID(task.ID),
			n.statusLabel(locale, task.Status),
			dueStr,
			n.formatOverdueDuration(locale, nowMs, derefInt64(task.DueAt, nowMs)),
		))
		n.postDM(recipient, msg)
	}
}

// NotifyDueSoon DMs the assignee that a task is due within the next 24h.
// Scheduler-fired (no human actor), so only the assignee is notified (not the
// creator — the assignee is the responsible party). Best-effort like overdue:
// a delivery failure is logged but not returned; the next-day run retries
// naturally (change due-color-and-scheduled-notify, design D6).
func (n *Notifier) NotifyDueSoon(assigneeID string, task TaskSummary) {
	if assigneeID == "" {
		return
	}
	locale := n.localeFor(assigneeID)
	dueStr := ""
	if task.DueAt != nil {
		dueStr = formatDue(locale, *task.DueAt, task.IsAllDay)
	}
	// bandEmoji prefixes 🔴 (due-soon is <24h = danger band). The template itself
	// carries no emoji so there is no duplication.
	msg := strings.TrimSpace(n.bandEmoji("notification.due_soon", task) +
		n.translator.T(locale, "notification.due_soon",
			n.renderTaskNameLink(task.ID, task.Summary),
			shortID(task.ID),
			n.statusLabel(locale, task.Status),
			dueStr,
		))
	n.postDM(assigneeID, msg)
}

// derefInt64 returns *v when non-nil, else fallback.
func derefInt64(v *int64, fallback int64) int64 {
	if v != nil {
		return *v
	}
	return fallback
}

// displayName returns a human-friendly name for userID (display name, no @
// prefix). Used so notifications read "by Alice" rather than "@alice" — DMs are
// 1-1 so mentioning the actor is noise.
func (n *Notifier) displayName(userID string) string {
	if n.api == nil || userID == "" {
		return userID
	}
	user, err := n.api.GetUser(userID)
	if err != nil || user == nil {
		return userID
	}
	return user.GetDisplayName(model.ShowNicknameFullName)
}

// uniqueRecipients returns the distinct, non-empty user ids from the given set,
// excluding excludeID. The actor is excluded so a user never gets a DM about
// their own action.
func uniqueRecipients(excludeID string, ids ...string) []string {
	seen := make(map[string]struct{}, len(ids))
	var out []string
	for _, id := range ids {
		if id == "" || id == excludeID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
