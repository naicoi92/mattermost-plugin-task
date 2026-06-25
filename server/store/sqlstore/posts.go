package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"github.com/naicoi92/mattermost-plugin-task/server/store"
)

// Under the all-channel model the legacy task_posts table has been collapsed
// into an inline channel_post_id column on task_tasks (migration 000010).
// These helpers read/write that column and reverse-lookup a task by its card
// post id (used by the MessageHasBeenPosted hook to detect task-thread
// replies).

// SetChannelPostID sets the home-channel card post id for a task. Pass an
// empty postID to clear it. The column is UNIQUE (partial index over non-NULL
// values), so assigning a post id already used by another task is an error the
// caller should treat as a conflict.
func (s *SQLStore) SetChannelPostID(ctx context.Context, taskID, postID string) error {
	if taskID == "" {
		return errors.New("set channel post id: task id is required")
	}
	var value any
	if postID != "" {
		value = postID
	}
	res, err := s.builder().
		Update(s.tableName(taskTableShort)).
		Set("channel_post_id", value).
		Where(sq.Eq{"id": taskID}).
		ExecContext(ctx)
	if err != nil {
		return fmt.Errorf("set channel post id %s: %w", taskID, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set channel post id %s: rows affected: %w", taskID, err)
	}
	if rows == 0 {
		return store.ErrTaskNotFound
	}
	return nil
}

// GetTaskIDByChannelPost returns the task_id of the task whose card is the
// given postID, or ErrPostNotFound when postID is not a tracked card. Used by
// the MessageHasBeenPosted hook to decide whether a thread reply belongs to a
// task's card.
func (s *SQLStore) GetTaskIDByChannelPost(ctx context.Context, postID string) (string, error) {
	if postID == "" {
		return "", errors.New("get task id by channel post: post id is required")
	}
	var taskID string
	err := s.builder().
		Select("id").
		From(s.tableName(taskTableShort)).
		Where(sq.Eq{"channel_post_id": postID}).
		Limit(1).
		QueryRowContext(ctx).
		Scan(&taskID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", store.ErrPostNotFound
		}
		return "", fmt.Errorf("get task id by channel post %s: %w", postID, err)
	}
	return taskID, nil
}
