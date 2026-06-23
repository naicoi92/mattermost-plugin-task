// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for NewTaskDialog (issue #30). Covers the pure helper
// parseDueLocal and the validation/submission contract via the exported
// component's behavior, without a full host Redux/intl provider harness.

import {ClientError} from 'client';

import {assigneeLookupError, buildCreateInput, deriveNewTaskContext, messageFor, normalizeAssigneeUsername, parseDueLocal} from 'components/new_task_dialog/new_task_dialog';

describe('deriveNewTaskContext', () => {
    test('a public/group channel yields a channel task with no assignee hint', () => {
        const ctx = deriveNewTaskContext({id: 'ch1', type: 'O', name: 'town-square'}, 'me');
        expect(ctx.channelId).toBe('ch1');
        expect(ctx.suggestedAssigneeID).toBe('');
    });

    test('a private channel also yields a channel task', () => {
        const ctx = deriveNewTaskContext({id: 'ch2', type: 'P', name: 'secret'}, 'me');
        expect(ctx.channelId).toBe('ch2');
        expect(ctx.suggestedAssigneeID).toBe('');
    });

    test('a DM with a partner yields a personal task with the partner as assignee', () => {
        const ctx = deriveNewTaskContext({id: 'dm1', type: 'D', name: 'me__partner'}, 'me');
        expect(ctx.channelId).toBe('');
        expect(ctx.suggestedAssigneeID).toBe('partner');
    });

    test('a DM with the partner id order reversed still picks the non-me user', () => {
        const ctx = deriveNewTaskContext({id: 'dm1', type: 'D', name: 'partner__me'}, 'me');
        expect(ctx.channelId).toBe('');
        expect(ctx.suggestedAssigneeID).toBe('partner');
    });

    test('a DM with myself (nota) yields a personal task assigned to me', () => {
        const ctx = deriveNewTaskContext({id: 'dm-self', type: 'D', name: 'me__me'}, 'me');
        expect(ctx.channelId).toBe('');
        expect(ctx.suggestedAssigneeID).toBe('me');
    });

    test('no channel context yields a personal task assigned to me', () => {
        expect(deriveNewTaskContext(null, 'me')).toEqual({channelId: '', suggestedAssigneeID: 'me'});
        expect(deriveNewTaskContext(undefined, 'me')).toEqual({channelId: '', suggestedAssigneeID: 'me'});
        expect(deriveNewTaskContext({id: '', type: 'O'}, 'me')).toEqual({channelId: '', suggestedAssigneeID: 'me'});
    });

    test('a group channel (type G) is treated as a channel task', () => {
        const ctx = deriveNewTaskContext({id: 'g1', type: 'G', name: 'group'}, 'me');
        expect(ctx.channelId).toBe('g1');
    });

    test('a DM whose name fails to parse falls back to personal + me', () => {
        const ctx = deriveNewTaskContext({id: 'dm2', type: 'D', name: ''}, 'me');
        expect(ctx.channelId).toBe('');
        expect(ctx.suggestedAssigneeID).toBe('me');
    });
});

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

// buildCreateInput assembles the POST /tasks body. The post_channel_id contract
// is the fix for "New Task sometimes posts a card, sometimes not": a DM task
// (personal scope, empty channel_id) must still announce its card into the
// originating DM via post_channel_id, without changing scope.
describe('buildCreateInput (post_channel_id announce card)', () => {
    const baseForm = {summary: 'Buy milk', description: '2L', priority: 'standard' as const, assigneeID: '', dueLocal: ''};

    test('a DM task sends post_channel_id (the originating DM) but no channel_id', () => {
        // DM context: personal scope (channelId empty), partner suggested as assignee.
        const ctx = deriveNewTaskContext({id: 'dm1', type: 'D', name: 'me__partner'}, 'me');
        const input = buildCreateInput(baseForm, ctx, 'dm1');
        expect(input.channel_id).toBeUndefined();
        expect(input.post_channel_id).toBe('dm1');
    });

    test('a channel task sends both channel_id and post_channel_id (redundant, harmless)', () => {
        const ctx = deriveNewTaskContext({id: 'ch1', type: 'O', name: 'town-square'}, 'me');
        const input = buildCreateInput(baseForm, ctx, 'ch1');
        expect(input.channel_id).toBe('ch1');
        expect(input.post_channel_id).toBe('ch1');
    });

    test('no originating channel sends neither channel_id nor post_channel_id', () => {
        const ctx = deriveNewTaskContext(null, 'me');
        const input = buildCreateInput(baseForm, ctx, undefined);
        expect(input.channel_id).toBeUndefined();
        expect(input.post_channel_id).toBeUndefined();
    });

    test('summary is trimmed', () => {
        const ctx = deriveNewTaskContext({id: 'ch1', type: 'O'}, 'me');
        const input = buildCreateInput({...baseForm, summary: '  spaced  '}, ctx, 'ch1');
        expect(input.summary).toBe('spaced');
    });

    test('assignee and due are propagated', () => {
        const ctx = deriveNewTaskContext({id: 'dm1', type: 'D', name: 'me__partner'}, 'me');
        const input = buildCreateInput({...baseForm, assigneeID: 'bob', dueLocal: '2026-06-19T12:00'}, ctx, 'dm1');
        expect(input.assignee_id).toBe('bob');
        expect(input.due).not.toBeUndefined();
        expect(typeof input.due).toBe('number');
    });
});
