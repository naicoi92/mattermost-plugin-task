package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

func TestAddMember_IdempotentAndValidates(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleCreator))
	// Re-adding the exact same edge is a no-op (PK conflict ignored).
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleCreator))

	t.Run("invalid role rejected", func(t *testing.T) {
		err := s.AddMember(ctx, "T1", "u1", "supervisor")
		require.Error(t, err)
	})
	t.Run("empty task id rejected", func(t *testing.T) {
		err := s.AddMember(ctx, "", "u1", model.MemberRoleCreator)
		require.Error(t, err)
	})
}

func TestAddMember_FKCascadeOnTaskDelete(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleCreator))
	require.NoError(t, s.AddMember(ctx, "T1", "u2", model.MemberRoleAssignee))

	require.NoError(t, s.DeleteTask(ctx, "T1"))

	members, err := s.ListMembers(ctx, "T1")
	require.NoError(t, err)
	assert.Empty(t, members, "FK ON DELETE CASCADE must remove members with the task")
}

func TestListMembers_ReturnsAllEdges(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleCreator))
	require.NoError(t, s.AddMember(ctx, "T1", "u2", model.MemberRoleAssignee))

	members, err := s.ListMembers(ctx, "T1")
	require.NoError(t, err)
	assert.Len(t, members, 2)
	roles := []string{members[0].Role, members[1].Role}
	assert.ElementsMatch(t, []string{model.MemberRoleCreator, model.MemberRoleAssignee}, roles)
}

func TestGetMemberByRole(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleCreator))
	require.NoError(t, s.AddMember(ctx, "T1", "u2", model.MemberRoleAssignee))

	t.Run("returns assignee", func(t *testing.T) {
		id, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
		require.NoError(t, err)
		assert.Equal(t, "u2", id)
	})
	t.Run("returns creator", func(t *testing.T) {
		id, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleCreator)
		require.NoError(t, err)
		assert.Equal(t, "u1", id)
	})
	t.Run("missing role yields ErrMemberNotFound", func(t *testing.T) {
		_, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleFollower)
		require.ErrorIs(t, err, ErrMemberNotFound)
	})
	t.Run("missing task yields ErrMemberNotFound", func(t *testing.T) {
		_, err := s.GetMemberByRole(ctx, "ghost", model.MemberRoleAssignee)
		require.ErrorIs(t, err, ErrMemberNotFound)
	})
}

func TestRemoveMember_Idempotent(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleCreator))

	// Remove existing edge.
	require.NoError(t, s.RemoveMember(ctx, "T1", "u1", model.MemberRoleCreator))
	id, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleCreator)
	require.ErrorIs(t, err, ErrMemberNotFound)
	assert.Equal(t, "", id)

	// Removing again is a no-op (idempotent), not an error.
	require.NoError(t, s.RemoveMember(ctx, "T1", "u1", model.MemberRoleCreator))
}

func TestSwapAssignee_UpdateInPlace(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleAssignee))

	require.NoError(t, s.SwapAssignee(ctx, "T1", "u1", "u2"))
	got, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u2", got)
}

func TestSwapAssignee_NoExistingAssigneeInsertsNew(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	// No prior assignee edge.
	require.NoError(t, s.SwapAssignee(ctx, "T1", "", "u1"))
	got, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u1", got)
}

func TestSwapAssignee_SameUserIsNoOp(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleAssignee))

	require.NoError(t, s.SwapAssignee(ctx, "T1", "u1", "u1"))
	got, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u1", got)
}

func TestListTasks_ScopeMineJoinsMembers(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	// u1 is assignee of T1 and T2; u2 is assignee of T3; T4 has no assignee.
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))
	mustCreate(t, s, ctx, fixture("T3", "k3"))
	mustCreate(t, s, ctx, fixture("T4", "k4"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleAssignee))
	require.NoError(t, s.AddMember(ctx, "T2", "u1", model.MemberRoleAssignee))
	require.NoError(t, s.AddMember(ctx, "T2", "u1", model.MemberRoleCreator)) // extra role, same user
	require.NoError(t, s.AddMember(ctx, "T3", "u2", model.MemberRoleAssignee))

	page, err := s.ListTasks(ctx, store.ListQuery{Scope: store.ScopeMine, UserID: "u1", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, page.Total, "u1 should see only T1 and T2")
	assert.False(t, page.HasMore)
	ids := []string{page.Items[0].(*model.TaskRow).ID, page.Items[1].(*model.TaskRow).ID}
	assert.ElementsMatch(t, []string{"T1", "T2"}, ids)
}

func TestListTasks_ScopeMineRequiresUserID(t *testing.T) {
	s := tasksTestStore(t)
	_, err := s.ListTasks(context.Background(), store.ListQuery{Scope: store.ScopeMine, Limit: 10})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope=mine requires UserID")
}

func TestIsValidMemberRole(t *testing.T) {
	assert.True(t, model.IsValidMemberRole(model.MemberRoleCreator))
	assert.True(t, model.IsValidMemberRole(model.MemberRoleAssignee))
	assert.True(t, model.IsValidMemberRole(model.MemberRoleFollower))
	assert.False(t, model.IsValidMemberRole("supervisor"))
	assert.False(t, model.IsValidMemberRole(""))
}
