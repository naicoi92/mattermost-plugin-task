// Package notification centralizes direct-message and ephemeral notifications
// for task events, sent from the plugin bot and translated via the server i18n
// helper using the recipient's locale (PLAN.md section 7).
//
// All task-event DMs route through this package so the rules are consistent:
//   - the assignee is DM'd when newly assigned (skipped if assignee == creator);
//   - creator + assignee are DM'd when a task is completed or cancelled;
//   - creator + assignee are DM'd when a comment is added;
//   - the assignee is DM'd when a reminder fires;
//   - a user is never DM'd when they are unassigned.
package notification

import (
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
)

// API is the subset of the Mattermost plugin API the notifier needs. Declaring
// it locally keeps the package unit-testable with a fake and decoupled from the
// concrete plugin.API shape.
type API interface {
	// GetUser returns the user with id (used to read Locale for i18n and
	// Username/DisplayName for message rendering).
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

// Notifier sends task-event DMs from the bot, translating text to each
// recipient's locale.
type Notifier struct {
	api        API
	translator Translator
	botUserID  string
}

// New returns a Notifier that posts DMs as botUserID, translating via t.
func New(api API, t Translator, botUserID string) *Notifier {
	return &Notifier{api: api, translator: t, botUserID: botUserID}
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

// dm posts a localized DM to recipientID. A failure to open the DM channel or
// create the post is logged but not returned: notifications are best-effort and
// must never break the originating task operation.
func (n *Notifier) dm(recipientID, key string, args ...any) {
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
	text := n.translator.T(n.localeFor(recipientID), key, args...)
	post := &model.Post{
		UserId:    n.botUserID,
		ChannelId: channel.Id,
		Message:   text,
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
}

// NotifyAssigned DMs the newly assigned user. It is a no-op when the assignee
// is the task creator (no self-DM) or when the assignee is empty.
func (n *Notifier) NotifyAssigned(assigneeID, creatorID string, task TaskSummary) {
	if assigneeID == "" || assigneeID == creatorID {
		return
	}
	actor := n.displayName(creatorID)
	n.dm(assigneeID, "notification.assigned", task.Summary, actor)
}

// NotifyCompleted DMs the creator and assignee when a task is marked done. The
// user who performed the action (actorID) is excluded from the recipients so
// they don't get a notification about their own action.
func (n *Notifier) NotifyCompleted(task TaskSummary, actorID, creatorID, assigneeID string) {
	actor := n.displayName(actorID)
	for _, recipient := range uniqueRecipients(actorID, creatorID, assigneeID) {
		n.dm(recipient, "notification.completed", task.Summary, actor)
	}
}

// NotifyCancelled DMs the creator and assignee when a task is cancelled. Same
// exclusion rule as NotifyCompleted (the actor isn't notified).
func (n *Notifier) NotifyCancelled(task TaskSummary, actorID, creatorID, assigneeID string) {
	actor := n.displayName(actorID)
	for _, recipient := range uniqueRecipients(actorID, creatorID, assigneeID) {
		n.dm(recipient, "notification.cancelled", task.Summary, actor)
	}
}

// NotifyCommented DMs the task participants (creator + assignee) when a comment
// is added. The commenter (actorID) is excluded.
func (n *Notifier) NotifyCommented(task TaskSummary, actorID, creatorID, assigneeID string) {
	actor := n.displayName(actorID)
	for _, recipient := range uniqueRecipients(actorID, creatorID, assigneeID) {
		n.dm(recipient, "notification.commented", actor, task.Summary)
	}
}

// NotifyReminder DMs the assignee that a task is due soon. Unlike the
// event notifications (assign/done/comment) this one is invoked by the
// background scheduler, which retries on failure — so it returns an error when
// the DM could not be delivered, letting the caller decide whether to mark the
// reminder fired. An empty assignee is a no-op (no error).
func (n *Notifier) NotifyReminder(assigneeID string, task TaskSummary) error {
	if assigneeID == "" || n.botUserID == "" {
		return nil
	}
	channel, err := n.api.GetDirectChannel(assigneeID, n.botUserID)
	if err != nil || channel == nil {
		// GetDirectChannel can return (nil, nil) during RPC shutdown;
		// guard against a nil-pointer on channel.Id below.
		if err != nil {
			return err
		}
		return errors.New("notification: GetDirectChannel returned nil channel")
	}
	text := n.translator.T(n.localeFor(assigneeID), "notification.reminder", task.Summary)
	post := &model.Post{
		UserId:    n.botUserID,
		ChannelId: channel.Id,
		Message:   text,
		Type:      model.PostTypeDefault,
	}
	if _, err := n.api.CreatePost(post); err != nil {
		return err
	}
	return nil
}

// displayName returns a human-friendly name for userID (username fallback to
// the id). Used so notifications read "by @alice" rather than a raw id.
func (n *Notifier) displayName(userID string) string {
	if n.api == nil || userID == "" {
		return userID
	}
	user, err := n.api.GetUser(userID)
	if err != nil || user == nil {
		return userID
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	return userID
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
