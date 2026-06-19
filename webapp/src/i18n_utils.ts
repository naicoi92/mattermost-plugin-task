// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Webapp i18n helper (issue #33). The bundles are registered with the host via
// registerTranslations in index.tsx, so the host already localizes any string
// the plugin passes through the standard intl pipeline (formatMessage /
// FormattedMessage). This module provides the plugin's own lightweight lookup
// for the places that need a plain translated string (e.g. component titles
// computed outside JSX), and a React hook that resolves the active locale from
// the host Redux store so a locale change re-renders plugin strings.
//
// The bundles (webapp/i18n/en.json, vi.json) are the single source of truth:
// the Makefile copies them from assets/i18n/ (i18n-copy target), and they are
// shared verbatim with the server (#4).

import {useSelector} from 'react-redux';

import type {GlobalState} from '@mattermost/types/store';

import en from '../i18n/en.json';
import vi from '../i18n/vi.json';

// Bundles keyed by locale code. 'en' is the canonical fallback.
const BUNDLES: Record<string, Record<string, string>> = {
    en: en as Record<string, string>,
    vi: vi as Record<string, string>,
};

// Fallback chain: try the exact locale, then its language part (e.g. vi-VN →
// vi), then English. English always exists, so a missing key yields the English
// value (never the raw key id).
function bundleForLocale(locale: string): Record<string, string> {
    if (BUNDLES[locale]) {
        return BUNDLES[locale];
    }
    const lang = locale.split('-')[0];
    if (BUNDLES[lang]) {
        return BUNDLES[lang];
    }
    return BUNDLES.en;
}

// formatMessage interpolates %s placeholders in the translated string for `id`.
// When `id` is absent in the bundle it falls back to the English value, and if
// that is also missing it returns the id itself (so a missing key is visible,
// not a silent empty string). Accepts the active locale so callers can resolve
// it once from the store and reuse it.
export function formatMessage(id: string, locale: string, ...args: Array<string | number>): string {
    const bundle = bundleForLocale(locale);
    let template = bundle[id] ?? BUNDLES.en[id] ?? id;

    // Replace each %s in order with the corresponding argument. A count
    // mismatch leaves extra placeholders intact rather than crashing.
    for (const arg of args) {
        const idx = template.indexOf('%s');
        if (idx === -1) {
            break;
        }
        template = template.slice(0, idx) + String(arg) + template.slice(idx + 2);
    }
    return template;
}

// activeLocaleSelector reads the host's current locale from the Redux store.
// Mattermost stores it at state.views.i18n.locale; fall back to 'en' when the
// shape is unavailable (tests, SSR) so lookups never throw.
export function activeLocaleSelector(state: GlobalState): string {
    const views = (state as unknown as {views?: {i18n?: {locale?: string}}}).views;
    return views?.i18n?.locale ?? 'en';
}

// useFormatMessage returns a translate function bound to the active locale. The
// hook subscribes to locale changes, so components re-render when the user
// switches language. Usage:
//   const t = useFormatMessage();
//   return <h1>{t('task.title')}</h1>;
export function useFormatMessage(): (id: string, ...args: Array<string | number>) => string {
    const locale = useSelector(activeLocaleSelector);
    return (id: string, ...args: Array<string | number>) => formatMessage(id, locale, ...args);
}

// useActiveLocale returns the current locale string, for components that need it
// directly (e.g. to format a due date).
export function useActiveLocale(): string {
    return useSelector(activeLocaleSelector);
}
