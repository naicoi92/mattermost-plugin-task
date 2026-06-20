package sqlstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// mustAppend appends an event and fails the test on error.
func mustAppend(t *testing.T, s *SQLStore, ctx context.Context, e model.TaskEvent) {
	t.Helper()
	require.NoError(t, s.AppendTaskEvent(ctx, e))
}

// eventFixture builds a TaskEvent with required defaults.
func eventFixture(id, taskID, eventType string, overrides ...func(*model.TaskEvent)) model.TaskEvent {
	e := model.TaskEvent{
		ID:        id,
		TaskID:    taskID,
		ActorID:   "u1",
		EventType: eventType,
		CreatedAt: 1_700_000_000_000,
	}
	for _, o := range overrides {
		o(&e)
	}
	return e
}

func TestAppendTaskEvent_InsertsRow(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	to := `{"status":"done"}`
	mustAppend(t, s, ctx, eventFixture("E1", "T1", model.EventStatusChanged, func(e *model.TaskEvent) {
		e.ToValue = &to
	}))

	got, err := s.ListTaskEvents(ctx, "T1", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "E1", got[0].ID)
	assert.Equal(t, model.EventStatusChanged, got[0].EventType)
	require.NotNil(t, got[0].ToValue)
	assert.Equal(t, to, *got[0].ToValue)
	assert.Nil(t, got[0].FromValue, "from_value should be nil when not set")
}

func TestAppendTaskEvent_ValidatesRequiredFields(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))

	cases := []struct {
		name string
		mut  func(*model.TaskEvent)
	}{
		{"missing id", func(e *model.TaskEvent) { e.ID = "" }},
		{"missing task id", func(e *model.TaskEvent) { e.TaskID = "" }},
		{"missing actor id", func(e *model.TaskEvent) { e.ActorID = "" }},
		{"invalid event type", func(e *model.TaskEvent) { e.EventType = "frobulated" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := eventFixture("EX", "T1", model.EventStatusChanged)
			tc.mut(&e)
			err := s.AppendTaskEvent(ctx, e)
			require.Error(t, err)
		})
	}
}

func TestListTaskEvents_NewestFirstWithTiebreak(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	// Three events: E1 oldest, E3 newest. E2 and E3 share a timestamp to
	// exercise the id tiebreaker.
	mustAppend(t, s, ctx, eventFixture("E1", "T1", model.EventCreated, func(e *model.TaskEvent) {
		e.CreatedAt = 1_000
	}))
	mustAppend(t, s, ctx, eventFixture("E2", "T1", model.EventCommented, func(e *model.TaskEvent) {
		e.CreatedAt = 5_000
	}))
	mustAppend(t, s, ctx, eventFixture("E3", "T1", model.EventStatusChanged, func(e *model.TaskEvent) {
		e.CreatedAt = 5_000 // same ts as E2; id DESC -> E3 before E2
	}))

	got, err := s.ListTaskEvents(ctx, "T1", 10)
	require.NoError(t, err)
	require.Len(t, got, 3)
	// Newest first: E3 (ts=5000, id=E3), E2 (ts=5000, id=E2), E1 (ts=1000).
	assert.Equal(t, []string{"E3", "E2", "E1"}, []string{got[0].ID, got[1].ID, got[2].ID})
}

func TestListTaskEvents_LimitCapsResults(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	ids := []string{"E1", "E2", "E3", "E4", "E5"}
	for i, id := range ids {
		idx := i // capture for the closure
		mustAppend(t, s, ctx, eventFixture(id, "T1", model.EventCommented, func(e *model.TaskEvent) {
			e.CreatedAt = int64(1000 + idx)
		}))
	}

	got, err := s.ListTaskEvents(ctx, "T1", 2)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestListTaskEvents_DefaultLimitWhenZeroOrNegative(t *testing.T) {
	s := tasksTestStore(t)
	// limit<=0 must not error; defaults to a sane page size.
	got, err := s.ListTaskEvents(context.Background(), "T1", 0)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestListTaskEvents_TaskIsolated(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustCreate(t, s, ctx, fixture("T2", "k2"))
	mustAppend(t, s, ctx, eventFixture("E1", "T1", model.EventCreated))
	mustAppend(t, s, ctx, eventFixture("E2", "T1", model.EventCommented))
	mustAppend(t, s, ctx, eventFixture("E3", "T2", model.EventCreated))

	got, err := s.ListTaskEvents(ctx, "T1", 10)
	require.NoError(t, err)
	assert.Len(t, got, 2)
	for _, e := range got {
		assert.Equal(t, "T1", e.TaskID)
	}
}

func TestListTaskEvents_FKCascadeOnTaskDelete(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	mustAppend(t, s, ctx, eventFixture("E1", "T1", model.EventCreated))
	mustAppend(t, s, ctx, eventFixture("E2", "T1", model.EventCommented))

	require.NoError(t, s.DeleteTask(ctx, "T1"))
	got, err := s.ListTaskEvents(ctx, "T1", 10)
	require.NoError(t, err)
	assert.Empty(t, got, "FK ON DELETE CASCADE must remove events with the task")
}

func TestAppendTaskEvent_RoundTripsBothFromAndTo(t *testing.T) {
	s := tasksTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ctx, fixture("T1", "k1"))
	from, to := `"todo"`, `"done"`
	mustAppend(t, s, ctx, eventFixture("E1", "T1", model.EventStatusChanged, func(e *model.TaskEvent) {
		e.FromValue = &from
		e.ToValue = &to
	}))

	got, err := s.ListTaskEvents(ctx, "T1", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NotNil(t, got[0].FromValue)
	require.NotNil(t, got[0].ToValue)
	assert.Equal(t, from, *got[0].FromValue)
	assert.Equal(t, to, *got[0].ToValue)
}

func TestIsValidEventType(t *testing.T) {
	valid := []string{
		model.EventCreated, model.EventStatusChanged, model.EventAssigned,
		model.EventUnassigned, model.EventDueChanged, model.EventSummaryChanged,
		model.EventDescriptionChanged, model.EventReminderSet,
		model.EventReminderCleared, model.EventCommented,
		model.EventSubtaskAdded, model.EventDeleted,
	}
	for _, v := range valid {
		assert.True(t, model.IsValidEventType(v), "expected %q valid", v)
	}
	assert.False(t, model.IsValidEventType("frobulated"))
	assert.False(t, model.IsValidEventType(""))
}
