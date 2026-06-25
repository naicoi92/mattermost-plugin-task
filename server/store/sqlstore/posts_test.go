package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// Under the all-channel model the posts table has been collapsed into an
// inline channel_post_id column on task_tasks. These tests cover the two
// helpers that remain: SetChannelPostID (write the card pointer) and
// GetTaskIDByChannelPost (reverse lookup used by the comment hook).

func TestSetChannelPostID_SetsValue(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	require.NoError(t, s.SetChannelPostID(ctx, "T1", "post-1"))

	// Verify via the reverse lookup rather than a raw query (db is private).
	got, err := s.GetTaskIDByChannelPost(ctx, "post-1")
	require.NoError(t, err)
	assert.Equal(t, "T1", got)
}

func TestSetChannelPostID_RequiresTaskID(t *testing.T) {
	s := tasksTestStore(t)
	err := s.SetChannelPostID(context.Background(), "", "post-1")
	require.Error(t, err)
}

func TestSetChannelPostID_UnknownTask(t *testing.T) {
	s := tasksTestStore(t)
	err := s.SetChannelPostID(context.Background(), "ghost", "post-1")
	require.ErrorIs(t, err, store.ErrTaskNotFound)
}

func TestSetChannelPostID_ClearsValue(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.SetChannelPostID(ctx, "T1", "post-1"))
	// Empty postID clears the pointer (sets it back to NULL).
	require.NoError(t, s.SetChannelPostID(ctx, "T1", ""))

	_, err := s.GetTaskIDByChannelPost(ctx, "post-1")
	require.ErrorIs(t, err, store.ErrPostNotFound)
}

func TestGetTaskIDByChannelPost(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.SetChannelPostID(ctx, "T1", "card-post"))

	t.Run("returns owning task", func(t *testing.T) {
		id, err := s.GetTaskIDByChannelPost(ctx, "card-post")
		require.NoError(t, err)
		assert.Equal(t, "T1", id)
	})
	t.Run("unknown post yields ErrPostNotFound", func(t *testing.T) {
		_, err := s.GetTaskIDByChannelPost(ctx, "no-such-post")
		require.ErrorIs(t, err, store.ErrPostNotFound)
	})
	t.Run("empty post id rejected", func(t *testing.T) {
		_, err := s.GetTaskIDByChannelPost(ctx, "")
		require.Error(t, err)
	})
}

// FKCascadeOnTaskDelete: dropping the task must drop its channel_post_id
// pointer along with it (it's a column, so this is trivially true, but pin
// the invariant).
func TestChannelPostID_TaskDeleted(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.SetChannelPostID(ctx, "T1", "p1"))

	require.NoError(t, s.DeleteTask(ctx, "T1"))
	_, err := s.GetTaskIDByChannelPost(ctx, "p1")
	require.ErrorIs(t, err, store.ErrPostNotFound)
}
