package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"github.com/naicoi92/mattermost-plugin-task/server/store"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// postsTableShort is the short (unprefixed) name of the task_posts table.
const postsTableShort = "posts"

// postColumns lists every column of task_posts in scan order.
var postColumns = []string{"id", "task_id", "post_id", "kind", "created_at"}

// tracking row exists.

// AddPost records that a post renders a task's card. kind must be one of the
// PostKind* constants; the store rejects anything else so the kind namespace
// stays controlled. id is the caller-assigned ULID for the tracking row.
//
// post_id is UNIQUE, so adding the same post twice (e.g. a retry after a
// transient error) is an error the caller should treat as already-tracked.
func (s *SQLStore) AddPost(ctx context.Context, id, taskID, postID, kind string) error {
	if id == "" || taskID == "" || postID == "" {
		return errors.New("add post: id, task id and post id are required")
	}
	if !model.IsValidPostKind(kind) {
		return fmt.Errorf("add post: invalid kind %q", kind)
	}
	_, err := s.builder().
		Insert(s.tableName(postsTableShort)).
		Columns(postColumns...).
		Values(id, taskID, postID, kind, timeNowMilli()).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("add post %s: %w", postID, err)
	}
	return nil
}

// ListPosts returns every tracked card post for a task. The card-update logic
// uses this to refresh every location when a task changes (no more hardcoded
// channel_post_id/dm_post_id pair).
func (s *SQLStore) ListPosts(ctx context.Context, taskID string) ([]model.TaskPost, error) {
	if taskID == "" {
		return nil, errors.New("list posts: task id is required")
	}
	rows, err := s.builder().
		Select(postColumns...).
		From(s.tableName(postsTableShort)).
		Where(sq.Eq{"task_id": taskID}).
		OrderByClause(s.escapeField("created_at") + " ASC").
		QueryContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list posts %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()

	var posts []model.TaskPost
	for rows.Next() {
		var p model.TaskPost
		if err := rows.Scan(&p.ID, &p.TaskID, &p.PostID, &p.Kind, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("list posts %s: %w", taskID, err)
		}
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list posts %s: %w", taskID, err)
	}
	return posts, nil
}

// GetPostByKind returns the post_id of the single tracked card of `kind` for
// `taskID` (e.g. the channel card or a share card). Returns store.ErrPostNotFound
// when no such row exists. LIMIT 1 keeps it cheap even if a future kind allows
// multiples.
func (s *SQLStore) GetPostByKind(ctx context.Context, taskID, kind string) (string, error) {
	if taskID == "" {
		return "", errors.New("get post by kind: task id is required")
	}
	if kind == "" {
		return "", errors.New("get post by kind: kind is required")
	}
	if !model.IsValidPostKind(kind) {
		return "", fmt.Errorf("get post by kind: invalid kind %q", kind)
	}
	var postID string
	err := s.builder().
		Select("post_id").
		From(s.tableName(postsTableShort)).
		Where(sq.Eq{"task_id": taskID, "kind": kind}).
		Limit(1).
		QueryRowContext(ctx).
		Scan(&postID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", store.ErrPostNotFound
		}
		return "", fmt.Errorf("get post by kind %s/%s: %w", taskID, kind, err)
	}
	return postID, nil
}

// GetTaskIDByPost returns the task_id of the task whose card is the given
// postID, or ErrPostNotFound when postID is not a tracked card. Used by the
// MessageHasBeenPosted hook to decide whether a thread reply belongs to a
// task's card (the reverse lookup of GetPostByKind).
func (s *SQLStore) GetTaskIDByPost(ctx context.Context, postID string) (string, error) {
	if postID == "" {
		return "", errors.New("get task id by post: post id is required")
	}
	var taskID string
	err := s.builder().
		Select("task_id").
		From(s.tableName(postsTableShort)).
		Where(sq.Eq{"post_id": postID}).
		Limit(1).
		QueryRowContext(ctx).
		Scan(&taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", store.ErrPostNotFound
		}
		return "", fmt.Errorf("get task id by post %s: %w", postID, err)
	}
	return taskID, nil
}

// DeletePost removes the tracking row for a single post. Idempotent: returns
// store.ErrPostNotFound when no tracking row exists (the post may already have been
// untracked, or was never a card); callers wanting idempotency ignore that
// error. The Mattermost post itself is not deleted here — that's a separate
// p.API.DeletePost concern at the service layer.
func (s *SQLStore) DeletePost(ctx context.Context, postID string) error {
	if postID == "" {
		return errors.New("delete post: post id is required")
	}
	res, err := s.builder().
		Delete(s.tableName(postsTableShort)).
		Where(sq.Eq{"post_id": postID}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("delete post %s: %w", postID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete post %s: rows affected: %w", postID, err)
	}
	if rows == 0 {
		return store.ErrPostNotFound
	}
	return nil
}
