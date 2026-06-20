package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// mustLink links a comment and fails the test on error.
func mustLink(t *testing.T, s *SQLStore, ctx context.Context, id, taskID, postID, authorID string, createdAt int64) model.TaskComment {
	t.Helper()
	c, err := s.LinkComment(ctx, id, taskID, postID, authorID, createdAt)
	require.NoError(t, err)
	return c
}

func TestLinkComment_InsertsMapping(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	c := mustLink(t, s, ctx, "C1", "T1", "post1", "u1", 1_700_000_000_000)
	assert.Equal(t, "C1", c.ID)
	assert.Equal(t, "post1", c.PostID)
	assert.Equal(t, "u1", c.AuthorID)
}

func TestLinkComment_RequiresAllFields(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	_, err := s.LinkComment(ctx, "", "T1", "post1", "u1", 1)
	require.Error(t, err)
	_, err = s.LinkComment(ctx, "C1", "", "post1", "u1", 1)
	require.Error(t, err)
	_, err = s.LinkComment(ctx, "C1", "T1", "", "u1", 1)
	require.Error(t, err)
	_, err = s.LinkComment(ctx, "C1", "T1", "post1", "", 1)
	require.Error(t, err)
}

func TestLinkComment_PostIDUnique(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))

	mustLink(t, s, ctx, "C1", "T1", "sharedpost", "u1", 1)
	// Same post_id on a different task must fail (UNIQUE constraint).
	_, err := s.LinkComment(ctx, "C2", "T2", "sharedpost", "u2", 2)
	require.Error(t, err)
}

func TestListComments_ChronologicalOrder(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	// Insert out of order to prove ORDER BY created_at.
	mustLink(t, s, ctx, "C2", "T1", "post2", "u1", 2_000)
	mustLink(t, s, ctx, "C1", "T1", "post1", "u1", 1_000)
	mustLink(t, s, ctx, "C3", "T1", "post3", "u2", 3_000)

	got, err := s.ListComments(ctx, "T1")
	require.NoError(t, err)
	require.Len(t, got, 3)
	// Chronological by created_at snapshot.
	assert.Equal(t, []string{"C1", "C2", "C3"}, []string{got[0].ID, got[1].ID, got[2].ID})
}

func TestListComments_TaskIsolated(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))
	mustLink(t, s, ctx, "C1", "T1", "p1", "u1", 1)
	mustLink(t, s, ctx, "C2", "T1", "p2", "u1", 2)
	mustLink(t, s, ctx, "C3", "T2", "p3", "u2", 3)

	got, err := s.ListComments(ctx, "T1")
	require.NoError(t, err)
	assert.Len(t, got, 2)
	for _, c := range got {
		assert.Equal(t, "T1", c.TaskID)
	}
}

func TestListComments_EmptyForTaskWithNone(t *testing.T) {
	s := tasksTestStore(t)
	got, err := s.ListComments(context.Background(), "T1")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestCountComments(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustLink(t, s, ctx, "C1", "T1", "p1", "u1", 1)
	mustLink(t, s, ctx, "C2", "T1", "p2", "u1", 2)
	mustLink(t, s, ctx, "C3", "T1", "p3", "u2", 3)

	count, err := s.CountComments(ctx, "T1")
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	t.Run("zero for task with none", func(t *testing.T) {
		count, err := s.CountComments(ctx, "T-empty")
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func TestUnlinkComment(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustLink(t, s, ctx, "C1", "T1", "p1", "u1", 1)

	require.NoError(t, s.UnlinkComment(ctx, "p1"))
	// Mapping gone.
	got, err := s.ListComments(ctx, "T1")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestUnlinkComment_NotFound(t *testing.T) {
	s := tasksTestStore(t)
	err := s.UnlinkComment(context.Background(), "ghostpost")
	require.ErrorIs(t, err, ErrCommentNotFound)
}

func TestLinkComment_FKCascadeOnTaskDelete(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustLink(t, s, ctx, "C1", "T1", "p1", "u1", 1)
	mustLink(t, s, ctx, "C2", "T1", "p2", "u2", 2)

	require.NoError(t, s.DeleteTask(ctx, "T1"))
	got, err := s.ListComments(ctx, "T1")
	require.NoError(t, err)
	assert.Empty(t, got, "FK ON DELETE CASCADE must remove comments with the task")
}

// Defensive-render contract: the caller (service/render layer) must skip rows
// whose GetPost returns nil. We can't exercise GetPost here (no plugin API),
// but we verify the mapping carries the post_id the caller needs to look up.
func TestListComments_CarriesPostIDForDefensiveRender(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustLink(t, s, ctx, "C1", "T1", "post-xyz", "u1", 1)
	got, err := s.ListComments(ctx, "T1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "post-xyz", got[0].PostID, "PostID must be present for GetPost defensive lookup")
	// The internal id round-trips from the value the caller passed to
	// LinkComment; this asserts identity, not ULID shape (the service layer
	// owns id allocation).
	assert.Equal(t, "C1", got[0].ID)
}
