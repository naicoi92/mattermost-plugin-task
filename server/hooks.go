package main

import (
	"context"
	"errors"
	"strings"
	"time"

	mmmodel "github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	taskmodel "github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// MessageHasBeenPosted is the inbound hook that implements comment-as-thread
// (M4-5, design §3.5): when a user replies in a task card's thread, the plugin
// automatically records the (task_id, post_id) mapping in task_comments. The
// comment content lives in the Mattermost post; native notifications,
// reactions, and @mentions work for free.
//
// Logic:
//  1. Skip non-replies (RootId == "") — only thread replies are comments.
//  2. Skip the bot's own posts — avoids a loop when the bot updates a card.
//  3. Look up the root post in task_posts; if it's not a tracked card, skip.
//  4. LinkComment + TouchTaskUpdatedAt + append a commented audit event, all
//     via the service's LinkComment (which wraps them in WithTx).
//  5. Failures are logged, not returned — the post still exists in Mattermost.
//
// The hook returns fast: a short bounded context + best-effort handling so a
// slow DB never blocks the channel's message pipeline.
func (p *Plugin) MessageHasBeenPosted(_ *plugin.Context, post *mmmodel.Post) {
	// Guard 1: only thread replies (RootId set) are candidates for comments.
	if post == nil || post.RootId == "" {
		return
	}
	// Guard 2: skip the bot's own posts (card updates, reminder DMs) to avoid
	// an infinite loop where linking a comment triggers another post.
	if post.UserId == p.botUserID {
		return
	}
	if p.taskStore == nil || p.taskService == nil {
		return
	}

	// Bounded context so a wedged DB can't hang the message pipeline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Reverse-lookup: is the root post a tracked task card?
	taskID, err := p.taskStore.GetTaskIDByChannelPost(ctx, post.RootId)
	if err != nil {
		if errors.Is(err, store.ErrPostNotFound) {
			// Root post is not a task card — not a task comment.
			return
		}
		p.API.LogError("MessageHasBeenPosted: failed to look up task for post",
			"root_id", post.RootId, "error", err)
		return
	}

	// Link the reply as a comment on the task. LinkComment wraps the mapping
	// insert, UpdatedAt touch, and commented audit event in one transaction.
	if _, _, err := p.taskService.LinkComment(taskID, post.Id, post.UserId); err != nil {
		// The post exists in Mattermost regardless; only the mapping failed.
		// Log so the gap is visible, but don't block the hook.
		p.API.LogError("MessageHasBeenPosted: failed to link task comment",
			"task_id", taskID, "post_id", post.Id, "error", err)
		return
	}

	// Real-time: a comment arrived — let the task detail / card refresh.
	// Best-effort; a missing task is skipped.
	if t, gErr := p.taskService.Get(taskID); gErr == nil && t != nil {
		p.broadcastTaskUpdated(t, []string{"comment"})
	}
}

// UserHasBeenDeactivated migrates DM-scoped tasks away from a deactivated
// user. When a user is deactivated, Mattermost soft-deletes their DM channels,
// which would orphan any task whose home channel is one of those DMs. For each
// such task the plugin relocates it to the self-DM of the OTHER participant
// (the still-active creator or assignee), preserving the card and comments.
//
// Tasks whose other participant is also deactivated (or missing) are left in
// place and logged as orphans for admin follow-up. The hook is best-effort:
// a failure to migrate one task is logged and skipped so it cannot block
// deactivation.
func (p *Plugin) UserHasBeenDeactivated(_ *plugin.Context, user *mmmodel.User) {
	if user == nil || p.taskStore == nil || p.taskService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find every task on which the deactivated user is a member (creator or
	// assignee) and try to migrate each DM-scoped one away from them.
	rows, err := p.taskStore.ListTasksByMember(ctx, user.Id, 0)
	if err != nil {
		p.API.LogError("UserHasBeenDeactivated: list tasks by member failed", "user_id", user.Id, "error", err)
		return
	}
	for _, row := range rows {
		t, gErr := p.taskService.Get(row.ID)
		if gErr != nil || t == nil {
			continue
		}
		p.maybeMigrateDeactivatedDMTask(ctx, t, user.Id)
	}
}

// maybeMigrateDeactivatedDMTask moves a single task out of a DM that involves
// deactivatedUserID. It is a no-op when the task is not DM-scoped or the
// deactivated user is not a participant. When the OTHER participant is also
// inactive, the task is logged as an orphan and left in place.
func (p *Plugin) maybeMigrateDeactivatedDMTask(ctx context.Context, t *taskmodel.Task, deactivatedUserID string) {
	if t.ChannelID == "" {
		return
	}
	ch, err := p.API.GetChannel(t.ChannelID)
	if err != nil || ch == nil || ch.Type != mmmodel.ChannelTypeDirect {
		return // not a DM-scoped task
	}
	// The deactivated user must be one of the two DM participants.
	other := otherDMParticipant(ch, deactivatedUserID)
	if other == "" {
		return // not this user's DM
	}
	// If the other participant is also deactivated, leave the task as an
	// orphan (nothing useful to migrate to) and log for admin follow-up.
	otherUser, gErr := p.API.GetUser(other)
	if gErr != nil || otherUser == nil || otherUser.DeleteAt != 0 {
		p.API.LogWarn("UserHasBeenDeactivated: orphan DM task (both participants inactive)",
			"task_id", t.ID, "channel_id", t.ChannelID)
		return
	}
	// Relocate to the active participant's self-DM and reuse the reassign
	// move-channel machinery (card repost + comment copy).
	selfDM, dErr := p.API.GetDirectChannel(other, other)
	if dErr != nil || selfDM == nil {
		p.API.LogError("UserHasBeenDeactivated: open self-DM failed",
			"task_id", t.ID, "user_id", other, "error", dErr)
		return
	}
	if selfDM.Id == t.ChannelID {
		return
	}
	if _, mErr := p.moveTaskChannelIfDM(t, t.CreatorID); mErr != nil {
		// moveTaskChannelIfDM resolves GetDirectChannel(creator, assignee); for
		// a task whose assignee was the deactivated user, that would re-open
		// the same DM, so fall back to an explicit move to the self-DM.
		if _, fErr := p.forceMoveTaskToChannel(t, selfDM.Id); fErr != nil {
			p.API.LogError("UserHasBeenDeactivated: migrate task failed",
				"task_id", t.ID, "target_channel_id", selfDM.Id, "error", fErr)
		}
	}
}

// forceMoveTaskToChannel relocates a task to targetChannelID unconditionally
// (used by the deactivation hook, where the normal assignee-based DM
// resolution does not apply). It posts a fresh card, copies comments, updates
// the task, and deletes the old card — the same steps as moveTaskChannelIfDM
// but without the GetDirectChannel(creator, assignee) resolution.
func (p *Plugin) forceMoveTaskToChannel(t *taskmodel.Task, targetChannelID string) (*taskmodel.Task, error) {
	newPostID := p.postCard(targetChannelID, t)
	if newPostID == "" {
		return nil, errors.New("force move: post card failed")
	}
	p.copyCommentsUnderCard(t.ID, newPostID, targetChannelID)
	updated, err := p.taskService.UpdateChannel(t.ID, targetChannelID, newPostID)
	if err != nil {
		return nil, err
	}
	if t.ChannelPostID != nil && *t.ChannelPostID != "" {
		if dErr := p.API.DeletePost(*t.ChannelPostID); dErr != nil {
			p.API.LogWarn("force move: delete old card failed",
				"task_id", t.ID, "old_post_id", *t.ChannelPostID, "error", dErr)
		}
	}
	return updated, nil
}

// otherDMParticipant returns the DM channel member that is NOT userID, or ""
// when userID is not a participant (or the channel name is not a 2-user DM).
func otherDMParticipant(ch *mmmodel.Channel, userID string) string {
	if ch == nil || ch.Type != mmmodel.ChannelTypeDirect {
		return ""
	}
	parts := strings.Split(ch.Name, "__")
	if len(parts) != 2 {
		return ""
	}
	if parts[0] == parts[1] {
		return "" // self-DM has no other participant
	}
	if parts[0] != userID && parts[1] != userID {
		return "" // userID is not a participant
	}
	if parts[0] == userID {
		return parts[1]
	}
	return parts[0]
}
