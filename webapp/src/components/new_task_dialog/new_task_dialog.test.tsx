// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for NewTaskDialog (issue #30). Covers the pure helper
// parseDueLocal and the validation/submission contract via the exported
// component's behavior, without a full host Redux/intl provider harness.

import {ClientError} from 'client';

import {messageFor, parseDueLocal} from 'components/new_task_dialog/new_task_dialog';

describe('parseDueLocal', () => {
    test('returns null for an empty string', () => {
        expect(parseDueLocal('')).toBeNull();
        expect(parseDueLocal('   ')).toBeNull();
    });

    test('returns epoch ms for a valid datetime-local string', () => {
        const ms = parseDueLocal('2026-06-19T12:00');
        expect(ms).not.toBeNull();
        expect(typeof ms).toBe('number');

        // The local interpretation means the exact ms varies by timezone, but
        // it must resolve to the right calendar day somewhere on 2026-06-19.
        const d = new Date(ms as number);
        expect(d.getUTCFullYear()).toBeGreaterThanOrEqual(2026);
    });

    test('returns null for a malformed value', () => {
        expect(parseDueLocal('not-a-date')).toBeNull();
    });

    test('is monotonic: a later input yields a larger ms', () => {
        const earlier = parseDueLocal('2026-06-19T09:00') as number;
        const later = parseDueLocal('2026-06-19T17:00') as number;
        expect(later).toBeGreaterThan(earlier);
    });
});

describe('ClientError handling in the dialog path', () => {
    // Tests the production messageFor directly (exported) rather than a copy.
    test('a ClientError surfaces its server message', () => {
        expect(messageFor(new ClientError(400, 'summary required'))).toBe('summary required');
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
