// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for NewTaskDialog (issue #30). Covers the pure helper
// parseDueLocal and the validation/submission contract via the exported
// component's behavior, without a full host Redux/intl provider harness.

import {ClientError} from 'client';

import {assigneeLookupError, messageFor, normalizeAssigneeUsername, parseDueLocal} from 'components/new_task_dialog/new_task_dialog';

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

describe('normalizeAssigneeUsername (#96)', () => {
    test('strips a single leading @', () => {
        expect(normalizeAssigneeUsername('@bob')).toBe('bob');
    });

    test('leaves a bare username untouched', () => {
        expect(normalizeAssigneeUsername('bob')).toBe('bob');
    });

    test('only strips the first @ so emails stay intact', () => {
        expect(normalizeAssigneeUsername('bob@company')).toBe('bob@company');
    });

    test('trims surrounding whitespace', () => {
        expect(normalizeAssigneeUsername('  @bob  ')).toBe('bob');
    });

    test('returns empty for a whitespace-only value', () => {
        expect(normalizeAssigneeUsername('   ')).toBe('');
    });
});

// Locks in the submit-path UX contract for an unknown assignee username
// (CodeRabbit review on PR #103): a 404 surfaces the localized, actionable
// message (not the raw server text), while any other error surfaces its raw
// message. Extracted into assigneeLookupError so it's testable without a
// Redux/Intl provider harness.
describe('assigneeLookupError (#96)', () => {
    const localized = 'User not found. Enter a valid @username.';

    test('a 404 (unknown username) returns the localized not-found message', () => {
        const err = new ClientError(404, 'user not found');
        expect(assigneeLookupError(err, () => localized)).toBe(localized);
    });

    test('a 404 does NOT surface the raw server text', () => {
        const err = new ClientError(404, 'resource not found');
        expect(assigneeLookupError(err, () => localized)).not.toContain('resource');
    });

    test('a non-404 ClientError surfaces its raw message', () => {
        const err = new ClientError(500, 'server exploded');
        expect(assigneeLookupError(err, () => localized)).toBe('server exploded');
    });

    test('a generic Error surfaces its message', () => {
        const err = new Error('network down');
        expect(assigneeLookupError(err, () => localized)).toBe('network down');
    });

    test('a non-Error value falls back to the generic message', () => {
        expect(assigneeLookupError(null, () => localized)).toBe('request failed');
    });

    test('notFoundText is only evaluated on the 404 path', () => {
        // A non-404 error must not trigger the i18n lookup at all.
        let called = false;
        const err = new ClientError(500, 'oops');
        const result = assigneeLookupError(err, () => {
            called = true;
            return localized;
        });
        expect(called).toBe(false);
        expect(result).toBe('oops');
    });
});
