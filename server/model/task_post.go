package model

// TaskPost kinds distinguish what role a tracked card post plays. Under the
// all-channel model only Channel (the card in the task's home channel) and
// Share (a card shared into another channel) are produced; the DM kind is
// removed because the home channel IS the DM when the task lives in one.
const (
	PostKindChannel = "channel"
	PostKindShare   = "share"
)

// IsValidPostKind reports whether kind is one of the recognized PostKind*
// constants. The store rejects anything else so the kind namespace stays
// controlled and a typo can't create an unknown kind the card-update logic
// can't handle.
func IsValidPostKind(kind string) bool {
	switch kind {
	case PostKindChannel, PostKindShare:
		return true
	default:
		return false
	}
}

// TaskPost is one row of the task_posts table: a tracked card post for a task.
// A task may have several (one in its home channel plus any number of share
// cards), each tagged with a kind so the card-update logic knows where to push
// refreshes.
type TaskPost struct {
	// ID is the internal ULID of the tracking row (caller-assigned).
	ID string `json:"id" db:"id"`
	// TaskID is the task whose card this post renders.
	TaskID string `json:"task_id" db:"task_id"`
	// PostID is the Mattermost post id of the card. UNIQUE across the table.
	PostID string `json:"post_id" db:"post_id"`
	// Kind is one of the PostKind* constants (channel/share).
	Kind string `json:"kind" db:"kind"`
	// CreatedAt is the ms-UTC timestamp the tracking row was added.
	CreatedAt int64 `json:"created_at" db:"created_at"`
}
