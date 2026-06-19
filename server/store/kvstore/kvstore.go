// Package kvstore provides a typed key-value storage layer for the Task plugin.
//
// The store uses a "key-per-edge" schema: every entity and every index edge is
// stored as an independent KV record. This avoids write contention on shared
// containers and makes reads simple prefix scans.
//
// Key formats:
//
//	Task entity:           t:{taskID}
//	Comment entity:        t:{taskID}:c:{commentULID}
//	Subtask membership:    idx:t:{parentTaskID}:sub:{taskID}
//	User assigned tasks:   idx:u:{userId}:assigned:{taskID}
//	User created tasks:    idx:u:{userId}:created:{taskID}
//	Channel tasks:         idx:ch:{channelId}:task:{taskID}
//	All tasks index:       idx:all:task:{taskID}
//	Reminder edge:         idx:reminder:{taskID}
package kvstore

import "github.com/naicoi92/mattermost-plugin-task/server/model"

// KVStore is the typed interface used by the rest of the plugin to persist
// tasks, comments, subtask relationships, reminders and secondary indexes.
type KVStore interface {
	// GetTask returns the task with the given ID, or nil if it does not exist.
	GetTask(id string) (*model.Task, error)
	// SaveTask persists the given task under t:{task.ID}.
	SaveTask(task model.Task) error
	// DeleteTask removes the task entity t:{id}.
	DeleteTask(id string) error

	// ListTaskIDsByPrefix scans keys matching prefix and returns the distinct
	// task IDs embedded in those keys. Stale index entries whose task no longer
	// exists are removed and omitted from the result.
	ListTaskIDsByPrefix(prefix string) ([]string, error)
	// ListUserAssignedTaskIDs returns task IDs from idx:u:{userID}:assigned:.
	ListUserAssignedTaskIDs(userID string) ([]string, error)
	// ListUserCreatedTaskIDs returns task IDs from idx:u:{userID}:created:.
	ListUserCreatedTaskIDs(userID string) ([]string, error)
	// ListChannelTaskIDs returns task IDs from idx:ch:{channelID}:task:.
	ListChannelTaskIDs(channelID string) ([]string, error)
	// ListAllTaskIDs returns task IDs from the global idx:all:task: index.
	ListAllTaskIDs() ([]string, error)
	// SaveIndex writes an independent index edge key.
	SaveIndex(key string) error
	// DeleteIndex removes an independent index edge key.
	DeleteIndex(key string) error

	// SaveSubtask persists a subtask membership edge from parentID to taskID.
	SaveSubtask(parentID, taskID string) error
	// GetSubtaskIDs returns the task IDs registered as subtasks of parentID.
	GetSubtaskIDs(parentID string) ([]string, error)

	// SaveComment persists a comment under t:{taskID}:c:{comment.ID}.
	SaveComment(taskID string, comment model.Comment) error
	// GetCommentIDs returns the IDs of comments attached to taskID.
	GetCommentIDs(taskID string) ([]string, error)

	// SaveReminder stores the reminder value for taskID under idx:reminder:{taskID}.
	SaveReminder(taskID string, value int64) error
	// DeleteReminder removes the reminder edge for taskID.
	DeleteReminder(taskID string) error
	// ListReminderKeys returns all keys prefixed with idx:reminder:.
	ListReminderKeys() ([]string, error)

	// SetAtomicWithRetries performs a read-modify-write on key using atomic
	// compare-and-set semantics: it reads the current value, calls update to
	// derive the new value, and writes it back only if the value has not
	// changed. On conflict it retries up to MaxRetries times with
	// RetryBackoff between attempts, logging each conflict with the key name
	// and retry count. Use this for any read-modify-write (e.g. counters,
	// append-to-collection) where lost updates are not acceptable.
	SetAtomicWithRetries(key string, update func(old []byte) (any, error)) error
}
