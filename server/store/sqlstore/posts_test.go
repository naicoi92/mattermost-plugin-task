package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

func TestAddPost_InsertsRow(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	require.NoError(t, s.AddPost(ctx, "P1", "T1", "post1", model.PostKindChannel))
	posts, err := s.ListPosts(ctx, "T1")
	require.NoError(t, err)
	require.Len(t, posts, 1)
	assert.Equal(t, "post1", posts[0].PostID)
	assert.Equal(t, model.PostKindChannel, posts[0].Kind)
}

func TestAddPost_RequiresFieldsAndValidKind(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	cases := []struct {
		name           string
		id, task, post string
		kind           string
	}{
		{"missing id", "", "T1", "p1", model.PostKindChannel},
		{"missing task", "P1", "", "p1", model.PostKindChannel},
		{"missing post", "P1", "T1", "", model.PostKindChannel},
		{"invalid kind", "P1", "T1", "p1", "broadcast"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.AddPost(ctx, tc.id, tc.task, tc.post, tc.kind)
			require.Error(t, err)
		})
	}
}

func TestAddPost_PostIDUnique(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))

	require.NoError(t, s.AddPost(ctx, "P1", "T1", "shared", model.PostKindChannel))
	// Same post_id on another task must fail (UNIQUE).
	err := s.AddPost(ctx, "P2", "T2", "shared", model.PostKindDM)
	require.Error(t, err)
}

func TestAddPost_OneCardPerTaskKind(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	require.NoError(t, s.AddPost(ctx, "P1", "T1", "ch1", model.PostKindChannel))
	// A second channel card for the same task must fail: the
	// UNIQUE(task_id, kind) constraint enforces one card per kind per task.
	err := s.AddPost(ctx, "P2", "T1", "ch2", model.PostKindChannel)
	require.Error(t, err)

	// A different kind on the same task is allowed.
	require.NoError(t, s.AddPost(ctx, "P3", "T1", "dm1", model.PostKindDM))
}

func TestListPosts_ReturnsAllKindsForTask(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddPost(ctx, "P1", "T1", "ch-post", model.PostKindChannel))
	require.NoError(t, s.AddPost(ctx, "P2", "T1", "dm-post", model.PostKindDM))

	posts, err := s.ListPosts(ctx, "T1")
	require.NoError(t, err)
	assert.Len(t, posts, 2)
	kinds := []string{posts[0].Kind, posts[1].Kind}
	assert.ElementsMatch(t, []string{model.PostKindChannel, model.PostKindDM}, kinds)
}

func TestListPosts_TaskIsolated(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))
	require.NoError(t, s.AddPost(ctx, "P1", "T1", "p1", model.PostKindChannel))
	require.NoError(t, s.AddPost(ctx, "P2", "T2", "p2", model.PostKindChannel))

	posts, err := s.ListPosts(ctx, "T1")
	require.NoError(t, err)
	assert.Len(t, posts, 1)
	assert.Equal(t, "T1", posts[0].TaskID)
}

func TestGetPostByKind(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddPost(ctx, "P1", "T1", "ch-post", model.PostKindChannel))
	require.NoError(t, s.AddPost(ctx, "P2", "T1", "dm-post", model.PostKindDM))

	t.Run("returns channel post", func(t *testing.T) {
		id, err := s.GetPostByKind(ctx, "T1", model.PostKindChannel)
		require.NoError(t, err)
		assert.Equal(t, "ch-post", id)
	})
	t.Run("returns dm post", func(t *testing.T) {
		id, err := s.GetPostByKind(ctx, "T1", model.PostKindDM)
		require.NoError(t, err)
		assert.Equal(t, "dm-post", id)
	})
	t.Run("missing kind yields ErrPostNotFound", func(t *testing.T) {
		_, err := s.GetPostByKind(ctx, "T1", "follower")
		require.Error(t, err)
	})
	t.Run("missing task yields ErrPostNotFound", func(t *testing.T) {
		_, err := s.GetPostByKind(ctx, "ghost", model.PostKindChannel)
		require.ErrorIs(t, err, ErrPostNotFound)
	})
	t.Run("invalid kind rejected before query", func(t *testing.T) {
		_, err := s.GetPostByKind(ctx, "T1", "broadcast")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid kind")
	})
}

func TestDeletePost(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddPost(ctx, "P1", "T1", "p1", model.PostKindChannel))

	require.NoError(t, s.DeletePost(ctx, "p1"))
	posts, err := s.ListPosts(ctx, "T1")
	require.NoError(t, err)
	assert.Empty(t, posts)
}

func TestDeletePost_NotFound(t *testing.T) {
	s := tasksTestStore(t)
	err := s.DeletePost(context.Background(), "ghost")
	require.ErrorIs(t, err, ErrPostNotFound)
}

func TestPosts_FKCascadeOnTaskDelete(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddPost(ctx, "P1", "T1", "p1", model.PostKindChannel))
	require.NoError(t, s.AddPost(ctx, "P2", "T1", "p2", model.PostKindDM))

	require.NoError(t, s.DeleteTask(ctx, "T1"))
	posts, err := s.ListPosts(ctx, "T1")
	require.NoError(t, err)
	assert.Empty(t, posts, "FK ON DELETE CASCADE must remove posts with the task")
}

func TestIsValidPostKind(t *testing.T) {
	assert.True(t, model.IsValidPostKind(model.PostKindChannel))
	assert.True(t, model.IsValidPostKind(model.PostKindDM))
	assert.False(t, model.IsValidPostKind("broadcast"))
	assert.False(t, model.IsValidPostKind(""))
}
