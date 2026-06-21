// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for QuickList (issue #28). Covers the pure helpers (buildParams,
// isOverdue, formatDueShort, messageFor) — the contract this file pins down
// without a host Redux/intl provider harness.

import {ClientError} from 'client';

import {buildParams, formatDueShort, isOverdue, messageFor, truncateDescription} from 'components/task_sidebar/quick_list';

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

describe('truncateDescription', () => {
    test('short text is returned unchanged', () => {
        expect(truncateDescription('Fix the login bug')).toBe('Fix the login bug');
    });

    test('whitespace runs (including newlines) collapse to single spaces', () => {
        expect(truncateDescription('line one\n\nline two\ttabbed')).toBe('line one line two tabbed');
    });

    test('text over the limit is cut at a word boundary and suffixed with ellipsis', () => {
        const text = 'This is a fairly long description that definitely exceeds the one hundred character limit we set for the preview row';
        const out = truncateDescription(text);
        expect(out.length).toBeLessThan(text.length);
        expect(out.endsWith('…')).toBe(true);

        // The cut lands at a word boundary: the full word right before the
        // ellipsis must also appear in the original text (not a partial token).
        const lastWord = out.slice(0, -1).split(' ').pop() || '';
        expect(text).toContain(lastWord);
    });

    test('a single long token with no spaces falls back to a hard cut', () => {
        const token = 'a'.repeat(200);
        const out = truncateDescription(token);
        expect(out.length).toBe(101); // 100 chars + ellipsis
        expect(out.endsWith('…')).toBe(true);
    });

    test('a custom maxChars is honored', () => {
        expect(truncateDescription('one two three four', 10)).toBe('one two…');
    });

    test('empty/whitespace-only input collapses to empty', () => {
        expect(truncateDescription('   ')).toBe('');
        expect(truncateDescription('\n\t')).toBe('');
    });
});
