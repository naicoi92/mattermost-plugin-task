package kvstore

import (
	"strings"

	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"

	"github.com/naicoi92/mattermost-plugin-task/server/model"
)

// Key prefix constants. These are used by both the store implementation and
// callers that need to build index keys manually.
const (
	keyTaskPrefix     = "t:"
	keyCommentPrefix  = ":c:"
	keyIndexPrefix    = "idx:"
	keySubtaskPrefix  = "idx:t:"
	keySubtaskInfix   = ":sub:"
	keyUserPrefix     = "idx:u:"
	keyAssignedInfix  = ":assigned:"
	keyCreatedInfix   = ":created:"
	keyChannelPrefix  = "idx:ch:"
	keyChannelInfix   = ":task:"
	keyAllTasksPrefix = "idx:all:task:"
	keyReminderPrefix = "idx:reminder:"
)

// pageSize is the number of keys fetched per ListKeys page.
const pageSize = 100

// Client implements KVStore using the Mattermost plugin KV service.
type Client struct {
	client *pluginapi.Client
}

// NewKVStore returns a KVStore backed by the given pluginapi client.
func NewKVStore(client *pluginapi.Client) KVStore {
	return Client{
		client: client,
	}
}

// SetAtomicWithRetries performs a read-modify-write on key using atomic
// compare-and-set, retrying on conflict (see MaxRetries / RetryBackoff). Each
// conflict is logged at warn level with the key name and retry count so
// contention shows up clearly in server logs.
func (c Client) SetAtomicWithRetries(key string, update func(old []byte) (any, error)) error {
	return SetAtomicWithRetries(&c.client.KV, c.client.Log.Warn, key, update)
}

// TaskKey returns the entity key for a task.
func TaskKey(id string) string {
	return keyTaskPrefix + id
}

// CommentKey returns the entity key for a comment on a task.
func CommentKey(taskID, commentID string) string {
	return keyTaskPrefix + taskID + keyCommentPrefix + commentID
}

// SubtaskKey returns the index edge key for a subtask membership.
func SubtaskKey(parentID, taskID string) string {
	return keySubtaskPrefix + parentID + keySubtaskInfix + taskID
}

// UserAssignedKey returns the index edge key for a task assigned to a user.
func UserAssignedKey(userID, taskID string) string {
	return keyUserPrefix + userID + keyAssignedInfix + taskID
}

// UserCreatedKey returns the index edge key for a task created by a user.
func UserCreatedKey(userID, taskID string) string {
	return keyUserPrefix + userID + keyCreatedInfix + taskID
}

// ChannelTaskKey returns the index edge key for a task in a channel.
func ChannelTaskKey(channelID, taskID string) string {
	return keyChannelPrefix + channelID + keyChannelInfix + taskID
}

// AllTasksKey returns the global "all tasks" index edge key.
func AllTasksKey(taskID string) string {
	return keyAllTasksPrefix + taskID
}

// ReminderKey returns the reminder edge key for a task.
func ReminderKey(taskID string) string {
	return keyReminderPrefix + taskID
}

// GetTask returns the task with the given ID, or nil if it does not exist.
func (c Client) GetTask(id string) (*model.Task, error) {
	var task model.Task
	if err := c.client.KV.Get(TaskKey(id), &task); err != nil {
		return nil, errors.Wrapf(err, "failed to get task %s", id)
	}
	if task.ID == "" {
		return nil, nil
	}
	return &task, nil
}

// SaveTask persists the given task under t:{task.ID}.
func (c Client) SaveTask(task model.Task) error {
	if task.ID == "" {
		return errors.New("task ID is required")
	}

	_, err := c.client.KV.Set(TaskKey(task.ID), task)
	if err != nil {
		return errors.Wrapf(err, "failed to save task %s", task.ID)
	}
	return nil
}

// DeleteTask removes the task entity t:{id}.
func (c Client) DeleteTask(id string) error {
	if err := c.client.KV.Delete(TaskKey(id)); err != nil {
		return errors.Wrapf(err, "failed to delete task %s", id)
	}
	return nil
}

// ListTaskIDsByPrefix scans keys matching prefix and returns the distinct
// task IDs embedded in those keys. Stale index entries whose task no longer
// exists are removed and omitted from the result.
func (c Client) ListTaskIDsByPrefix(prefix string) ([]string, error) {
	seen := make(map[string]struct{})
	var result []string
	page := 0

	for {
		keys, err := c.client.KV.ListKeys(page, pageSize, pluginapi.WithPrefix(prefix))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list keys with prefix %s", prefix)
		}

		for _, key := range keys {
			taskID, ok := extractTaskID(prefix, key)
			if !ok {
				continue
			}
			if _, exists := seen[taskID]; exists {
				continue
			}
			seen[taskID] = struct{}{}

			if exists, err := c.taskExists(taskID); err != nil {
				return nil, errors.Wrapf(err, "failed to check existence of task %s", taskID)
			} else if !exists {
				// Self-healing: the index edge points to a deleted task.
				if err := c.DeleteIndex(key); err != nil {
					return nil, errors.Wrapf(err, "failed to delete stale index %s", key)
				}
				continue
			}

			result = append(result, taskID)
		}

		if len(keys) < pageSize {
			break
		}
		page++
	}

	return result, nil
}

// SaveIndex writes an independent index edge key.
func (c Client) SaveIndex(key string) error {
	_, err := c.client.KV.Set(key, indexValue)
	if err != nil {
		return errors.Wrapf(err, "failed to save index %s", key)
	}
	return nil
}

// DeleteIndex removes an independent index edge key.
func (c Client) DeleteIndex(key string) error {
	if err := c.client.KV.Delete(key); err != nil {
		return errors.Wrapf(err, "failed to delete index %s", key)
	}
	return nil
}

// SaveSubtask persists a subtask membership edge from parentID to taskID.
func (c Client) SaveSubtask(parentID, taskID string) error {
	if parentID == "" {
		return errors.New("parent task ID is required")
	}
	if taskID == "" {
		return errors.New("task ID is required")
	}
	return c.SaveIndex(SubtaskKey(parentID, taskID))
}

// GetSubtaskIDs returns the task IDs registered as subtasks of parentID.
func (c Client) GetSubtaskIDs(parentID string) ([]string, error) {
	if parentID == "" {
		return nil, errors.New("parent task ID is required")
	}
	return c.ListTaskIDsByPrefix(keySubtaskPrefix + parentID + keySubtaskInfix)
}

// SaveComment persists a comment under t:{taskID}:c:{comment.ID}.
func (c Client) SaveComment(taskID string, comment model.Comment) error {
	if taskID == "" {
		return errors.New("task ID is required")
	}
	if comment.ID == "" {
		return errors.New("comment ID is required")
	}

	_, err := c.client.KV.Set(CommentKey(taskID, comment.ID), comment)
	if err != nil {
		return errors.Wrapf(err, "failed to save comment %s on task %s", comment.ID, taskID)
	}
	return nil
}

// GetCommentIDs returns the IDs of comments attached to taskID.
func (c Client) GetCommentIDs(taskID string) ([]string, error) {
	prefix := CommentKey(taskID, "")
	var result []string
	page := 0

	for {
		keys, err := c.client.KV.ListKeys(page, pageSize, pluginapi.WithPrefix(prefix))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list comment IDs for task %s", taskID)
		}

		for _, key := range keys {
			commentID, ok := extractCommentID(key)
			if !ok {
				continue
			}
			result = append(result, commentID)
		}

		if len(keys) < pageSize {
			break
		}
		page++
	}

	return result, nil
}

// SaveReminder stores the reminder value for taskID under idx:reminder:{taskID}.
func (c Client) SaveReminder(taskID string, value int64) error {
	if taskID == "" {
		return errors.New("task ID is required")
	}
	_, err := c.client.KV.Set(ReminderKey(taskID), value)
	if err != nil {
		return errors.Wrapf(err, "failed to save reminder for task %s", taskID)
	}
	return nil
}

// DeleteReminder removes the reminder edge for taskID.
func (c Client) DeleteReminder(taskID string) error {
	if taskID == "" {
		return errors.New("task ID is required")
	}
	if err := c.client.KV.Delete(ReminderKey(taskID)); err != nil {
		return errors.Wrapf(err, "failed to delete reminder for task %s", taskID)
	}
	return nil
}

// ListReminderKeys returns all keys prefixed with idx:reminder:.
func (c Client) ListReminderKeys() ([]string, error) {
	var result []string
	page := 0

	for {
		keys, err := c.client.KV.ListKeys(page, pageSize, pluginapi.WithPrefix(keyReminderPrefix))
		if err != nil {
			return nil, errors.Wrap(err, "failed to list reminder keys")
		}
		result = append(result, keys...)

		if len(keys) < pageSize {
			break
		}
		page++
	}

	return result, nil
}

// indexValue is a canonical placeholder for marker-only index records.
var indexValue = struct{}{}

// taskExists reports whether a task entity exists for the given ID.
func (c Client) taskExists(id string) (bool, error) {
	task, err := c.GetTask(id)
	if err != nil {
		return false, err
	}
	return task != nil, nil
}

// extractTaskID extracts the task ID from an index key given the expected prefix.
// The key format for index edges places the task ID at the end, after the final
// separator that follows the prefix.
func extractTaskID(prefix, key string) (string, bool) {
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}

	// The prefix already ends with a separator (e.g. idx:reminder: or
	// idx:t:{parent}:sub:), so the remainder of the key is the task ID.
	id := strings.TrimPrefix(key, prefix)
	return id, id != ""
}

// extractCommentID extracts the comment ULID from a comment entity key.
func extractCommentID(key string) (string, bool) {
	idx := strings.LastIndex(key, keyCommentPrefix)
	if idx < 0 {
		return "", false
	}
	commentID := key[idx+len(keyCommentPrefix):]
	return commentID, commentID != ""
}
