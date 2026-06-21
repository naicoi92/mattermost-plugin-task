// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Single typed HTTP client for the webapp to call this plugin's REST endpoints
// (issue #31). Every function maps 1:1 to a route declared in server/api.go and
// returns the parsed JSON response, throwing ClientError on a non-2xx reply.
//
// The plugin id is read from the manifest so the base path stays in sync with
// plugin.json; it matches the server-side route prefix declared in api.go:
//   <siteUrl>/plugins/<plugin id>/api/v1/...
//
// Host-provided libraries (react, redux, react-router-dom) are declared as
// webpack externals; this client depends only on the browser fetch API.

import manifest from 'manifest';

import {Client4} from 'mattermost-redux/client';

import type {
    Comment,
    CreateCommentInput,
    CreateSubtaskInput,
    CreateTaskInput,
    ListTasksParams,
    PatchTaskInput,
    SetAssigneeInput,
    SetReminderInput,
    Task,
    TaskStatus,
} from 'types/tasks';

// Base URL prefix for every plugin REST call. Matches the prefix registered in
// server/api.go (PathPrefix("/api/v1")) under /plugins/<plugin id>.
export const PLUGIN_API_BASE_URL = `/plugins/${manifest.id}/api/v1`;

// ClientError carries the server's status code and message so callers can branch
// on well-known codes (404 not found, 403 forbidden, 409 parent-done conflict).
export class ClientError extends Error {
    status: number;
    message: string;

    constructor(status: number, message: string) {
        super(message);
        this.name = 'ClientError';
        this.status = status;
        this.message = message;

        // Restore the prototype chain, which Object.setPrototypeOf-based
        // subclasses lose under some transpilers; keeps instanceof reliable.
        Object.setPrototypeOf(this, ClientError.prototype);
    }
}

// Options for doFetch. `method` defaults to GET; `body` is JSON-serialized.
interface FetchOptions {
    method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
    body?: unknown;
}

// buildQuery converts the list params into a URL query string, omitting empty
// values so the server sees clean, optional filters.
function buildQuery(params?: ListTasksParams): string {
    if (!params) {
        return '';
    }
    const q = new URLSearchParams();
    if (params.scope) {
        q.set('scope', params.scope);
    }
    if (params.channel_id) {
        q.set('channel_id', params.channel_id);
    }
    if (params.status) {
        q.set('status', params.status);
    }
    if (params.due) {
        q.set('due', params.due);
    }
    if (params.after_order_key) {
        q.set('after_order_key', params.after_order_key);
    }
    if (params.limit) {
        q.set('limit', String(params.limit));
    }
    const str = q.toString();
    return str ? `?${str}` : '';
}

// doFetch performs a JSON request against the plugin API, returning the parsed
// body. A non-2xx response is surfaced as ClientError; 204 No Content yields
// undefined.
//
// Request options are delegated to Client4.getOptions(), the same helper the
// host webapp and the official plugins (e.g. mattermost-plugin-jira) use.
// Client4 injects the headers the Mattermost server requires before it forwards
// a request to a plugin: X-Requested-With: XMLHttpRequest, X-CSRF-Token (read
// from the MMCSRF cookie) on write methods, and credentials: 'include'. Without
// X-CSRF-Token the server rejects the CSRF check (EnableCSRFChecks is enabled
// by default) and never injects the Mattermost-User-Id header, so the plugin's
// auth middleware returns 401 even though the session cookie was sent.
export async function doFetch<T>(path: string, options: FetchOptions = {}): Promise<T> {
    const {method = 'GET', body} = options;
    const url = `${PLUGIN_API_BASE_URL}${path}`;

    const res = await fetch(url, Client4.getOptions({
        method,
        headers: body === undefined ? undefined : {'Content-Type': 'application/json'},
        body: body === undefined ? undefined : JSON.stringify(body),
    }));

    if (!res.ok) {
        // The server uses plain-text error bodies (writeError in api.go); fall
        // back to the status text when the body is empty.
        let message = '';
        try {
            message = (await res.text()).trim();
        } catch {
            message = '';
        }
        throw new ClientError(res.status, message || res.statusText || 'request failed');
    }

    // 204 No Content (DELETE endpoints): nothing to parse.
    if (res.status === 204) {
        return undefined as T;
    }

    // Some GET handlers may return an empty body; guard against JSON parse errors.
    const text = await res.text();
    if (!text) {
        return undefined as T;
    }
    return JSON.parse(text) as T;
}

// ---------------------------------------------------------------------------
// Tasks CRUD (server/api.go: POST/GET/PATCH/DELETE /tasks[/:id])
// ---------------------------------------------------------------------------

export function createTask(input: CreateTaskInput): Promise<Task> {
    return doFetch<Task>('/tasks', {method: 'POST', body: input});
}

export function getTask(id: string): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}`);
}

export function listTasks(params?: ListTasksParams): Promise<Task[]> {
    return doFetch<Task[]>(`/tasks${buildQuery(params)}`);
}

export function patchTask(id: string, input: PatchTaskInput): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}`, {method: 'PATCH', body: input});
}

export function deleteTask(id: string): Promise<void> {
    return doFetch<void>(`/tasks/${encodeURIComponent(id)}`, {method: 'DELETE'});
}

// ---------------------------------------------------------------------------
// Status & assignee (server/api.go: PATCH /tasks/:id/status,
// POST/DELETE /tasks/:id/assignee)
// ---------------------------------------------------------------------------

export function setTaskStatus(id: string, status: TaskStatus): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}/status`, {
        method: 'PATCH',
        body: {status},
    });
}

export function setTaskAssignee(id: string, userID: string): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}/assignee`, {
        method: 'POST',
        body: {user_id: userID} satisfies SetAssigneeInput,
    });
}

export function removeTaskAssignee(id: string): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}/assignee`, {method: 'DELETE'});
}

// ---------------------------------------------------------------------------
// Reminders (server/api.go: POST/DELETE /tasks/:id/reminder)
// ---------------------------------------------------------------------------

export function setReminder(id: string, offsetMS: number): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}/reminder`, {
        method: 'POST',
        body: {offset_ms: offsetMS} satisfies SetReminderInput,
    });
}

export function removeReminder(id: string): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}/reminder`, {method: 'DELETE'});
}

// ---------------------------------------------------------------------------
// Subtasks (server/api.go: POST/GET /tasks/:id/subtasks)
// ---------------------------------------------------------------------------

export function createSubtask(parentID: string, input: CreateSubtaskInput): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(parentID)}/subtasks`, {
        method: 'POST',
        body: input,
    });
}

export function listSubtasks(parentID: string): Promise<Task[]> {
    return doFetch<Task[]>(`/tasks/${encodeURIComponent(parentID)}/subtasks`);
}

// ---------------------------------------------------------------------------
// Comments (server/api.go: POST/GET /tasks/:id/comments)
// ---------------------------------------------------------------------------

export function createComment(taskID: string, input: CreateCommentInput): Promise<Comment> {
    return doFetch<Comment>(`/tasks/${encodeURIComponent(taskID)}/comments`, {
        method: 'POST',
        body: input,
    });
}

export function listComments(taskID: string): Promise<Comment[]> {
    return doFetch<Comment[]>(`/tasks/${encodeURIComponent(taskID)}/comments`);
}

// ---------------------------------------------------------------------------
// Kanban ordering (forward-looking; declared in issue #31's acceptance list but
// not yet backed by a server route). It is intentionally a thin stub so the
// webapp can adopt it without a client rewrite once the server endpoint lands.
// ---------------------------------------------------------------------------

// setTaskOrder re-orders a task for Kanban drag-and-drop. The server endpoint is
// not yet implemented; this stub keeps the client surface complete per #31.
export function setTaskOrder(id: string, orderKey: string): Promise<Task> {
    return doFetch<Task>(`/tasks/${encodeURIComponent(id)}/order`, {
        method: 'PATCH',
        body: {order_key: orderKey},
    });
}

// ---------------------------------------------------------------------------
// User lookup (host REST API v4) — used to resolve @username → user id for the
// New Task dialog assignee field (#96). Not under PLUGIN_API_BASE_URL: this is
// the host'"'"'s /api/v4 endpoint, authenticated via the same session cookie.
// ---------------------------------------------------------------------------

// User is the minimal slice of model.User the picker needs.
export interface User {
    id: string;
    username: string;
}

// getUserByUsername resolves a username (without the leading @) to a user via
// the host REST API. Throws ClientError (404) when the username is unknown. It
// hits the host /api/v4 path directly (not the plugin API prefix), so it
// performs its own fetch rather than reuse doFetch, but still delegates request
// options to Client4.getOptions() so the host's auth/credential handling is
// consistent (GET needs no CSRF token, but credentials: 'include' and
// X-Requested-With still apply).
export async function getUserByUsername(username: string): Promise<User> {
    const url = `/api/v4/users/username/${encodeURIComponent(username)}`;
    const res = await fetch(url, Client4.getOptions({}));
    if (!res.ok) {
        let message = '';
        try {
            message = (await res.text()).trim();
        } catch {
            message = '';
        }
        throw new ClientError(res.status, message || res.statusText || 'request failed');
    }
    return JSON.parse(await res.text()) as User;
}

// getUser resolves a user id to a user via the host REST API
// (GET /api/v4/users/<id>). Throws ClientError (404) when the id is unknown.
// Like getUserByUsername it hits the host /api/v4 path directly and delegates
// request options to Client4.getOptions({}) so auth/credentials/CSRF handling
// matches the rest of the client. Prefer the host Redux store (getUser
// selector) for already-loaded profiles; this is the fallback when the user
// isn't cached.
export async function getUser(userID: string): Promise<User> {
    const url = `/api/v4/users/${encodeURIComponent(userID)}`;
    const res = await fetch(url, Client4.getOptions({}));
    if (!res.ok) {
        let message = '';
        try {
            message = (await res.text()).trim();
        } catch {
            message = '';
        }
        throw new ClientError(res.status, message || res.statusText || 'request failed');
    }
    return JSON.parse(await res.text()) as User;
}

// UserSearchResult is the minimal slice of model.User the listing endpoints
// return. Kept compatible with getUserByUsername's User (id + username) but
// also carries the display name for richer picker rows.
export interface UserSearchResult {
    id: string;
    username: string;
    first_name?: string;
    last_name?: string;
    nickname?: string;
    is_bot?: boolean;
    delete_at?: number;
}

// searchUsers lists users for the assignee picker. When channelID is provided
// it scopes to that channel's members (host REST API v4
// GET /api/v4/users?in_channel=<id>&per_page=N); otherwise it lists users the
// caller can see (GET /api/v4/users?per_page=N&in_team=... would need a team
// id, so the no-channel path lists globally visible users). The optional term
// is sent as `term` for server-side filtering when non-empty; callers may also
// filter further client-side.
//
// Like getUserByUsername this hits the host /api/v4 path directly and delegates
// request options to Client4.getOptions({}) so auth/credentials/CSRF handling
// matches the rest of the client. Bots and deleted users are filtered out
// client-side so the picker only offers real, active people.
export async function searchUsers(term?: string, channelID?: string, perPage = 50): Promise<UserSearchResult[]> {
    const q = new URLSearchParams();
    q.set('per_page', String(perPage));
    if (channelID) {
        q.set('in_channel', channelID);
    }
    if (term && term.trim()) {
        q.set('term', term.trim());
    }
    const url = `/api/v4/users?${q.toString()}`;
    const res = await fetch(url, Client4.getOptions({}));
    if (!res.ok) {
        let message = '';
        try {
            message = (await res.text()).trim();
        } catch {
            message = '';
        }
        throw new ClientError(res.status, message || res.statusText || 'request failed');
    }
    const list = JSON.parse(await res.text()) as UserSearchResult[];
    return list.filter((u) => !u.is_bot && !u.delete_at);
}

export default {
    createTask,
    getTask,
    listTasks,
    patchTask,
    deleteTask,
    setTaskStatus,
    setTaskAssignee,
    removeTaskAssignee,
    setReminder,
    removeReminder,
    createSubtask,
    listSubtasks,
    createComment,
    listComments,
    setTaskOrder,
    getUserByUsername,
    getUser,
    searchUsers,
};
