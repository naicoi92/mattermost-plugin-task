package model

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestIsValidStatus(t *testing.T) {
	for _, s := range []string{StatusTodo, StatusInProgress, StatusDone, StatusCancelled} {
		if !IsValidStatus(s) {
			t.Errorf("IsValidStatus(%q) = false, want true", s)
		}
	}

	for _, s := range []string{"", "TODO", "done ", "cancelled-extra", "paused", "in-progress"} {
		if IsValidStatus(s) {
			t.Errorf("IsValidStatus(%q) = true, want false", s)
		}
	}
}

func TestTaskJSONRoundTrip(t *testing.T) {
	due := int64(1_700_000_000_000)
	original := Task{
		ID:            "01HZX",
		Summary:       "Ship MVP",
		ChannelID:     "ch1",
		CreatorID:     "u1",
		AssigneeID:    "u2",
		Due:           &due,
		IsAllDay:      true,
		Status:        StatusInProgress,
		OrderKey:      "a0",
		CreatedAt:     100,
		UpdatedAt:     200,
		ReminderFired: true,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Required fields and a set pointer must be present in the output.
	assertJSONHas(t, data, "id", "summary", "status", "due", "is_all_day", "reminder_fired")

	var got Task
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, original)
	}
}

func TestTaskNilOptionalFieldsOmitted(t *testing.T) {
	// A task with no due/reminders/completion should omit those nullable fields,
	// and unmarshal them back to nil.
	original := Task{
		ID:      "02",
		Summary: "No due",
		Status:  StatusTodo,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	for _, key := range []string{`"due"`, `"completed_at"`, `"cancelled_at"`, `"reminder_offset"`} {
		if bytes.Contains(data, []byte(key)) {
			t.Errorf("expected %s to be omitted, got %s", key, data)
		}
	}

	var got Task
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Due != nil || got.CompletedAt != nil || got.CancelledAt != nil || got.ReminderOffset != nil {
		t.Errorf("expected nil optional fields, got %+v", got)
	}
}

func TestCommentJSONRoundTrip(t *testing.T) {
	original := Comment{
		ID:        "01HZXCOMMENT",
		UserID:    "u1",
		Content:   "Looks good",
		CreatedAt: 100,
		UpdatedAt: 100,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	assertJSONHas(t, data, "id", "user_id", "content", "created_at", "updated_at")

	var got Comment
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, original)
	}
}

// assertJSONHas fails if any expected key is absent from the marshalled JSON.
func assertJSONHas(t *testing.T, data []byte, keys ...string) {
	t.Helper()
	for _, k := range keys {
		needle := `"` + k + `"`
		if !bytes.Contains(data, []byte(needle)) {
			t.Errorf("expected key %s in json, got %s", needle, data)
		}
	}
}
