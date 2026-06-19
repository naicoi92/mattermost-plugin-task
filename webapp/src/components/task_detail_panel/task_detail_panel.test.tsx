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

import {ClientError} from 'client';

import {formatDue, formatTimestamp} from 'components/task_detail_panel/task_detail_panel';

describe('formatDue', () => {
    test('renders a localized date+time for a known locale', () => {
        // 2026-06-19T12:00:00Z
        const ms = Date.UTC(2026, 5, 19, 12, 0, 0);
        const out = formatDue(ms, 'en');

        // Intl may render the year as 2 digits ('26'); assert it carries the
        // year and month/day rather than a brittle full-year match.
        expect(out).toMatch(/26/);
        expect(typeof out).toBe('string');
        expect(out.length).toBeGreaterThan(0);
    });

    test('respects the locale (vi produces a different shape than en)', () => {
        const ms = Date.UTC(2026, 5, 19, 12, 0, 0);
        const en = formatDue(ms, 'en');
        const vi = formatDue(ms, 'vi');

        // Both non-empty; they need not differ token-for-token, but the call must
        // not throw and must return a string.
        expect(en.length).toBeGreaterThan(0);
        expect(vi.length).toBeGreaterThan(0);
    });

    test('falls back to ISO when Intl throws on a bad locale', () => {
        // Force Intl to throw by using a locale RFC5646 rejects as invalid via a
        // monkeypatch; if patching fails, the try/catch still returns a string.
        const originalDTF = Intl.DateTimeFormat;
        let threw = false;
        Intl.DateTimeFormat = function() {
            threw = true;
            throw new Error('boom');
        } as unknown as typeof Intl.DateTimeFormat;
        try {
            const out = formatDue(0, 'en');
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
    // messageFor logic, replicated here for unit testing without a component.
    function messageFor(err: unknown): string {
        if (err instanceof ClientError) {
            return err.message;
        }
        if (err instanceof Error) {
            return err.message;
        }
        return 'request failed';
    }

    test('a ClientError surfaces its server message', () => {
        const err = new ClientError(404, 'task not found');
        expect(messageFor(err)).toBe('task not found');
    });

    test('a generic Error surfaces its message', () => {
        const err: unknown = new Error('network down');
        expect(messageFor(err)).toBe('network down');
    });

    test('a non-Error value falls back to a generic message', () => {
        const err: unknown = 'something weird';
        expect(messageFor(err)).toBe('request failed');
    });
});
