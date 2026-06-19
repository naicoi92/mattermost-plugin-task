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

export type TaskStatus = typeof TaskStatus[keyof typeof TaskStatus];

// Task mirrors server/model/task.Task. Pointer-typed fields use `number | null`
// so the webapp can represent an absent value distinct from zero, matching the
// server's `omitempty` + `*int64` semantics.
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
    order_key: string; // global fractional-index rank for Kanban ordering
    completed_at?: number;
    cancelled_at?: number;
    parent_task_id: string; // non-empty for subtasks
    reminder_offset?: number; // ms before due; absent means no reminder
    reminder_fired: boolean;
    created_at: number;
    updated_at: number;
}

// Comment mirrors server/model/comment.Comment.
export interface Comment {
    id: string;
    user_id: string;
    content: string;
    created_at: number;
    updated_at: number;
}

// ListScope enumerates the Quick List result scopes, matching task.Scope.
export type ListScope = 'mine' | 'channel' | 'all';

// ListTasksParams is the query-string shape for GET /tasks. It mirrors the
// server's task.ListQuery (server/task/service.go) minus the server-only
// UserID/ChannelID-from-context fields.
export interface ListTasksParams {
    scope?: ListScope;
    channel_id?: string;
    status?: TaskStatus | '';
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
    assignee_id?: string;
    due?: number;
    is_all_day?: boolean;
    parent_task_id?: string;
    reminder_offset?: number;
}

// PatchTaskInput is the JSON body for PATCH /tasks/:id. Only fields named in
// update_fields are modified; a field present in update_fields with a null
// pointer clears that field. Matches server PatchInput (server/task/service.go).
export interface PatchTaskInput {
    update_fields: Array<'summary' | 'description' | 'due' | 'is_all_day'>;
    summary?: string;
    description?: string | null;
    due?: number | null;
    is_all_day?: boolean;
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
// server createCommentRequest.
export interface CreateCommentInput {
    content: string;
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
