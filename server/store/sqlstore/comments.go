package sqlstore

import (
	"context"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// commentsTableShort is the short (unprefixed) name of the task_comments table.
const commentsTableShort = "comments"

// commentColumns lists every column of task_comments in scan order.
var commentColumns = []string{"id", "task_id", "post_id", "author_id", "created_at"}

// ErrCommentNotFound is returned by UnlinkComment callers that need to
// distinguish "no such post mapping" from a genuine database error. LinkComment
// and ListComments don't surface it (LinkComment is insert-only, ListComments
// returns an empty slice for a task with no comments).
var ErrCommentNotFound = errors.New("task comment not found")

// LinkComment records the mapping between a task and a Mattermost thread-reply
// post. Called by the MessageHasBeenPosted hook (M4-5) when a reply lands in a
// task's card thread. The post's author and create-at are snapshotted so the
// mapping stays useful for audit/sort even after the post is deleted.
//
// id is the caller-assigned ULID for the mapping row (the service layer owns
// id allocation, same as CreateTask). post_id is UNIQUE, so linking the same
// post twice (e.g. a hook firing twice) is an error the caller should treat as
// already-linked and ignore.
func (s *SQLStore) LinkComment(ctx context.Context, id, taskID, postID, authorID string, createdAt int64) (model.TaskComment, error) {
	if id == "" || taskID == "" || postID == "" || authorID == "" {
		return model.TaskComment{}, errors.New("link comment: id, task id, post id and author id are required")
	}
	c := model.TaskComment{
		ID:        id,
		TaskID:    taskID,
		PostID:    postID,
		AuthorID:  authorID,
		CreatedAt: createdAt,
	}
	_, err := s.builder().
		Insert(s.tableName(commentsTableShort)).
		Columns(commentColumns...).
		Values(c.ID, c.TaskID, c.PostID, c.AuthorID, c.CreatedAt).
		ExecContext(ctx)
	if err != nil {
		return model.TaskComment{}, fmt.Errorf("link comment %s: %w", postID, err)
	}
	return c, nil
}

// ListComments returns the comment mappings of a task in chronological order.
// Ties on created_at are broken by id so the ordering is deterministic when
// two comments share a timestamp (e.g. same-ms imports). The caller fetches
// each post's content via GetPost(PostID) and should skip rows whose post has
// been deleted (defensive self-heal, matching the KV store's behaviour).
func (s *SQLStore) ListComments(ctx context.Context, taskID string) ([]model.TaskComment, error) {
	if taskID == "" {
		return nil, errors.New("list comments: task id is required")
	}
	rows, err := s.builder().
		Select(commentColumns...).
		From(s.tableName(commentsTableShort)).
		Where(sq.Eq{"task_id": taskID}).
		OrderByClause(s.escapeField("created_at") + " ASC, " + s.escapeField("id") + " ASC").
		QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list comments %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var comments []model.TaskComment
	for rows.Next() {
		var c model.TaskComment
		if err := rows.Scan(&c.ID, &c.TaskID, &c.PostID, &c.AuthorID, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("list comments %s: %w", taskID, err)
		}
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list comments %s: %w", taskID, err)
	}
	return comments, nil
}

// CountComments returns the number of comments linked to a task. Uses COUNT(*)
// rather than ListComments + len so the card indicator doesn't load every
// mapping row.
func (s *SQLStore) CountComments(ctx context.Context, taskID string) (int, error) {
	if taskID == "" {
		return 0, errors.New("count comments: task id is required")
	}
	var count int
	err := s.builder().
		Select("COUNT(*)").
		From(s.tableName(commentsTableShort)).
		Where(sq.Eq{"task_id": taskID}).
		QueryRowContext(ctx).
		Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count comments %s: %w", taskID, err)
	}
	return count, nil
}

// UnlinkComment removes the mapping for a single post. Returns
// ErrCommentNotFound when no mapping exists (a post can be deleted before its
// mapping is recorded, or the hook may fire for a non-card reply); callers
// that want idempotent behaviour can ignore ErrCommentNotFound.
func (s *SQLStore) UnlinkComment(ctx context.Context, postID string) error {
	if postID == "" {
		return errors.New("unlink comment: post id is required")
	}
	res, err := s.builder().
		Delete(s.tableName(commentsTableShort)).
		Where(sq.Eq{"post_id": postID}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("unlink comment %s: %w", postID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("unlink comment %s: rows affected: %w", postID, err)
	}
	if rows == 0 {
		return ErrCommentNotFound
	}
	return nil
}

// Compile-time check that the comment model satisfies the scan target shape.
var _ model.TaskComment
