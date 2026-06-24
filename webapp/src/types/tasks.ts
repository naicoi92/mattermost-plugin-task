// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Type definitions mirroring the server's task and comment models
// (server/model/task.go, server/model/comment.go). These are pure data carriers
// for the webapp API client (webapp/src/client.ts) and React components.

// Task statuses, matching model.Status* in server/model/task.go.
export const TaskStatus = {
    Todo: 'todo',
    InProgress: 'in_progress',
    Done: 'done',
    Cancelled: 'cancelled',
} as const;

export type TaskStatus = (typeof TaskStatus)[keyof typeof TaskStatus];

// Task priorities, matching model.Priority* in server/model/priority.go.
// Mirrors the Mattermost message-priority enum (standard/important/urgent);
// standard is the implicit default.
export const TaskPriority = {
    Standard: 'standard',
    Important: 'important',
    Urgent: 'urgent',
} as const;

export type TaskPriority = (typeof TaskPriority)[keyof typeof TaskPriority];

// Task mirrors server/model/task.Task. Optional fields (`?:`) are absent when
// the server omits them (matching Go's `omitempty` on a `*int64`), which in
// TypeScript is `number | undefined`. This is distinct from PatchTaskInput
// below, which uses explicit `| null` to signal "clear this field" on a PATCH.
export interface Task {
    id: string;
    summary: string;
    description: string;
    channel_id: string;
    creator_id: string;
    assignee_id: string;
    channel_post_id: string;
    dm_post_id: string;
    due?: number; // ms epoch; absent means no due date
    is_all_day: boolean;
    status: TaskStatus;
    priority: TaskPriority;
    order_key: string; // global fractional-index rank for Kanban ordering
    completed_at?: number;
    cancelled_at?: number;
    parent_task_id: string; // non-empty for subtasks
    reminder_offset?: number; // ms before due; absent means no reminder
    reminder_fired: boolean;
    created_at: number;
    updated_at: number;
}

// Comment mirrors the server listComments transport response (server/api.go
// commentResponse): the DB row fields plus content (resolved from the backing
// post.Message) and a deleted flag for out-of-band deleted posts. The server
// is the single source of truth for these fields; the webapp does not send
// user_id/updated_at (dropped — the server no longer emits them).
export interface Comment {
    id: string;
    task_id: string;
    post_id: string;
    author_id: string; // was user_id; the row snapshot, not re-derived from the post
    created_at: number;
    content: string; // from post.Message (single source of truth, Hướng A)
    deleted: boolean; // true when the backing post was deleted out-of-band
}

// TaskEventType mirrors the Event* constants in server/model/task_event.go.
// The Activity feed label map (webapp.task.activity.label.*) covers every
// value here; an unknown type must not render an English/empty string.
export const TaskEventType = {
    Created: 'created',
    StatusChanged: 'status_changed',
    Assigned: 'assigned',
    Unassigned: 'unassigned',
    DueChanged: 'due_changed',
    SummaryChanged: 'summary_changed',
    DescriptionChanged: 'description_changed',
    PriorityChanged: 'priority_changed',
    ReminderSet: 'reminder_set',
    ReminderFired: 'reminder_fired',
    ReminderCleared: 'reminder_cleared',
    Commented: 'commented',
    SubtaskAdded: 'subtask_added',
    Deleted: 'deleted',
} as const;
export type TaskEventType = (typeof TaskEventType)[keyof typeof TaskEventType];

// TaskEvent mirrors server/model/task_event.go: one row of the task audit trail.
// FromValue/ToValue are JSON snapshots (absent when nil on the server).
export interface TaskEvent {
    id: string;
    task_id: string;
    actor_id: string;
    event_type: string;
    from_value?: string;
    to_value?: string;
    created_at: number;
}

// ListScope enumerates the Quick List result scopes, matching task.Scope. Two
// scopes: channel (tasks of a channel) and direct (tasks shared between two
// DM users). The earlier mine/all scopes were removed with the slash-command
// and mobile-dialog paths.
export type ListScope = 'channel' | 'direct';

// ListTasksParams is the query-string shape for GET /tasks. It mirrors the
// server's task.ListQuery (server/task/service.go) minus the server-only
// UserID/ChannelID-from-context fields.
export interface ListTasksParams {
    scope?: ListScope;
    channel_id?: string;
    partner_id?: string;
    status?: TaskStatus | '';
    priority?: TaskPriority | '';
    due?: string;
    after_order_key?: string;
    limit?: number;
}

// CreateTaskInput is the JSON body for POST /tasks. It matches the server's
// createTaskRequest (server/api.go): creator_id is filled server-side from the
// authenticated user.
export interface CreateTaskInput {
    summary: string;
    description?: string;
    channel_id?: string;

    // post_channel_id is the originating channel that should receive the
    // announce card when the task itself is personal (empty channel_id), e.g.
    // a task created in a DM. It does not change the task's own scope — only
    // where the card is posted (server/api.go createTask).
    post_channel_id?: string;
    assignee_id?: string;
    due?: number;
    is_all_day?: boolean;
    parent_task_id?: string;
    reminder_offset?: number;
    priority?: TaskPriority;
}

// ShareTaskResult is the response body of POST /tasks/:id/share
// (server/api.go shareTask). post_id is the card post id - newly created, or
// the existing one when the share was idempotent (the task already had a card
// in that channel).
export interface ShareTaskResult {
    post_id: string;
}

// PatchTaskInput is the JSON body for PATCH /tasks/:id. Only fields named in
// update_fields are modified; a field present in update_fields with a null
// pointer clears that field (for fields that support clearing). Matches server
// PatchInput (server/task/service.go).
export interface PatchTaskInput {
    update_fields: Array<
    'summary' | 'description' | 'due' | 'is_all_day' | 'priority'
    >;
    summary?: string;
    description?: string | null;
    due?: number | null;
    is_all_day?: boolean;

    // priority is NOT nullable like due/description: the server treats a nil
    // pointer as a no-op (priority always has a value — the default 'standard').
    // Send a non-null TaskPriority to change it. The `| null` in the union is
    // kept only to satisfy generic PATCH encoders; callers should not send null.
    priority?: TaskPriority | null;
}

// CreateSubtaskInput is the JSON body for POST /tasks/:id/subtasks. The subtask
// inherits the parent's channel and (by default) assignee; explicit fields
// override the inherited defaults. Matches server createSubtaskRequest.
export interface CreateSubtaskInput {
    summary: string;
    assignee_id?: string;
    due?: number;
}

// CreateCommentInput is the JSON body for POST /tasks/:id/comments. Matches
// server createCommentRequest. channel_id is the channel the viewer is acting
// from so the comment threads under the task's card IN that channel (Change B
// shared-task fix); omitted for contexts with no active channel.
export interface CreateCommentInput {
    content: string;
    channel_id?: string;
}

// SetReminderInput is the JSON body for POST /tasks/:id/reminder. Matches
// server setReminderRequest.
export interface SetReminderInput {
    offset_ms: number;
}

// SetAssigneeInput is the JSON body for POST /tasks/:id/assignee. Matches
// server setAssigneeRequest.
export interface SetAssigneeInput {
    user_id: string;
}
