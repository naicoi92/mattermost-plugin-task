package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

func TestWithTx_CommitsOnSuccess(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()

	// Create a task inside a transaction; on success it must be visible
	// outside the tx (committed).
	err := s.WithTx(ctx, func(tx store.Store) error {
		_, e := tx.CreateTask(ctx, fixture("T1", "k1"))
		return e
	})
	require.NoError(t, err)

	got, err := s.GetTask(ctx, "T1")
	require.NoError(t, err)
	assert.Equal(t, "T1", got.ID, "committed task must be visible outside the tx")
}

func TestWithTx_RollsBackOnError(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	sentinel := errors.New("boom")

	// Create a task then return an error: the whole tx must roll back, so the
	// task must NOT be visible outside the tx.
	err := s.WithTx(ctx, func(tx store.Store) error {
		if _, e := tx.CreateTask(ctx, fixture("T1", "k1")); e != nil {
			return e
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel, "WithTx must surface the fn error")

	_, err = s.GetTask(ctx, "T1")
	require.ErrorIs(t, err, ErrTaskNotFound, "rolled-back task must not be visible")
}

func TestWithTx_MultiTableAtomicCommit(t *testing.T) {
	// The whole point of WithTx: a multi-table operation (task + member +
	// event) commits atomically. This mirrors what service.Create will do.
	s := tasksTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx store.Store) error {
		_, e := tx.CreateTask(ctx, fixture("T1", "k1"))
		if e != nil {
			return e
		}
		if e := tx.AddMember(ctx, "T1", "u1", model.MemberRoleCreator); e != nil {
			return e
		}
		return tx.AppendTaskEvent(ctx, model.TaskEvent{
			ID:        "E1",
			TaskID:    "T1",
			ActorID:   "u1",
			EventType: model.EventCreated,
			CreatedAt: 1,
		})
	})
	require.NoError(t, err)

	// All three writes visible together.
	_, err = s.GetTask(ctx, "T1")
	require.NoError(t, err)
	id, err := s.GetMemberByRole(ctx, "T1", model.MemberRoleCreator)
	require.NoError(t, err)
	assert.Equal(t, "u1", id)
	events, err := s.ListTaskEvents(ctx, "T1", 10)
	require.NoError(t, err)
	assert.Len(t, events, 1)
}

func TestWithTx_MultiTableAtomicRollback(t *testing.T) {
	// The other half of the contract: if any step fails, NONE of the writes
	// persist. Simulate a failure on the 3rd step (AppendTaskEvent).
	s := tasksTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx store.Store) error {
		if _, e := tx.CreateTask(ctx, fixture("T1", "k1")); e != nil {
			return e
		}
		if e := tx.AddMember(ctx, "T1", "u1", model.MemberRoleCreator); e != nil {
			return e
		}
		// Invalid event type -> AppendTaskEvent errors -> whole tx rolls back.
		return tx.AppendTaskEvent(ctx, model.TaskEvent{
			ID:        "E1",
			TaskID:    "T1",
			ActorID:   "u1",
			EventType: "frobulated",
			CreatedAt: 1,
		})
	})
	require.Error(t, err)

	// Nothing persisted: task, member, event all absent.
	_, err = s.GetTask(ctx, "T1")
	require.ErrorIs(t, err, ErrTaskNotFound)
	_, err = s.GetMemberByRole(ctx, "T1", model.MemberRoleCreator)
	require.ErrorIs(t, err, ErrMemberNotFound)
	events, err := s.ListTaskEvents(ctx, "T1", 10)
	require.NoError(t, err)
	assert.Empty(t, events)
}

func TestWithTx_RollsBackOnPanic(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()

	// A panic inside fn must roll the tx back and repanic (so a half-applied
	// tx can't escape to the pool).
	assert.Panics(t, func() {
		_ = s.WithTx(ctx, func(tx store.Store) error {
			_, _ = tx.CreateTask(ctx, fixture("T1", "k1"))
			panic("kaboom")
		})
	})

	// The task written before the panic must NOT have persisted.
	_, err := s.GetTask(ctx, "T1")
	require.ErrorIs(t, err, ErrTaskNotFound, "panic must roll back the tx")
}
