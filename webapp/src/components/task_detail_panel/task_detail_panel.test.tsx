// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

/**
 * @jest-environment jsdom
 */

// Unit tests for TaskDetailPanel (issue #29). These cover the pure helpers
// (formatDue, formatTimestamp, isOverdue via formatDue behavior, messageFor)
// and the permission gate. Full render+interaction tests would need a host
// Redux/intl provider; the helpers + the delete-permission logic are the
// contract this file pins down without that harness.

import * as client from 'client';
import {ClientError} from 'client';
import React from 'react';
import TestRenderer, {act} from 'react-test-renderer';

import TaskDetailPanel, {
    activityLabelKey,
    commentAuthorLabel,
    commentBodyText,
    formatDue,
    formatTimestamp,
    mergeActivity,
    messageFor,
} from 'components/task_detail_panel/task_detail_panel';
import type {TaskDetailPanelProps} from 'components/task_detail_panel/task_detail_panel';

import {TaskEventType} from 'types/tasks';
import type {Comment, Task, TaskEvent} from 'types/tasks';

// =============================================================================
// Render harness (react-test-renderer) for the panel-level AC tests that the
// verify report flagged as missing (GAP 1–5). The repo has no host
// Redux/intl provider or react-dom in devDeps, so these render via
// react-test-renderer with the panel's host hooks (react-redux, i18n_utils,
// client, user resolution) mocked at module level. The pure-helper tests
// above are unaffected: the mocked modules' real classes (e.g. ClientError)
// are preserved, and the exported helpers stay pure.
// =============================================================================

const PLUGIN_STATE_KEY = 'plugins-com.mattermost.plugin-task';

// Configurable mock state. Names are `mock*`-prefixed so the babel
// jest-hoist plugin allows the jest.mock factories below to reference them.
let mockStoreState: Record<string, unknown> = {};
let mockActorLabels: Record<string, string> = {};
let mockActorStatuses: Record<string, string> = {};
let mockAssigneeLabel = '';
let mockLocale = 'vi';
let mockDispatch = jest.fn();

jest.mock('react-redux', () => ({
    useDispatch: () => mockDispatch,
    useSelector: (sel: (s: unknown) => unknown) => sel(mockStoreState),
}));

jest.mock('i18n_utils', () => {
    const fs = require('fs');
    const path = require('path');
    const vi = JSON.parse(
        fs.readFileSync(
            path.join(__dirname, '..', '..', '..', 'i18n', 'vi.json'),
            'utf8',
        ),
    );
    const mock = JSON.parse(
        fs.readFileSync(
            path.join(__dirname, '..', '..', '..', 'tests', 'i18n_mock.json'),
            'utf8',
        ),
    );
    return {
        useFormatMessage: () => (key: string) => vi[key] ?? mock[key] ?? key,
        useActiveLocale: () => mockLocale,
    };
});

jest.mock('client', () => {
    const actual = jest.requireActual('client');
    const mocked: Record<string, unknown> = {...actual};
    for (const k of Object.keys(actual)) {
        const v = (actual as Record<string, unknown>)[k];

        // Keep ClientError real so messageFor's instanceof check still works.
        if (typeof v === 'function' && k !== 'ClientError') {
            mocked[k] = jest.fn(() => Promise.resolve(undefined));
        }
    }
    return mocked;
});

jest.mock('components/user_picker/use_resolved_user', () => ({
    useResolvedUser: () => ({label: mockAssigneeLabel, user: undefined}),
    useResolvedUsers: () => mockActorLabels,
    useResolvedStatuses: () => mockActorStatuses,
}));

jest.mock('components/task_sidebar/quick_list', () => ({
    isDueSoon: () => false,
}));

jest.mock('components/user_picker/user_picker', () => {
    const React2 = jest.requireActual('react');
    return {
        __esModule: true,
        default: () => React2.createElement('span'),
    };
});

jest.mock('components/shared/meta_dropdown', () => {
    const React2 = jest.requireActual('react');
    return {
        __esModule: true,
        default: () => React2.createElement('span'),
    };
});

function makeTask(over: Partial<Task> = {}): Task {
    return {
        id: 'T',
        summary: 'Task T',
        description: '',
        channel_id: '',
        creator_id: 'u1',
        assignee_id: '',
        channel_post_id: '',
        dm_post_id: '',
        is_all_day: false,
        status: 'todo',
        priority: 'standard',
        order_key: 'k',
        parent_task_id: '',
        reminder_fired: false,
        created_at: 1000,
        updated_at: 1000,
        ...over,
    };
}

function makeComment(over: Partial<Comment> = {}): Comment {
    return {
        id: 'c1',
        task_id: 'T',
        post_id: 'p1',
        author_id: 'alice',
        created_at: 1000,
        content: 'hello',
        file_ids: [],
        deleted: false,
        ...over,
    };
}

function makeEvent(over: Partial<TaskEvent> = {}): TaskEvent {
    return {
        id: 'e1',
        task_id: 'T',
        actor_id: 'bob',
        event_type: 'status_changed',
        created_at: 1000,
        ...over,
    };
}

function resetHarnessState() {
    mockActorLabels = {};
    mockActorStatuses = {};
    mockAssigneeLabel = '';
    mockLocale = 'vi';
    mockStoreState = {
        [PLUGIN_STATE_KEY]: {
            selectedTaskID: 'T',
            selectedTask: null,
            commentRev: {} as Record<string, number>,
        },
        entities: {
            channels: {currentChannelId: '', channels: {}},
            users: {users: {}},
        },
    };
    mockDispatch = jest.fn();
    jest.clearAllMocks();
}

// Set the comment-rev signal for the open task (simulates a WS
// task_updated with changedFields=["comment"] being dispatched by index.tsx).
function bumpCommentRev(taskID: string, rev: number) {
    const slice = mockStoreState[PLUGIN_STATE_KEY] as {
        commentRev: Record<string, number>;
    };
    slice.commentRev = {...slice.commentRev, [taskID]: rev};
}

// nodeText recursively concatenates the rendered text of a test instance.
function nodeText(node: unknown): string {
    if (node === null || node === undefined) {
        return '';
    }
    if (typeof node === 'string') {
        return node;
    }
    if (typeof node === 'number') {
        return String(node);
    }
    if (Array.isArray(node)) {
        return node.map(nodeText).join('');
    }
    const inst = node as { children?: unknown[] };
    return nodeText(inst.children ?? []);
}

function renderPanel(props: Partial<TaskDetailPanelProps> = {}) {
    return TestRenderer.create(
        React.createElement(TaskDetailPanel, {taskID: 'T', ...props}),
    );
}

// activityItemInstances returns the <li> test instances for the Activity feed.
function activityItemInstances(root: TestRenderer.ReactTestInstance) {
    return root.
        findAllByType('li').
        filter((li) =>
            String(li.props.className ?? '').includes('task-detail__activity-item'),
        );
}

function findAvatar(li: TestRenderer.ReactTestInstance) {
    return li.
        findAllByType('span').
        find((s) =>
            String(s.props.className ?? '').includes('task-detail__activity-avatar'),
        );
}

function findBody(li: TestRenderer.ReactTestInstance) {
    return li.
        findAllByType('div').
        find((d) =>
            String(d.props.className ?? '').includes('task-detail__activity-body'),
        );
}

function findCommentCard(li: TestRenderer.ReactTestInstance) {
    return li.
        findAllByType('div').
        find((d) =>
            String(d.props.className ?? '').includes('task-detail__activity-comment'),
        );
}

function findComposer(root: TestRenderer.ReactTestInstance) {
    return root.
        findAllByType('textarea').
        find((t) =>
            String(t.props.className ?? '').includes('task-detail__comment-input'),
        );
}

function findSendButton(root: TestRenderer.ReactTestInstance) {
    return root.
        findAllByType('button').
        find((b) =>
            String(b.props.className ?? '').includes('task-detail__comment-send'),
        );
}

// defer returns a controllable pending promise for dedupe timing tests.
function defer<T>() {
    let resolve!: (v: T) => void;
    const promise = new Promise<T>((res) => {
        resolve = res;
    });
    return {promise, resolve};
}

describe('comment render helpers (Task 5 — author_id + deleted placeholder)', () => {
    // AC1: the author label resolves from the row author_id snapshot via the
    // resolved-users map, falling back to the raw id (never '?').
    test('commentAuthorLabel resolves the author from author_id', () => {
        const c: Comment = {
            id: 'c1',
            task_id: 'T',
            post_id: 'p1',
            author_id: 'alice',
            created_at: 1000,
            content: 'hello',
            file_ids: [],
            deleted: false,
        };
        expect(commentAuthorLabel(c, {alice: '@Alice'})).toBe('@Alice');
    });

    test('commentAuthorLabel falls back to the raw author_id (not "?")', () => {
        const c: Comment = {
            id: 'c1',
            task_id: 'T',
            post_id: 'p1',
            author_id: 'alice',
            created_at: 0,
            content: '',
            file_ids: [],
            deleted: false,
        };
        expect(commentAuthorLabel(c, {})).toBe('alice');
    });

    // AC2: a deleted comment renders the placeholder, not the (missing) content.
    test('commentBodyText renders the placeholder for a deleted comment', () => {
        const c: Comment = {
            id: 'c1',
            task_id: 'T',
            post_id: 'p1',
            author_id: 'alice',
            created_at: 0,
            content: '',
            file_ids: [],
            deleted: true,
        };
        expect(commentBodyText(c, '(comment đã bị xóa)')).toBe(
            '(comment đã bị xóa)',
        );
    });

    test('commentBodyText renders the content for a live comment', () => {
        const c: Comment = {
            id: 'c1',
            task_id: 'T',
            post_id: 'p1',
            author_id: 'alice',
            created_at: 0,
            content: 'hello',
            file_ids: [],
            deleted: false,
        };
        expect(commentBodyText(c, '(comment đã bị xóa)')).toBe('hello');
    });
});

describe('formatDue', () => {
    test('renders a non-empty relative string for a future date', () => {
        // 30 days from "now" → beyond the within-7-days branch, so the output
        // carries the absolute date (weekday + day + month, same year).
        const ms = Date.now() + (30 * 24 * 60 * 60 * 1000);
        const out = formatDue(ms, 'en');
        expect(typeof out).toBe('string');
        expect(out.length).toBeGreaterThan(0);
    });

    test('respects the locale (vi produces a valid shape)', () => {
        const ms = Date.now() + (30 * 24 * 60 * 60 * 1000);
        const en = formatDue(ms, 'en');
        const vi = formatDue(ms, 'vi');

        // Both non-empty; they need not differ token-for-token, but the call must
        // not throw and must return a string.
        expect(en.length).toBeGreaterThan(0);
        expect(vi.length).toBeGreaterThan(0);
    });

    test('falls back to ISO when Intl throws', () => {
        const originalDTF = Intl.DateTimeFormat;
        let threw = false;
        Intl.DateTimeFormat = function() {
            threw = true;
            throw new Error('boom');
        } as unknown as typeof Intl.DateTimeFormat;
        try {
            const out = formatDue(Date.now() + (30 * 86400000), 'en');
            expect(typeof out).toBe('string');
            expect(out.length).toBeGreaterThan(0);
            expect(threw).toBe(true);
        } finally {
            Intl.DateTimeFormat = originalDTF;
        }
    });
});

describe('formatTimestamp', () => {
    test('renders a short date+time string', () => {
        const ms = Date.UTC(2026, 5, 19, 9, 30, 0);
        const out = formatTimestamp(ms, 'en');

        // Year may render 2-digit; assert the month/day + time are present.
        expect(out).toMatch(/6\/19\/26/);
    });

    test('never throws on a valid timestamp', () => {
        expect(() => formatTimestamp(Date.now(), 'vi')).not.toThrow();
    });
});

describe('ClientError message extraction', () => {
    // Tests the production messageFor directly (exported) rather than a copy.
    test('a ClientError surfaces its server message', () => {
        expect(messageFor(new ClientError(404, 'task not found'))).toBe(
            'task not found',
        );
    });

    test('a ClientError with empty message falls back', () => {
        expect(messageFor(new ClientError(500, ''))).toBe('request failed');
    });

    test('a generic Error surfaces its message', () => {
        expect(messageFor(new Error('network down'))).toBe('network down');
    });

    test('a non-Error value falls back to a generic message', () => {
        expect(messageFor('something weird')).toBe('request failed');
    });

    test('null falls back to a generic message', () => {
        expect(messageFor(null)).toBe('request failed');
    });
});

describe('Activity feed merge/sort (Task 7.3 — AC5)', () => {
    function ev(id: string, createdAt: number, eventType = 'created'): TaskEvent {
        return {
            id,
            task_id: 'T',
            actor_id: 'a',
            event_type: eventType,
            created_at: createdAt,
            to_value: undefined,
        };
    }
    function cm(id: string, createdAt: number): Comment {
        return {
            id,
            task_id: 'T',
            post_id: 'p',
            author_id: 'a',
            created_at: createdAt,
            content: 'x',
            file_ids: [],
            deleted: false,
        };
    }

    test('interleaves events and comments newest-first', () => {
        const events = [ev('e1', 1000), ev('e3', 3000)];
        const comments = [cm('c2', 2000)];
        const merged = mergeActivity(comments, events);
        expect(merged.map((m) => m.id)).toEqual(['e3', 'c2', 'e1']);
    });

    test('at equal created_at, comment sorts before event (typeRank comment=0)', () => {
        const events = [ev('e9', 2000)];
        const comments = [cm('c9', 2000)];
        const merged = mergeActivity(comments, events);
        expect(merged.map((m) => m.id)).toEqual(['c9', 'e9']);
    });

    test('at equal created_at and kind, id descending is the final tie-breaker', () => {
        const events = [ev('a1', 2000), ev('b2', 2000)];
        const merged = mergeActivity([], events);
        expect(merged.map((m) => m.id)).toEqual(['b2', 'a1']);
    });

    // Bug: a comment produces BOTH a task_comments row AND an EventCommented
    // audit event whose to_value points at the same comment id. The merged
    // feed used to render both as separate items (a comment card + a bare
    // "@author đã bình luận" event with no body) — appearing as "2 comment boxes".
    // mergeActivity MUST drop the EventCommented whose to_value is a comment id
    // already present in the feed (the comment item carries the body).
    test('dedupes EventCommented whose to_value is a present comment id', () => {
        const comment = cm('c1', 2000);
        const events = [
            {...ev('e-comment', 2000, 'commented'), to_value: 'c1'},
            ev('e-other', 1000, 'status_changed'),
        ];
        const merged = mergeActivity([comment], events);
        expect(merged.map((m) => m.id)).toEqual(['c1', 'e-other']);
    });

    // Keep an EventCommented whose to_value does NOT match a present comment
    // (e.g. its backing post was deleted out-of-band so the comment row is gone):
    // it still shows in the feed as a bare event (author + "đã bình luận").
    test('keeps EventCommented whose to_value has no matching comment', () => {
        const events = [{...ev('e-orphan', 2000, 'commented'), to_value: 'gone'}];
        const merged = mergeActivity([], events);
        expect(merged.map((m) => m.id)).toEqual(['e-orphan']);
    });
});

describe('Activity Vietnamese action labels (Task 7.4 — AC6)', () => {
    // The jest moduleNameMapper replaces i18n/*.json imports with a mock bundle,
    // so read the real vi.json from disk via fs to assert the label coverage.
    const fs = require('fs');
    const path = require('path');
    const viBundle = JSON.parse(
        fs.readFileSync(
            path.join(__dirname, '..', '..', '..', 'i18n', 'vi.json'),
            'utf8',
        ),
    ) as Record<string, string>;

    const expected: Record<string, string> = {
        created: 'đã tạo task',
        status_changed: 'đã đổi trạng thái',
        assigned: 'đã gán người làm',
        unassigned: 'đã bỏ gán người làm',
        due_changed: 'đã đổi hạn chót',
        summary_changed: 'đã đổi tiêu đề',
        description_changed: 'đã đổi mô tả',
        priority_changed: 'đã đổi mức ưu tiên',
        reminder_set: 'đã đặt nhắc nhở',
        reminder_fired: 'nhắc nhở đã kích hoạt',
        reminder_cleared: 'đã xóa nhắc nhở',
        commented: 'đã bình luận',
        subtask_added: 'đã thêm subtask',
        deleted: 'đã xóa task',
    };

    test('every TaskEventType constant maps to a non-empty Vietnamese label', () => {
        const types = Object.values(TaskEventType);
        expect(types).toHaveLength(14);
        for (const type of types) {
            const key = activityLabelKey(type);
            expect(key).toBe(`webapp.task.activity.label.${type}`);
            const label = viBundle[key];
            expect(label).toBe(expected[type]);
            expect(label.length).toBeGreaterThan(0);
        }
    });

    test('the commented label replaces the old comments.commented key', () => {
        expect(viBundle[activityLabelKey('commented')]).toBe('đã bình luận');
    });
});

describe('composer helpers (Task 8 — textarea + keyboard + auto-grow)', () => {
    // AC7: auto-grow caps at 120px.
    test('composerCappedHeight caps at 120 and never exceeds it', () => {
        const {
            composerCappedHeight,
        } = require('components/task_detail_panel/task_detail_panel');
        expect(composerCappedHeight(60)).toBe(60);
        expect(composerCappedHeight(120)).toBe(120);
        expect(composerCappedHeight(500)).toBe(120);
    });

    // AC7: Enter sends, Shift+Enter inserts a newline (does not send).
    test('composerKeyDown returns send on Enter without Shift', () => {
        const {
            composerKeyDown,
        } = require('components/task_detail_panel/task_detail_panel');
        expect(composerKeyDown({key: 'Enter', shiftKey: false})).toBe('send');
    });

    test('composerKeyDown returns newline on Shift+Enter (not send)', () => {
        const {
            composerKeyDown,
        } = require('components/task_detail_panel/task_detail_panel');
        expect(composerKeyDown({key: 'Enter', shiftKey: true})).toBe('newline');
    });

    test('composerKeyDown returns none for other keys', () => {
        const {
            composerKeyDown,
        } = require('components/task_detail_panel/task_detail_panel');
        expect(composerKeyDown({key: 'a', shiftKey: false})).toBe('none');
    });

    // AC7: send button disabled when empty or whitespace-only.
    test('composerInputValid is false for empty/whitespace, true otherwise', () => {
        const {
            composerInputValid,
        } = require('components/task_detail_panel/task_detail_panel');
        expect(composerInputValid('')).toBe(false);
        expect(composerInputValid('   ')).toBe(false);
        expect(composerInputValid('\t\n')).toBe(false);
        expect(composerInputValid('hi')).toBe(true);
        expect(composerInputValid(' hi ')).toBe(true);
    });
});

// =============================================================================
// Panel-render tests closing the verify-report gaps (GAP 1–5).
// These render TaskDetailPanel via react-test-renderer with the host hooks
// mocked (see the harness above) and assert the AC behaviors at the element /
// render level the strict-TDD task briefs mandated.
// =============================================================================

function setupClientMocks(opts: {
    task?: Task;
    comments?: Comment[];
    events?: TaskEvent[];
}) {
    (client.getTask as jest.Mock).mockResolvedValue(opts.task ?? makeTask());
    (client.listSubtasks as jest.Mock).mockResolvedValue([]);
    (client.listComments as jest.Mock).mockResolvedValue(opts.comments ?? []);
    (client.listTaskEvents as jest.Mock).mockResolvedValue(opts.events ?? []);
    (client.createComment as jest.Mock).mockResolvedValue(undefined);
}

describe('GAP 1 — AC3 panel realtime refetch + dedupe (task-realtime-sync)', () => {
    test('AC3: a task_updated with changedFields=[comment] refetches comments and renders the new comment WITHOUT a taskID change (second viewer)', async () => {
        resetHarnessState();
        const old = makeComment({
            id: 'c-old',
            author_id: 'alice',
            content: 'old text',
            created_at: 1000,
        });
        mockActorLabels = {alice: '@Alice'};
        setupClientMocks({comments: [old]});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        // Initial feed: only the old comment.
        let items = activityItemInstances(renderer.root);
        expect(items).toHaveLength(1);
        expect(nodeText(findCommentCard(items[0]!))).toContain('old text');
        const baselineCalls = (client.listComments as jest.Mock).mock.calls.length;

        // Simulate a second viewer: another user creates a comment → the server
        // broadcasts task_updated with changedFields=["comment"] → index.tsx
        // dispatches COMMENT_REV_BUMP → the panel's commentRevForTask changes.
        // The taskID does NOT change (V2 did not reselect the task).
        const fresh = makeComment({
            id: 'c-new',
            author_id: 'alice',
            content: 'fresh from peer',
            created_at: 5000,
        });
        (client.listComments as jest.Mock).mockResolvedValue([fresh, old]);
        await act(async () => {
            bumpCommentRev('T', 1);
            renderer.update(React.createElement(TaskDetailPanel, {taskID: 'T'}));
        });

        // A refetch occurred (listComments called again) …
        expect(
            (client.listComments as jest.Mock).mock.calls.length,
        ).toBeGreaterThan(baselineCalls);

        // … every refetch was for the still-open task T (no taskID change) …
        expect(
            (client.listComments as jest.Mock).mock.calls.some(
                (c: unknown[]) => c[0] === 'T',
            ),
        ).toBe(true);

        // … and the new comment renders at the top of the feed (newest-first).
        items = activityItemInstances(renderer.root);
        expect(items).toHaveLength(2);
        expect(nodeText(findCommentCard(items[0]!))).toContain('fresh from peer');

        // The task is still T (header summary present) — taskID did not change.
        expect(
            renderer.root.
                findAllByType('h2').
                some((h) => nodeText(h).includes('Task T')),
        ).toBe(true);
    });

    test('AC3 dedupe: a rapid second changedFields=[comment] event while a refetch is in flight does NOT issue a parallel duplicate listComments', async () => {
        resetHarnessState();
        const c1 = makeComment({id: 'c1', content: 'one', created_at: 1000});
        setupClientMocks({comments: [c1]});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        // Steady state: mount effects settled. Make the next listComments stay
        // pending so the comment-refetch effect's in-flight gate is exercised.
        const d = defer<Comment[]>();
        (client.listComments as jest.Mock).mockReturnValue(d.promise);
        (client.listTaskEvents as jest.Mock).mockResolvedValue([]);
        (client.listComments as jest.Mock).mockClear();

        // First bump → comment-refetch effect runs → exactly one in-flight fetch.
        await act(async () => {
            bumpCommentRev('T', 1);
            renderer.update(React.createElement(TaskDetailPanel, {taskID: 'T'}));
        });
        expect(client.listComments as jest.Mock).toHaveBeenCalledTimes(1);

        // Rapid second bump WHILE the first refetch is still in flight → the
        // inflightRef/pendingRef coalesce MUST NOT start a parallel duplicate.
        await act(async () => {
            bumpCommentRev('T', 2);
            renderer.update(React.createElement(TaskDetailPanel, {taskID: 'T'}));
        });
        expect(client.listComments as jest.Mock).toHaveBeenCalledTimes(1);

        // Settling the in-flight fetch coalesces exactly one re-run (sequential,
        // not parallel) — never a third call from the second bump.
        await act(async () => {
            d.resolve([c1]);
        });
        expect(
            (client.listComments as jest.Mock).mock.calls.length,
        ).toBeGreaterThanOrEqual(2);
        expect(
            (client.listComments as jest.Mock).mock.calls.length,
        ).toBeLessThanOrEqual(2);
    });
});

describe('GAP 2 — AC8 new comment renders as the first feed item (task-details-panel)', () => {
    test('AC8: after creating a comment from the composer it renders as the FIRST (top) Activity feed item', async () => {
        resetHarnessState();
        const older = makeComment({
            id: 'c-old',
            author_id: 'alice',
            content: 'older item',
            created_at: 1000,
        });
        mockActorLabels = {alice: '@Alice'};
        setupClientMocks({comments: [older]});
        (client.createComment as jest.Mock).mockResolvedValue(
            makeComment({
                id: 'c-new',
                author_id: 'alice',
                content: 'fresh top',
                created_at: 5000,
            }),
        );
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        // Type into the composer, then send.
        const composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onChange({target: {value: 'fresh top'}});
        });
        const send = findSendButton(renderer.root)!;
        await act(async () => {
            send.props.onClick();
        });

        const items = activityItemInstances(renderer.root);
        expect(items.length).toBeGreaterThanOrEqual(2);

        // The freshly created comment is the FIRST (top) feed item.
        expect(nodeText(findCommentCard(items[0]!))).toContain('fresh top');
    });
});

describe('GAP 3 — AC5 Activity item render (task-event-activity)', () => {
    test('AC5: a comment item renders avatar, actor name, Vietnamese action label, relative time, and the comment text card below the action line', async () => {
        resetHarnessState();
        const c = makeComment({
            id: 'c1',
            author_id: 'alice',
            content: 'hello',
            created_at: Date.UTC(2026, 5, 19, 9, 30, 0),
        });
        mockActorLabels = {alice: '@Alice'};
        mockActorStatuses = {alice: 'online'};
        setupClientMocks({comments: [c]});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        const items = activityItemInstances(renderer.root);
        expect(items).toHaveLength(1);
        const li = items[0]!;

        // Avatar: gradient initials pill (initials derived from "@Alice" → "AL").
        const avatar = findAvatar(li)!;
        expect(String(avatar.props.className)).toContain(
            'task-detail__activity-avatar',
        );
        expect(nodeText(avatar)).toBe('AL');

        // Actor display name + Vietnamese action label "đã bình luận".
        const body = findBody(li)!;
        const bodyText = nodeText(body);
        expect(bodyText).toContain('@Alice');
        expect(bodyText).toContain('đã bình luận');

        // Relative time present (non-empty).
        const time = li.
            findAllByType('span').
            find((s) =>
                String(s.props.className ?? '').includes('task-detail__activity-time'),
            )!;
        expect(nodeText(time).length).toBeGreaterThan(0);

        // Comment text card BELOW the action line, containing the content.
        const card = findCommentCard(li)!;
        expect(card).toBeDefined();
        expect(nodeText(card)).toContain('hello');
    });

    test('AC5: an event item renders avatar, actor name, Vietnamese action label, relative time and NO comment text card', async () => {
        resetHarnessState();
        const e = makeEvent({
            id: 'e1',
            actor_id: 'bob',
            event_type: 'status_changed',
            created_at: Date.UTC(2026, 5, 19, 8, 0, 0),
        });
        mockActorLabels = {bob: '@Bob'};
        mockActorStatuses = {bob: 'away'};
        setupClientMocks({events: [e]});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        const items = activityItemInstances(renderer.root);
        expect(items).toHaveLength(1);
        const li = items[0]!;

        const avatar = findAvatar(li)!;
        expect(String(avatar.props.className)).toContain(
            'task-detail__activity-avatar',
        );

        const bodyText = nodeText(findBody(li)!);
        expect(bodyText).toContain('@Bob');
        expect(bodyText).toContain('đã đổi trạng thái');

        // An event item MUST NOT render a comment text card.
        expect(findCommentCard(li)).toBeUndefined();
    });

    test('AC5: a deleted:true comment renders the (comment đã bị xóa) placeholder card', async () => {
        resetHarnessState();
        const c = makeComment({
            id: 'c1',
            author_id: 'alice',
            content: '',
            deleted: true,
            created_at: 1000,
        });
        mockActorLabels = {alice: '@Alice'};
        setupClientMocks({comments: [c]});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        const li = activityItemInstances(renderer.root)[0]!;
        const card = findCommentCard(li)!;
        expect(card).toBeDefined();
        expect(nodeText(card)).toContain('(comment đã bị xóa)');
    });
});

describe('GAP 4 — AC7 element-level composer (task-details-panel)', () => {
    test('AC7: the composer is a <textarea>, not an <input>', async () => {
        resetHarnessState();
        setupClientMocks({});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        const textareas = renderer.root.findAllByType('textarea');

        // Exactly one textarea (the composer); the description is a div while
        // not editing, so no second textarea renders.
        expect(textareas).toHaveLength(1);
        const composer = textareas[0]!;
        expect(composer.type).toBe('textarea');
        expect(String(composer.props.className)).toContain(
            'task-detail__comment-input',
        );
    });

    test('AC7: the textarea auto-grows and caps height at 120px', async () => {
        resetHarnessState();
        setupClientMocks({});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });
        const composer = findComposer(renderer.root)!;
        const style: Record<string, string> = {};

        // Content exceeding 120px → capped at 120px (internal scroll).
        act(() => {
            composer.props.onInput({currentTarget: {style, scrollHeight: 500}});
        });
        expect(style.height).toBe('120px');

        // Small content → grows to its scrollHeight, not forced to 120.
        act(() => {
            composer.props.onInput({currentTarget: {style, scrollHeight: 60}});
        });
        expect(style.height).toBe('60px');
    });

    test('AC7: Enter sends the comment (no newline); Shift+Enter inserts a newline (no send)', async () => {
        resetHarnessState();
        setupClientMocks({});
        (client.createComment as jest.Mock).mockResolvedValue(
            makeComment({id: 'c-new', content: 'hi', created_at: 5000}),
        );
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        // Type non-empty input first (so addComment does not early-return).
        let composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onChange({target: {value: 'hi'}});
        });

        // Re-fetch the composer so its onKeyDown closes over newComment="hi".
        composer = findComposer(renderer.root)!;

        // Enter (no Shift) → send: preventDefault fires, createComment called.
        (client.createComment as jest.Mock).mockClear();
        let prevented = false;
        await act(async () => {
            composer.props.onKeyDown({
                key: 'Enter',
                shiftKey: false,
                preventDefault: () => {
                    prevented = true;
                },
            });
        });
        expect(prevented).toBe(true);
        expect(client.createComment as jest.Mock).toHaveBeenCalled();

        // Shift+Enter → newline: no preventDefault, no send.
        (client.createComment as jest.Mock).mockClear();
        let prevented2 = false;
        await act(async () => {
            composer.props.onKeyDown({
                key: 'Enter',
                shiftKey: true,
                preventDefault: () => {
                    prevented2 = true;
                },
            });
        });
        expect(prevented2).toBe(false);
        expect(client.createComment as jest.Mock).not.toHaveBeenCalled();
    });

    test('AC7: the send button is disabled when input empty/whitespace and enabled when non-whitespace', async () => {
        resetHarnessState();
        setupClientMocks({});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        // Empty → disabled.
        expect(findSendButton(renderer.root)!.props.disabled).toBe(true);

        // Whitespace-only → disabled.
        let composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onChange({target: {value: '   '}});
        });
        expect(findSendButton(renderer.root)!.props.disabled).toBe(true);

        // Non-whitespace → enabled.
        composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onChange({target: {value: 'hi'}});
        });
        expect(findSendButton(renderer.root)!.props.disabled).toBe(false);
    });
});

describe('GAP 5 — AC5/AC6 status-dot wiring (task-details-panel styling)', () => {
    // The avatar status dot MUST reflect the actor's presence status via a
    // data-driven modifier class (online/away/dnd/offline), not a dead
    // offline default. Presence is resolved through useResolvedStatuses
    // (backed by client.getUserStatus → host /api/v4/users/:id/status).

    test('AC5/status-dot: an online actor → avatar has the --online modifier', async () => {
        resetHarnessState();
        const c = makeComment({
            id: 'c1',
            author_id: 'alice',
            content: 'hi',
            created_at: 1000,
        });
        mockActorLabels = {alice: '@Alice'};
        mockActorStatuses = {alice: 'online'};
        setupClientMocks({comments: [c]});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });
        const li = activityItemInstances(renderer.root)[0]!;
        const avatar = findAvatar(li)!;
        expect(String(avatar.props.className)).toContain(
            'task-detail__activity-avatar--online',
        );
    });

    test('AC5/status-dot: an away actor → avatar has the --away modifier (data-driven, not hardcoded offline)', async () => {
        resetHarnessState();
        const c = makeComment({
            id: 'c1',
            author_id: 'bob',
            content: 'hi',
            created_at: 1000,
        });
        mockActorLabels = {bob: '@Bob'};
        mockActorStatuses = {bob: 'away'};
        setupClientMocks({comments: [c]});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });
        const li = activityItemInstances(renderer.root)[0]!;
        const avatar = findAvatar(li)!;
        expect(String(avatar.props.className)).toContain(
            'task-detail__activity-avatar--away',
        );
        expect(String(avatar.props.className)).not.toContain('--offline');
    });

    test('actorStatusClass maps each presence status to its modifier (offline fallback is explicit, data-driven)', () => {
        const {
            actorStatusClass,
        } = require('components/task_detail_panel/task_detail_panel');
        expect(actorStatusClass('online')).toBe(
            'task-detail__activity-avatar--online',
        );
        expect(actorStatusClass('away')).toBe('task-detail__activity-avatar--away');
        expect(actorStatusClass('dnd')).toBe('task-detail__activity-avatar--dnd');
        expect(actorStatusClass('offline')).toBe(
            'task-detail__activity-avatar--offline',
        );

        // Unknown / missing status → explicit offline fallback (not a dead default).
        expect(actorStatusClass(undefined)).toBe(
            'task-detail__activity-avatar--offline',
        );
        expect(actorStatusClass('')).toBe('task-detail__activity-avatar--offline');
    });
});

describe('FIX-B — composer markup follows open-design (task-comment-composer-fix)', () => {
    test('composer uses .comment-box > .comment-field with a textarea + icon-only send button', async () => {
        resetHarnessState();
        setupClientMocks({});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });
        const root = renderer.root;

        // The open-design container (.comment-box) wraps a .comment-field that
        // holds the textarea + the send control.
        const box = root.
            findAllByType('div').
            find((d) =>
                String(d.props.className ?? '').includes('task-detail__comment-box'),
            );
        expect(box).toBeTruthy();
        if (!box) {
            throw new Error('expected a .task-detail__comment-box container');
        }

        const field = box.
            findAllByType('div').
            find((d) =>
                String(d.props.className ?? '').includes('task-detail__comment-field'),
            );
        expect(field).toBeTruthy();
        if (!field) {
            throw new Error('expected a .task-detail__comment-field inside the box');
        }

        // The field holds exactly one textarea (the composer).
        const textareas = field.findAllByType('textarea');
        expect(textareas).toHaveLength(1);
        expect(String(textareas[0]!.props.className)).toContain(
            'task-detail__comment-input',
        );

        // The field holds exactly one icon-only send button (.comment-send),
        // NOT the old text "Gửi" primary button (.task-btn--primary).
        const sendBtns = field.
            findAllByType('button').
            filter((b) =>
                String(b.props.className ?? '').includes('task-detail__comment-send'),
            );
        expect(sendBtns).toHaveLength(1);
        expect(sendBtns[0]!.props['aria-label']).toBeTruthy();

        // The old flex-row container (.task-detail__comment-input as a DIV)
        // must no longer exist.
        const oldContainer = root.
            findAllByType('div').
            find(
                (d) =>
                    String(d.props.className ?? '').trim() ===
					'task-detail__comment-input',
            );
        expect(oldContainer).toBeFalsy();
    });
});

// ============================================================================
// FEAT-C — @mention autocomplete (task-comment-composer-fix)
// ============================================================================

function findMentionDropdown(root: TestRenderer.ReactTestInstance) {
    return root.
        findAllByType('ul').
        find((ul) =>
            String(ul.props.className ?? '').includes(
                'task-detail__mention-dropdown',
            ),
        );
}

function findMentionItems(root: TestRenderer.ReactTestInstance) {
    return root.
        findAllByType('li').
        filter((li) =>
            String(li.props.className ?? '').includes('task-detail__mention-item'),
        );
}

// Wait for the debounced (150ms) mention fetch + its async resolution to flush.
async function flushMentionFetch() {
    await act(async () => {
        await new Promise((r) => setTimeout(r, 220));
    });
}

describe('FEAT-C — @mention autocomplete (task-comment-composer-fix)', () => {
    test('3.2: typing fast coalesces to the last query (debounce + drop-stale)', async () => {
        jest.useFakeTimers();
        try {
            resetHarnessState();
            (client.searchUsers as jest.Mock).mockResolvedValue([
                {id: 'u-ab', username: 'abby'},
            ]);
            setupClientMocks({task: makeTask({channel_id: 'ch1'})});
            let renderer!: TestRenderer.ReactTestRenderer;
            await act(async () => {
                renderer = renderPanel();
            });

            // Type ' @a' then ' @ab' before the 150ms debounce fires.
            let composer = findComposer(renderer.root)!;
            await act(async () => {
                composer.props.onChange({
                    target: {value: ' @a', selectionStart: 3},
                });
            });
            composer = findComposer(renderer.root)!;
            await act(async () => {
                composer.props.onChange({
                    target: {value: ' @ab', selectionStart: 4},
                });
            });

            // No fetch yet (debounced).
            expect(client.searchUsers as jest.Mock).not.toHaveBeenCalled();

            // Advance past the debounce window.
            await act(async () => {
                jest.advanceTimersByTime(200);
            });

            // Only the LAST query ('ab') was issued — the 'a' fetch was coalesced away.
            const calls = (client.searchUsers as jest.Mock).mock.calls;
            expect(calls.length).toBe(1);
            expect(calls[0]![0]).toBe('ab');

            // The dropdown reflects the last query's result.
            const items = findMentionItems(renderer.root);
            expect(items.length).toBe(1);
        } finally {
            jest.useRealTimers();
        }
    });

    test('3.4: ArrowDown/ArrowUp wrap highlight; Enter inserts; Esc closes without inserting', async () => {
        resetHarnessState();
        (client.searchUsers as jest.Mock).mockResolvedValue([
            {id: 'u1', username: 'alice'},
            {id: 'u2', username: 'bob'},
            {id: 'u3', username: 'carol'},
        ]);
        setupClientMocks({task: makeTask({channel_id: 'ch1'})});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        // Open the dropdown by typing ' @'.
        let composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onChange({target: {value: ' @', selectionStart: 2}});
        });
        await flushMentionFetch();

        let items = findMentionItems(renderer.root);
        expect(items).toHaveLength(3);

        // First item is highlighted by default.
        expect(String(items[0]!.props.className)).toContain('--highlighted');
        expect(String(items[1]!.props.className)).not.toContain('--highlighted');

        // ArrowDown moves highlight to the 2nd item.
        composer = findComposer(renderer.root)!;
        act(() => {
            composer.props.onKeyDown({
                key: 'ArrowDown',
                preventDefault: () => {},
            });
        });
        items = findMentionItems(renderer.root);
        expect(String(items[0]!.props.className)).not.toContain('--highlighted');
        expect(String(items[1]!.props.className)).toContain('--highlighted');

        // ArrowUp from the 2nd item wraps? No — go back up to 1st, then ArrowUp
        // again to wrap to the LAST (carol). Two ArrowUps from index 1 → index 2 (wrap).
        composer = findComposer(renderer.root)!;
        act(() => {
            composer.props.onKeyDown({key: 'ArrowUp', preventDefault: () => {}});
        });
        composer = findComposer(renderer.root)!;
        act(() => {
            composer.props.onKeyDown({key: 'ArrowUp', preventDefault: () => {}});
        });
        items = findMentionItems(renderer.root);
        expect(String(items[2]!.props.className)).toContain('--highlighted');

        // Enter inserts the highlighted mention (@carol) and closes the dropdown.
        composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onKeyDown({
                key: 'Enter',
                shiftKey: false,
                preventDefault: () => {},
            });
        });
        expect(findMentionDropdown(renderer.root)).toBeFalsy();
        expect(findComposer(renderer.root)!.props.value).toContain('@carol');
    });

    test('3.4: Escape closes the dropdown without inserting', async () => {
        resetHarnessState();
        (client.searchUsers as jest.Mock).mockResolvedValue([
            {id: 'u1', username: 'alice'},
        ]);
        setupClientMocks({task: makeTask({channel_id: 'ch1'})});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        let composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onChange({target: {value: ' @', selectionStart: 2}});
        });
        await flushMentionFetch();
        expect(findMentionDropdown(renderer.root)).toBeTruthy();

        composer = findComposer(renderer.root)!;
        act(() => {
            composer.props.onKeyDown({key: 'Escape', preventDefault: () => {}});
        });
        expect(findMentionDropdown(renderer.root)).toBeFalsy();

        // Text unchanged (still just ' @').
        expect(findComposer(renderer.root)!.props.value).toBe(' @');
    });

    test('3.6: selecting a mention then sending posts content containing @username verbatim', async () => {
        resetHarnessState();
        (client.createComment as jest.Mock).mockResolvedValue(
            makeComment({id: 'c-new', content: 'x', created_at: 5000}),
        );
        (client.searchUsers as jest.Mock).mockResolvedValue([
            {id: 'u-carol', username: 'carol'},
        ]);
        setupClientMocks({task: makeTask({channel_id: 'ch1'})});
        let renderer!: TestRenderer.ReactTestRenderer;
        await act(async () => {
            renderer = renderPanel();
        });

        // Open dropdown and pick 'carol' via mouseDown on the item.
        const composer = findComposer(renderer.root)!;
        await act(async () => {
            composer.props.onChange({target: {value: ' @', selectionStart: 2}});
        });
        await flushMentionFetch();
        const item = findMentionItems(renderer.root)[0]!;
        await act(async () => {
            item.props.onMouseDown({preventDefault: () => {}});
        });

        // The composer now contains '@carol '. Click the send button.
        const send = findSendButton(renderer.root)!;
        await act(async () => {
            send.props.onClick();
        });

        const call = (client.createComment as jest.Mock).mock.calls[0]!;
        expect(call[1]).toMatchObject({
            content: expect.stringContaining('@carol'),
        });
    });
});
