// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for QuickList. Covers the pure helpers (buildParams, isOverdue,
// groupTasks, countByTab, messageFor, truncateDescription) — the contract this
// file pins down without a host Redux/intl provider harness.

import {ClientError} from 'client';

import {
    buildParams,
    countByTab,
    groupTasks,
    isOverdue,
    messageFor,
    truncateDescription,
} from 'components/task_sidebar/quick_list';

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
        priority: 'standard',
        order_key: '',
        parent_task_id: '',
        reminder_fired: false,
        created_at: 0,
        updated_at: 0,
        ...over,
    };
}

describe('buildParams', () => {
    test('channel mode (non-DM) sends scope=channel + channel_id', () => {
        const p = buildParams('all', 'ch1', false, '');
        expect(p.scope).toBe('channel');
        expect(p.channel_id).toBe('ch1');
        expect(p.partner_id).toBeUndefined();
        expect(p.limit).toBe(25);
    });

    test('direct mode (DM) now uses scope=channel + channel_id (all-channel model)', () => {
        const p = buildParams('all', 'u-partner', true, '');
        expect(p.scope).toBe('channel');
        expect(p.channel_id).toBe('u-partner');
        expect(p.partner_id).toBeUndefined();
    });

    test('channel mode without a channelID omits channel_id', () => {
        const p = buildParams('all', undefined, false, '');
        expect(p.channel_id).toBeUndefined();
    });

    test('todo tab sets status filter', () => {
        const p = buildParams('todo', 'ch1', false, '');
        expect(p.status).toBe('todo');
    });

    test('today tab sets due filter', () => {
        const p = buildParams('today', 'ch1', false, '');
        expect(p.due).toBe('today');
        expect(p.status).toBeUndefined();
    });

    test('all tab sends neither status nor due', () => {
        const p = buildParams('all', 'ch1', false, '');
        expect(p.status).toBeUndefined();
        expect(p.due).toBeUndefined();
    });

    test('after_order_key enables pagination', () => {
        const p = buildParams('all', 'ch1', false, 'm0');
        expect(p.after_order_key).toBe('m0');
    });
});

describe('isOverdue', () => {
    test('no due date is not overdue', () => {
        expect(isOverdue(makeTask({due: undefined}))).toBe(false);
    });

    test('a due date >24h away on an open task is not overdue (danger band)', () => {
        // isOverdue now means "danger band" (due within 24h OR past).
        // A due 25h away is still warning band, not danger.
        expect(
            isOverdue(
                makeTask({due: Date.now() + ((25 * 60 * 60) * 1000), status: 'todo'}),
            ),
        ).toBe(false);
    });

    test('a past due date on an open task is overdue', () => {
        expect(
            isOverdue(makeTask({due: Date.now() - 10000, status: 'todo'})),
        ).toBe(true);
    });

    test('a past due date on a done task is not overdue', () => {
        expect(
            isOverdue(makeTask({due: Date.now() - 10000, status: 'done'})),
        ).toBe(false);
    });

    test('a past due date on a cancelled task is not overdue', () => {
        expect(
            isOverdue(makeTask({due: Date.now() - 10000, status: 'cancelled'})),
        ).toBe(false);
    });
});

describe('groupTasks', () => {
    test('buckets by status + due window', () => {
        const overdue = makeTask({
            id: '1',
            status: 'todo',
            due: Date.now() - 86400000,
        });
        const today = makeTask({
            id: '2',
            status: 'todo',

            // Anchor to local noon today so the bucket test never crosses a
            // day boundary when run near midnight (CodeRabbit review).
            due: (() => {
                const n = new Date();
                return new Date(
                    n.getFullYear(),
                    n.getMonth(),
                    n.getDate(),
                    12,
                    0,
                    0,
                ).getTime();
            })(),
        });
        const upcoming = makeTask({
            id: '3',
            status: 'todo',
            due: Date.now() + (30 * 86400000),
        });
        const done = makeTask({id: '4', status: 'done'});
        const cancelled = makeTask({id: '5', status: 'cancelled'});
        const groups = groupTasks([overdue, today, upcoming, done, cancelled]);

        // Three non-empty buckets in canonical order.
        expect(groups.map((g) => g.key)).toEqual([
            'attention',
            'upcoming',
            'completed',
        ]);
        expect(groups[0].items.map((t) => t.id).sort()).toEqual(['1', '2']);
        expect(groups[1].items.map((t) => t.id)).toEqual(['3']);
        expect(groups[2].items.map((t) => t.id).sort()).toEqual(['4', '5']);
    });

    test('empty buckets are omitted', () => {
        const onlyDone = makeTask({id: '1', status: 'done'});
        const groups = groupTasks([onlyDone]);
        expect(groups.map((g) => g.key)).toEqual(['completed']);
    });
});

describe('countByTab', () => {
    test('counts each tab from the loaded list', () => {
        const tasks: Task[] = [
            makeTask({id: '1', status: 'todo'}),
            makeTask({id: '2', status: 'todo'}),
            makeTask({id: '3', status: 'done'}),
            makeTask({id: '4', status: 'cancelled'}),
        ];
        const counts = countByTab(tasks, false);
        expect(counts.all.label).toBe('4');
        expect(counts.todo.label).toBe('2');
        expect(counts.done.label).toBe('1');
        expect(counts.cancelled.label).toBe('1');
        expect(counts.in_progress.label).toBe('0');
    });

    test("appends '+' when hasMore is true", () => {
        const tasks: Task[] = [makeTask({id: '1', status: 'todo'})];
        const counts = countByTab(tasks, true);
        expect(counts.all.label).toBe('1+');
        expect(counts.all.plus).toBe(true);
    });

    test("does not append '+' for zero-count tabs", () => {
        const tasks: Task[] = [];
        const counts = countByTab(tasks, true);
        expect(counts.all.label).toBe('0');
        expect(counts.all.plus).toBe(false);
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
        expect(truncateDescription('line one\n\nline two\ttabbed')).toBe(
            'line one line two tabbed',
        );
    });

    test('text over the limit is cut at a word boundary and suffixed with ellipsis', () => {
        const text =
			'This is a fairly long description that definitely exceeds the one hundred character limit we set for the preview row';
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
