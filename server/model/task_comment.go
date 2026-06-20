package model

// TaskComment is one row of the task_comments mapping table. A task's
// discussion lives in a Mattermost thread (rooted at the task's card post);
// this struct records only the link between a thread-reply post and its task,
// plus a snapshot of author/timestamp so audit and sort survive post deletion.
//
// Content is intentionally NOT stored here — it lives in the Mattermost post
// and is fetched on demand via GetPost(PostID). This is the "comment-as-thread"
// hybrid design (SQL_MIGRATION_PLAN.md §3.5).
type TaskComment struct {
	// ID is the internal ULID of the mapping row (not the post id).
	ID string `json:"id" db:"id"`
	// TaskID is the task this comment belongs to.
	TaskID string `json:"task_id" db:"task_id"`
	// PostID is the Mattermost post id of the thread reply. UNIQUE across the
	// table so a reply can't be linked to two tasks.
	PostID string `json:"post_id" db:"post_id"`
	// AuthorID is a snapshot of the post's user id at link time, so audit
	// works even if the post is later deleted.
	AuthorID string `json:"author_id" db:"author_id"`
	// CreatedAt is a snapshot of the post's CreateAt at link time, used for
	// chronological sort.
	CreatedAt int64 `json:"created_at" db:"created_at"`
}
