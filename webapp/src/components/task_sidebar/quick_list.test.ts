// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for QuickList (issue #28). Covers the pure helpers (buildParams,
// isOverdue, formatDueShort, messageFor) — the contract this file pins down
// without a host Redux/intl provider harness.

import {ClientError} from 'client';

import {buildParams, formatDueShort, isOverdue, messageFor} from 'components/task_sidebar/quick_list';

import type {Task} from 'types/tasks';

function makeTask(over: Partial<Task> = {}): Task {
    return {
        id: '1',
        summary: 't',
        description: '',
        channel_id: '',
        creator_id: '',
        assignee_id: '',
        channel_post_id: '',
        dm_post_id: '',
        is_all_day: false,
        status: 'todo',
        order_key: '',
        parent_task_id: '',
        reminder_fired: false,
        created_at: 0,
        updated_at: 0,
        ...over,
    };
}

describe('buildParams', () => {
    test('mine tab yields scope=mine and no channel_id', () => {
        const p = buildParams('mine', '', '', undefined, '');
        expect(p.scope).toBe('mine');
        expect(p.channel_id).toBeUndefined();
        expect(p.limit).toBe(25);
    });

    test('channel tab includes channel_id when provided', () => {
        const p = buildParams('channel', '', '', 'ch1', '');
        expect(p.scope).toBe('channel');
        expect(p.channel_id).toBe('ch1');
    });

    test('channel tab without a channelID omits channel_id', () => {
        const p = buildParams('channel', '', '', undefined, '');
        expect(p.channel_id).toBeUndefined();
    });

    test('status filter is passed through', () => {
        const p = buildParams('mine', 'done', '', undefined, '');
        expect(p.status).toBe('done');
    });

    test('due filter is passed through', () => {
        const p = buildParams('mine', '', 'today', undefined, '');
        expect(p.due).toBe('today');
    });

    test('after_order_key enables pagination', () => {
        const p = buildParams('mine', '', '', undefined, 'm0');
        expect(p.after_order_key).toBe('m0');
    });

    test('empty filters/after yield no optional fields', () => {
        const p = buildParams('mine', '', '', undefined, '');
        expect(p.status).toBeUndefined();
        expect(p.due).toBeUndefined();
        expect(p.after_order_key).toBeUndefined();
    });
});

describe('isOverdue', () => {
    test('no due date is not overdue', () => {
        expect(isOverdue(makeTask({due: undefined}))).toBe(false);
    });

    test('a future due date on an open task is not overdue', () => {
        expect(isOverdue(makeTask({due: Date.now() + 10000, status: 'todo'}))).toBe(false);
    });

    test('a past due date on an open task is overdue', () => {
        expect(isOverdue(makeTask({due: Date.now() - 10000, status: 'todo'}))).toBe(true);
    });

    test('a past due date on a done task is not overdue', () => {
        expect(isOverdue(makeTask({due: Date.now() - 10000, status: 'done'}))).toBe(false);
    });

    test('a past due date on a cancelled task is not overdue', () => {
        expect(isOverdue(makeTask({due: Date.now() - 10000, status: 'cancelled'}))).toBe(false);
    });
});

describe('formatDueShort', () => {
    test('renders a localized date string', () => {
        const out = formatDueShort(Date.UTC(2026, 5, 19), 'en');
        expect(typeof out).toBe('string');
        expect(out.length).toBeGreaterThan(0);
    });

    test('falls back to ISO when Intl throws', () => {
        const originalDTF = Intl.DateTimeFormat;
        Intl.DateTimeFormat = function() {
            throw new Error('boom');
        } as unknown as typeof Intl.DateTimeFormat;
        try {
            const out = formatDueShort(0, 'en');
            expect(typeof out).toBe('string');
            expect(out.length).toBeGreaterThan(0);
        } finally {
            Intl.DateTimeFormat = originalDTF;
        }
    });
});

describe('messageFor', () => {
    test('a ClientError surfaces its server message', () => {
        expect(messageFor(new ClientError(404, 'not found'))).toBe('not found');
    });

    test('a ClientError with empty message falls back', () => {
        expect(messageFor(new ClientError(500, ''))).toBe('request failed');
    });

    test('a generic Error surfaces its message', () => {
        expect(messageFor(new Error('offline'))).toBe('offline');
    });

    test('a non-Error value falls back', () => {
        expect(messageFor(null)).toBe('request failed');
    });
});
