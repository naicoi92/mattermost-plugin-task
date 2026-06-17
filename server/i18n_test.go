package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewI18n(t *testing.T) {
	assert := assert.New(t)

	i18n, err := NewI18n()
	assert.NoError(err)
	assert.NotNil(i18n)
	assert.NotEmpty(i18n.messages)
	assert.Contains(i18n.messages, "en")
	assert.Contains(i18n.messages, "vi")
}

func TestI18nT(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	i18n, err := NewI18n()
	require.NoError(err)

	tests := []struct {
		name     string
		locale   string
		key      string
		args     []any
		expected string
	}{
		{
			name:     "english task created",
			locale:   "en",
			key:      "task.created",
			args:     []any{"Task A"},
			expected: "Task created: Task A",
		},
		{
			name:     "vietnamese task created",
			locale:   "vi",
			key:      "task.created",
			args:     []any{"Task A"},
			expected: "Đã tạo task: Task A",
		},
		{
			name:     "fallback to english for missing locale",
			locale:   "fr",
			key:      "task.list.empty",
			expected: "No tasks found.",
		},
		{
			name:     "fallback to english for missing key",
			locale:   "vi",
			key:      "nonexistent.key",
			expected: "nonexistent.key",
		},
		{
			name:     "returns english key fallback when key missing everywhere",
			locale:   "en",
			key:      "missing.key",
			expected: "missing.key",
		},
		{
			name:     "no interpolation args",
			locale:   "en",
			key:      "task.list.empty",
			expected: "No tasks found.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := i18n.T(tc.locale, tc.key, tc.args...)
			assert.Equal(tc.expected, result)
		})
	}
}

func TestI18nT_NilReceiver(t *testing.T) {
	assert := assert.New(t)

	var i18n *I18n
	result := i18n.T("en", "task.created", "Task A")
	assert.Equal("task.created", result)
}
