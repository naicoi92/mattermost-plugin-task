// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for QuickList. Covers the pure helpers (buildParams, classifyGroup,
// isDueWithinDays, sortTasksByDue, buildGroups, isOverdue, messageFor,
// truncateDescription) — the contract this file pins down without a host
// Redux/intl provider harness.

import {ClientError} from 'client';

import {
    buildGroups,
    buildParams,
    classifyGroup,
    isDueWithinDays,
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

// startOfToday(ms) returns the local-midnight timestamp for the calendar day of
// `ms`, so day-boundary tests never flake near midnight.
function startOfToday(ms: number): number {
    const d = new Date(ms);
    return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}
const DAY_MS = 24 * 60 * 60 * 1000;
const HALF_DAY = Math.floor(DAY_MS / 2);

// atDay returns the ms epoch `n` whole days after `base` (parenthesized so no
// inline mixed operators). Callers add HALF_DAY / small offsets as plain sums
// so due dates never land on a midnight boundary (keeps day-diff math stable).
const atDay = (base: number, n: number): number => {
    const shifted = n * DAY_MS;
    return base + shifted;
};

describe('buildParams', () => {
    test('scope=mine omits channel_id and sends scope=mine', () => {
        const p = buildParams('mine', undefined, '');
        expect(p.scope).toBe('mine');
        expect(p.channel_id).toBeUndefined();
        expect(p.limit).toBe(25);
    });

    test('scope=mine ignores a supplied channelID', () => {
        // mine spans all channels; channel_id must not leak into the request.
        const p = buildParams('mine', 'ch1', '');
        expect(p.scope).toBe('mine');
        expect(p.channel_id).toBeUndefined();
    });

    test('scope=channel sends scope=channel + channel_id', () => {
        const p = buildParams('channel', 'ch1', '');
        expect(p.scope).toBe('channel');
        expect(p.channel_id).toBe('ch1');
    });

    test('scope=channel without a channelID omits channel_id', () => {
        const p = buildParams('channel', undefined, '');
        expect(p.channel_id).toBeUndefined();
    });

    test('after_order_key enables pagination', () => {
        const p = buildParams('mine', undefined, 'm0');
        expect(p.after_order_key).toBe('m0');
    });
});

describe('classifyGroup', () => {
    // Anchor `now` to local noon today so "today" and "1-3 day" windows never
    // cross a day boundary when the test runs near midnight.
    const now = (() => {
        const d = new Date();
        return new Date(
            d.getFullYear(),
            d.getMonth(),
            d.getDate(),
            12,
            0,
            0,
        ).getTime();
    })();
    const todayNoon = now;
    const base = startOfToday(now);
    const yesterday = todayNoon - DAY_MS;
    const tomorrow = atDay(base, 1) + HALF_DAY;
    const day2 = atDay(base, 2) + HALF_DAY;
    const day5 = atDay(base, 5) + HALF_DAY;

    test('urgent priority not yet due → URGENT', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'urgent', status: 'todo', due: day5}),
                now,
            ),
        ).toBe('urgent');
    });

    test('overdue standard → URGENT', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'standard', status: 'todo', due: yesterday}),
                now,
            ),
        ).toBe('urgent');
    });

    test('due today standard → URGENT', () => {
        expect(
            classifyGroup(
                makeTask({
                    priority: 'standard',
                    status: 'in_progress',
                    due: todayNoon,
                }),
                now,
            ),
        ).toBe('urgent');
    });

    test('important priority due far → IMPORTANT', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'important', status: 'todo', due: day5}),
                now,
            ),
        ).toBe('important');
    });

    test('standard due in 1-3 days → IMPORTANT', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'standard', status: 'todo', due: day2}),
                now,
            ),
        ).toBe('important');
    });

    test('standard due in 1 day → IMPORTANT', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'standard', status: 'todo', due: tomorrow}),
                now,
            ),
        ).toBe('important');
    });

    test('standard due >3 days → NORMAL', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'standard', status: 'todo', due: day5}),
                now,
            ),
        ).toBe('normal');
    });

    test('standard no due → NORMAL', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'standard', status: 'todo', due: undefined}),
                now,
            ),
        ).toBe('normal');
    });

    test('done status → DONE regardless of priority/due', () => {
        expect(
            classifyGroup(
                makeTask({status: 'done', priority: 'urgent', due: todayNoon}),
                now,
            ),
        ).toBe('done');
    });

    test('cancelled status → DONE', () => {
        expect(classifyGroup(makeTask({status: 'cancelled'}), now)).toBe('done');
    });

    test('overdue beats important priority → URGENT', () => {
        expect(
            classifyGroup(
                makeTask({priority: 'important', status: 'todo', due: yesterday}),
                now,
            ),
        ).toBe('urgent');
    });
});

describe('isDueWithinDays', () => {
    const now = (() => {
        const d = new Date();
        return new Date(
            d.getFullYear(),
            d.getMonth(),
            d.getDate(),
            12,
            0,
            0,
        ).getTime();
    })();
    const base = startOfToday(now);

    test('due tomorrow is within [1,3]', () => {
        expect(
            isDueWithinDays(makeTask({due: atDay(base, 1) + 1000}), now, 1, 3),
        ).toBe(true);
    });

    test('due in 3 days is within [1,3]', () => {
        expect(
            isDueWithinDays(makeTask({due: atDay(base, 3) + 1000}), now, 1, 3),
        ).toBe(true);
    });

    test('due today is NOT within [1,3]', () => {
        expect(isDueWithinDays(makeTask({due: base + 1000}), now, 1, 3)).toBe(
            false,
        );
    });

    test('due in 5 days is NOT within [1,3]', () => {
        expect(
            isDueWithinDays(makeTask({due: atDay(base, 5) + 1000}), now, 1, 3),
        ).toBe(false);
    });

    test('no due is not within any window', () => {
        expect(isDueWithinDays(makeTask({due: undefined}), now, 1, 3)).toBe(
            false,
        );
    });
});

describe('sortTasksByDue', () => {
    const {sortTasksByDue} = require('components/task_sidebar/quick_list');

    test('sorts ascending by due; no-due tasks go last', () => {
        const a = makeTask({id: 'a', due: 300});
        const b = makeTask({id: 'b', due: 100});
        const c = makeTask({id: 'c', due: undefined});
        const d = makeTask({id: 'd', due: 200});
        const out = sortTasksByDue([a, b, c, d]);
        expect(out.map((t: Task) => t.id)).toEqual(['b', 'd', 'a', 'c']);
    });

    test('stable: equal due preserves input order', () => {
        const a = makeTask({id: 'a', due: 100, order_key: 'k1'});
        const b = makeTask({id: 'b', due: 100, order_key: 'k2'});
        const out = sortTasksByDue([a, b]);
        expect(out.map((t: Task) => t.id)).toEqual(['a', 'b']);
    });

    test('all no-due preserves input order', () => {
        const a = makeTask({id: 'a', due: undefined});
        const b = makeTask({id: 'b', due: undefined});
        const out = sortTasksByDue([a, b]);
        expect(out.map((t: Task) => t.id)).toEqual(['a', 'b']);
    });
});

describe('buildGroups', () => {
    test('buckets into 4 canonical groups, sorted by due within each', () => {
        const now = (() => {
            const d = new Date();
            return new Date(
                d.getFullYear(),
                d.getMonth(),
                d.getDate(),
                12,
                0,
                0,
            ).getTime();
        })();
        const base = startOfToday(now);
        const urgentFar = makeTask({
            id: 'uf',
            priority: 'urgent',
            due: atDay(base, 10),
        }); // urgent by priority
        const urgentOver = makeTask({
            id: 'uo',
            priority: 'urgent',
            due: now - DAY_MS,
        }); // overdue too
        const important = makeTask({id: 'im', due: atDay(base, 2) + 1000}); // 1-3 days
        const normal = makeTask({id: 'no', due: atDay(base, 6)}); // >3 days
        const done = makeTask({id: 'dn', status: 'done'});

        const groups = buildGroups(
            [normal, urgentOver, important, urgentFar, done],
            now,
        );
        expect(groups.map((g) => g.key)).toEqual([
            'urgent',
            'important',
            'normal',
            'done',
        ]);

        // Urgent sorted by due asc: overdue (yesterday) before far-future urgent.
        expect(groups[0].items.map((t) => t.id)).toEqual(['uo', 'uf']);
        expect(groups[1].items.map((t) => t.id)).toEqual(['im']);
        expect(groups[2].items.map((t) => t.id)).toEqual(['no']);
        expect(groups[3].items.map((t) => t.id)).toEqual(['dn']);
    });

    test('empty groups are omitted, canonical order preserved', () => {
        const now = Date.now();
        const onlyNormal = makeTask({id: 'n', due: atDay(now, 10)});
        const groups = buildGroups([onlyNormal], now);
        expect(groups.map((g) => g.key)).toEqual(['normal']);
    });

    test('empty input yields no groups', () => {
        expect(buildGroups([], Date.now())).toEqual([]);
    });
});

describe('isOverdue', () => {
    test('no due date is not overdue', () => {
        expect(isOverdue(makeTask({due: undefined}))).toBe(false);
    });

    test('a due date >24h away on an open task is not overdue (danger band)', () => {
        // isOverdue now means "danger band" (due within 24h OR past).
        // A due 25h away is still warning band, not danger.
        const ms25h = 25 * 60 * 60 * 1000;
        expect(
            isOverdue(makeTask({due: Date.now() + ms25h, status: 'todo'})),
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
