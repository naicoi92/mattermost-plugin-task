package dialog

import (
	"testing"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCreateSubmission_HappyPath(t *testing.T) {
	sub, err := ParseCreateSubmission(map[string]any{
		FieldSummary:     "Ship it",
		FieldDescription: "do the thing",
		FieldAssignee:    "u-bob",
		FieldDue:         "1700000000000",
		FieldScope:       ScopeChannel,
	})
	require.NoError(t, err)
	assert.Equal(t, "Ship it", sub.Summary)
	assert.Equal(t, "do the thing", sub.Description)
	require.NotNil(t, sub.AssigneeID)
	assert.Equal(t, "u-bob", *sub.AssigneeID)
	require.NotNil(t, sub.Due)
	assert.Equal(t, int64(1700000000000), *sub.Due)
	assert.False(t, sub.Personal)
}

func TestParseCreateSubmission_SummaryRequired(t *testing.T) {
	_, err := ParseCreateSubmission(map[string]any{FieldSummary: "   "})
	assert.ErrorIs(t, err, ErrSummaryRequired)
}

func TestParseCreateSubmission_PersonalScope(t *testing.T) {
	sub, err := ParseCreateSubmission(map[string]any{
		FieldSummary: "x", FieldScope: ScopePersonal,
	})
	require.NoError(t, err)
	assert.True(t, sub.Personal)
}

func TestParseCreateSubmission_InvalidDue(t *testing.T) {
	_, err := ParseCreateSubmission(map[string]any{
		FieldSummary: "x", FieldDue: "next week",
	})
	assert.ErrorIs(t, err, ErrInvalidDue)
}

func TestParseCreateSubmission_OptionalFieldsBlank(t *testing.T) {
	sub, err := ParseCreateSubmission(map[string]any{FieldSummary: "x"})
	require.NoError(t, err)
	assert.Nil(t, sub.AssigneeID)
	assert.Nil(t, sub.Due)
	assert.False(t, sub.Personal)
}

func TestParseCreateSubmission_DueAsFloatCoerced(t *testing.T) {
	// JSON numbers arrive as float64; the parser coerces them.
	sub, err := ParseCreateSubmission(map[string]any{
		FieldSummary: "x", FieldDue: float64(1700000000000),
	})
	require.NoError(t, err)
	require.NotNil(t, sub.Due)
	assert.Equal(t, int64(1700000000000), *sub.Due)
}

func TestNewTaskDialog_PrefillsSummary(t *testing.T) {
	d := NewTaskDialog("Seed", "ch1")
	assert.Equal(t, CallbackTaskCreate, d.CallbackId)
	assert.Equal(t, "ch1", d.State)
	require.NotEmpty(t, d.Elements)
	assert.Equal(t, FieldSummary, d.Elements[0].Name)
	assert.Equal(t, "Seed", d.Elements[0].Default)
}

func TestNewTaskDialog_HasAssigneeUserPicker(t *testing.T) {
	d := NewTaskDialog("", "")
	var assignee *model.DialogElement
	for i := range d.Elements {
		if d.Elements[i].Name == FieldAssignee {
			assignee = &d.Elements[i]
			break
		}
	}
	require.NotNil(t, assignee)
	assert.Equal(t, "users", assignee.DataSource, "assignee uses a user picker")
}
