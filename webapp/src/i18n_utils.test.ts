// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Unit tests for the webapp i18n helper (issue #33). Pins down locale fallback,
// %s interpolation, missing-key handling, and the locale selector.
//
// Jest's moduleNameMapper routes every i18n/*.json import through
// tests/i18n_mock.json (a single shared object), so en and vi resolve to the
// same bundle under test. These tests therefore assert the helper's contract —
// fallback ordering, interpolation, missing-key behavior — against the known
// mock values rather than the real en/vi strings. The real bundles are
// validated separately by the i18n-extract parity check and by manual E2E.

import {formatMessage, activeLocaleSelector} from 'i18n_utils';

import type {GlobalState} from '@mattermost/types/store';

// KNOWN is the set of keys/values present in tests/i18n_mock.json, so the tests
// below don't hardcode magic strings scattered across the file.
const KNOWN = {
    title: {key: 'webapp.task.title', value: 'Tasks'},
    new: {key: 'webapp.task.new', value: 'New Task'},
    progress: {key: 'webapp.task.subtasks.progress', value: '%s/%s done'},
} as const;

describe('formatMessage locale resolution', () => {
    test('resolves a known key for the en locale', () => {
        expect(formatMessage(KNOWN.new.key, 'en')).toBe(KNOWN.new.value);
    });

    test('resolves a known key for the vi locale', () => {
        // Under the shared mock, vi == en; the real vi bundle is validated
        // separately. Here we assert the lookup path doesn't throw and returns
        // the bundle value.
        expect(formatMessage(KNOWN.new.key, 'vi')).toBe(KNOWN.new.value);
    });

    test('falls back to a bundle for an unknown locale', () => {
        expect(formatMessage(KNOWN.title.key, 'fr')).toBe(KNOWN.title.value);
    });

    test('falls back via the language part of a regional locale (vi-VN)', () => {
        expect(formatMessage(KNOWN.new.key, 'vi-VN')).toBe(KNOWN.new.value);
    });

    test('falls back via the language part for an unknown region (de-DE)', () => {
        expect(formatMessage(KNOWN.new.key, 'de-DE')).toBe(KNOWN.new.value);
    });
});

describe('formatMessage %s interpolation', () => {
    test('interpolates two %s arguments in order', () => {
        expect(formatMessage(KNOWN.progress.key, 'en', 3, 5)).toBe('3/5 done');
    });

    test('leaves extra placeholders intact when too few args are given', () => {
        expect(formatMessage(KNOWN.progress.key, 'en', 2)).toBe('2/%s done');
    });

    test('ignores surplus arguments', () => {
        expect(formatMessage(KNOWN.title.key, 'en', 'unused')).toBe(KNOWN.title.value);
    });

    test('stringifies a numeric argument', () => {
        expect(formatMessage(KNOWN.progress.key, 'en', 1, 4)).toBe('1/4 done');
    });
});

describe('formatMessage missing keys', () => {
    test('returns the id itself when the key is absent everywhere', () => {
        expect(formatMessage('no.such.key', 'en')).toBe('no.such.key');
    });

    test('returns the id for an unknown locale and unknown key', () => {
        expect(formatMessage('no.such.key', 'fr')).toBe('no.such.key');
    });
});

describe('activeLocaleSelector', () => {
    test('reads state.views.i18n.locale when present', () => {
        const state = {
            views: {i18n: {locale: 'vi'}},
        } as unknown as GlobalState;
        expect(activeLocaleSelector(state)).toBe('vi');
    });

    test('falls back to en when the i18n slice is absent', () => {
        const state = {} as GlobalState;
        expect(activeLocaleSelector(state)).toBe('en');
    });

    test('falls back to en when views is absent', () => {
        const state = {entities: {}} as unknown as GlobalState;
        expect(activeLocaleSelector(state)).toBe('en');
    });
});
