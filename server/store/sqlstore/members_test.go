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

func TestAddMember_AllowsMultipleUsersPerRoleAtSchemaLevel(t *testing.T) {
	// The composite PK (task_id, user_id, role) — not (task_id, role) — is a
	// deliberate future-proofing choice (see SQL_MIGRATION_PLAN.md §3.2): the
	// schema must allow several users in the same role so multi-assignee /
	// follower can be enabled without a migration. This test locks that
	// invariant at the storage layer; the MVP still enforces one creator +
	// one assignee per task at the application/service layer.
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleAssignee))
	// Same role, different user — must succeed at the store layer.
	require.NoError(t, s.AddMember(ctx, "T1", "u2", model.MemberRoleAssignee))

	members, err := s.ListMembers(ctx, "T1")
	require.NoError(t, err)
	assert.Len(t, members, 2)
}

func TestGetMemberByRole_RejectsInvalidRole(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	// An invalid role must error here rather than be masked as not-found.
	_, err := s.GetMemberByRole(ctx, "T1", "assginee")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
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
	t.Run("missing role yields store.ErrMemberNotFound", func(t *testing.T) {
		_, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleFollower)
		require.ErrorIs(t, err, store.ErrMemberNotFound)
	})
	t.Run("missing task yields store.ErrMemberNotFound", func(t *testing.T) {
		_, err := s.GetMemberByRole(ctx, "ghost", model.MemberRoleAssignee)
		require.ErrorIs(t, err, store.ErrMemberNotFound)
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
	require.ErrorIs(t, err, store.ErrMemberNotFound)
	assert.Equal(t, "", id)

	// Removing again is a no-op (idempotent), not an error.
	require.NoError(t, s.RemoveMember(ctx, "T1", "u1", model.MemberRoleCreator))
}

func TestSetAssignee_UpdateInPlace(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleAssignee))

	require.NoError(t, s.SetAssignee(ctx, "T1", "u2"))
	got, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u2", got)
}

func TestSetAssignee_NoExistingAssigneeInsertsNew(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	// No prior assignee edge.
	require.NoError(t, s.SetAssignee(ctx, "T1", "u1"))
	got, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleAssignee)
	require.NoError(t, err)
	assert.Equal(t, "u1", got)
}

// TestListTasks_ScopeDirectRequiresMutualMembership verifies the DM scope
// returns only tasks where BOTH the caller and the partner are members (with
// the assignee or creator role). Tasks where only one of them is a member are
// hidden — this is the security boundary that prevents a caller from
// enumerating a third user's tasks by guessing partner_id.
func TestListTasks_ScopeDirectRequiresMutualMembership(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	// u1 + u2 are the DM pair.
	// T1: both u1 (assignee) + u2 (creator)  → returned.
	// T2: only u1 (assignee)                  → hidden (u2 not a member).
	// T3: only u2 (creator)                   → hidden (u1 not a member).
	// T4: both u1 + u3 (third party)          → hidden (u2 not a member).
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))
	mustCreate(t, s, ctx, fixture("T3", "k3"))
	mustCreate(t, s, ctx, fixture("T4", "k4"))
	require.NoError(t, s.AddMember(ctx, "T1", "u1", model.MemberRoleAssignee))
	require.NoError(t, s.AddMember(ctx, "T1", "u2", model.MemberRoleCreator))
	require.NoError(t, s.AddMember(ctx, "T2", "u1", model.MemberRoleAssignee))
	require.NoError(t, s.AddMember(ctx, "T3", "u2", model.MemberRoleCreator))
	require.NoError(t, s.AddMember(ctx, "T4", "u1", model.MemberRoleAssignee))
	require.NoError(t, s.AddMember(ctx, "T4", "u3", model.MemberRoleCreator))

	page, err := s.ListTasks(ctx, store.ListQuery{Scope: store.ScopeDirect, UserID: "u1", PartnerID: "u2", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, page.Total, "only T1 has both u1 and u2 as members")
	require.Len(t, page.Items, 1)
	assert.Equal(t, "T1", page.Items[0].(*model.TaskRow).ID)
}

// TestListTasks_ScopeDirectPartnerGuessingFails verifies the security
// invariant: a caller cannot enumerate a third user's tasks by passing their
// id as partner_id. Here u1 tries to peek at u3's tasks by claiming
// partner_id=u3, but u1 is not a member of any task u3 relates to, so nothing
// is returned.
func TestListTasks_ScopeDirectPartnerGuessingFails(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	// u3 created T1 alone; u1 is not a member.
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	require.NoError(t, s.AddMember(ctx, "T1", "u3", model.MemberRoleCreator))

	page, err := s.ListTasks(ctx, store.ListQuery{Scope: store.ScopeDirect, UserID: "u1", PartnerID: "u3", Limit: 10})
	require.NoError(t, err)
	require.Empty(t, page.Items, "no task rows should leak to an unrelated caller")
	assert.Equal(t, 0, page.Total, "u1 cannot see u3's task by claiming partner_id=u3")
}

func TestListTasks_ScopeDirectRequiresUserAndPartner(t *testing.T) {
	s := tasksTestStore(t)
	_, err := s.ListTasks(context.Background(), store.ListQuery{Scope: store.ScopeDirect, UserID: "u1", Limit: 10})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope=direct requires UserID and PartnerID")

	_, err = s.ListTasks(context.Background(), store.ListQuery{Scope: store.ScopeDirect, PartnerID: "u2", Limit: 10})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scope=direct requires UserID and PartnerID")
}

func TestIsValidMemberRole(t *testing.T) {
	assert.True(t, model.IsValidMemberRole(model.MemberRoleCreator))
	assert.True(t, model.IsValidMemberRole(model.MemberRoleAssignee))
	assert.True(t, model.IsValidMemberRole(model.MemberRoleFollower))
	assert.False(t, model.IsValidMemberRole("supervisor"))
	assert.False(t, model.IsValidMemberRole(""))
}
