package model

// Comment is a comment attached to a Task. ID is a ULID; because ULIDs are
// time-sortable, the ID also serves as the creation-order sort key when listing
// a task's comments.
type Comment struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}
