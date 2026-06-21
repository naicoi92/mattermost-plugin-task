// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

import formatDueRelative from 'components/shared/format_due_relative';

// Reference "now": 2026-06-21T10:00 local (we use a fixed date and compute
// other timestamps relative to it so the assertions are deterministic).
const NOW_MS = Date.UTC(2026, 5, 21, 10, 0); // 2026-06-21T10:00 UTC
const ONE_DAY = 24 * 60 * 60 * 1000;

describe('components/shared/format_due_relative', () => {
    test('same calendar day (local) → "Today, HH:mm"', () => {
        // 4 hours after now (same UTC day → same local day for most TZs).
        const due = NOW_MS + (4 * 60 * 60 * 1000);
        const out = formatDueRelative({dueMs: due, locale: 'en', nowMs: NOW_MS});
        expect(out).toMatch(/^Today, \d{1,2}:\d{2}/);
    });

    test('tomorrow (local) → "Tomorrow"', () => {
        const due = NOW_MS + ONE_DAY;
        const out = formatDueRelative({dueMs: due, locale: 'en', nowMs: NOW_MS});
        expect(out).toBe('Tomorrow');
    });

    test('overdue + open → "N days overdue" (English plural)', () => {
        const due = NOW_MS - (3 * ONE_DAY);
        const out = formatDueRelative({dueMs: due, locale: 'en', nowMs: NOW_MS, isOverdue: true});
        expect(out).toBe('3 days overdue');
    });

    test('overdue + open (1 day) → singular "1 day overdue"', () => {
        const due = NOW_MS - ONE_DAY;
        const out = formatDueRelative({dueMs: due, locale: 'en', nowMs: NOW_MS, isOverdue: true});
        expect(out).toBe('1 day overdue');
    });

    test('Vietnamese locale → "Quá hạn N ngày"', () => {
        const due = NOW_MS - (2 * ONE_DAY);
        const out = formatDueRelative({dueMs: due, locale: 'vi', nowMs: NOW_MS, isOverdue: true});
        expect(out).toBe('Quá hạn 2 ngày');
    });

    test('within 7 days (future) → weekday + day/month', () => {
        const due = NOW_MS + (5 * ONE_DAY);
        const out = formatDueRelative({dueMs: due, locale: 'en', nowMs: NOW_MS});

        // Shape only — Intl renders month-before-day ("Fri, Jun 26") on en;
        // assert the weekday prefix, a day number, and a short month name.
        expect(out).toMatch(/^[A-Z][a-z]{2}, /);
        expect(out).toMatch(/\b\d{1,2}\b/);
        expect(out).toMatch(/[A-Z][a-z]{2}/);
    });

    test('different year → includes the year', () => {
        const due = Date.UTC(2027, 5, 21, 10, 0);
        const out = formatDueRelative({dueMs: due, locale: 'en', nowMs: NOW_MS});
        expect(out).toContain('2027');
    });

    test('Intl-throws → ISO fallback (never throws)', () => {
        const due = NOW_MS + (30 * ONE_DAY); // far enough to skip the dayDiff branches
        const originalDTF = Intl.DateTimeFormat;

        // Force Intl.DateTimeFormat to throw so the catch returns the ISO
        // fallback. (Bogus locale names don't reliably throw — every string is
        // accepted as a locale tag — so we monkey-patch the constructor.)
        Intl.DateTimeFormat = function() {
            throw new Error('boom');
        } as unknown as typeof Intl.DateTimeFormat;
        try {
            const out = formatDueRelative({dueMs: due, locale: 'en', nowMs: NOW_MS});
            expect(out).toMatch(/^\d{4}-\d{2}-\d{2}T/);
        } finally {
            Intl.DateTimeFormat = originalDTF;
        }
    });
});
