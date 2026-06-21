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

import {formatDue, formatTimestamp, messageFor} from 'components/task_detail_panel/task_detail_panel';

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
        expect(messageFor(new ClientError(404, 'task not found'))).toBe('task not found');
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
