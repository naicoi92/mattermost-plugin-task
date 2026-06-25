package main

import (
	"context"
	"errors"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

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
func (p *Plugin) MessageHasBeenPosted(_ *plugin.Context, post *model.Post) {
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
