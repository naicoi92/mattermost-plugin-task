// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import {dueBand} from 'components/shared/due_band';

// Table-test mirrors server TestDueBand (server/message_attachment_test.go) so
// both implementations stay in sync for the same input (design D1).

describe('components/shared/due_band', () => {
    const now = 1_700_000_000_000;
    const hour = 60 * 60 * 1000;

    const cases: Array<
    [string, number | undefined, string, 'muted' | 'warning' | 'danger']
    > = [
        ['no due → muted', undefined, 'todo', 'muted'],
        ['no due (0) → muted', 0, 'todo', 'muted'],
        ['terminal done → muted even if overdue', now - hour, 'done', 'muted'],
        ['terminal cancelled → muted', now + hour, 'cancelled', 'muted'],
        ['>72h → muted', now + (73 * hour), 'todo', 'muted'],
        ['exactly 72h → warning', now + (72 * hour), 'todo', 'warning'],
        ['48h → warning', now + (48 * hour), 'in_progress', 'warning'],
        [
            'exactly 24h → warning (boundary inclusive)',
            now + (24 * hour),
            'todo',
            'warning',
        ],
        ['just under 24h → danger', now + ((24 * hour) - 1), 'todo', 'danger'],
        ['12h → danger', now + (12 * hour), 'todo', 'danger'],
        ['overdue → danger', now - (5 * hour), 'todo', 'danger'],
        ['far past → danger', now - (100 * hour), 'in_progress', 'danger'],
    ];

    cases.forEach(([name, dueMs, status, want]) => {
        test(name, () => {
            expect(dueBand(dueMs, now, status)).toBe(want);
        });
    });
});
