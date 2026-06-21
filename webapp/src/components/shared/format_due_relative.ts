// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// formatDueRelative renders a due-date timestamp as a short, human-friendly
// relative string for the Quick List row and the Task Detail meta-table:
//
//   - same calendar day (local)   → "Today, 17:00"        (open or terminal)
//   - tomorrow (local)             → "Tomorrow"
//   - past + open (overdue)        → "3 days overdue"
//   - past + terminal (done/cancelled) → "Yesterday" / "Mon, 15 Jun"
//   - within 7 days                → "Fri, 20 Jun"
//   - otherwise this year          → "Mon, 15 Jun"
//   - different year               → "Mon, 15 Jun 2027"
//
// `nowMs` defaults to Date.now() but is a parameter so tests are deterministic.
// `isOverdue` is the caller's verdict (open + past); the helper doesn't re-
// derive the status rule so it stays a pure formatting function.
//
// Exported for unit testing; consumers import the default export.

const MS_PER_DAY = 24 * 60 * 60 * 1000;

export interface DueRelativeInput {

    // dueMs is the deadline timestamp in ms epoch.
    dueMs: number;

    // locale is the user's active locale, passed to Intl.DateTimeFormat.
    locale: string;

    // nowMs is the reference "now" timestamp; defaults to Date.now().
    nowMs?: number;

    // isOverdue flags the open+past case so the helper renders the overdue
    // phrasing instead of a past absolute date.
    isOverdue?: boolean;
}

// formatDueRelative is the entry point. It never throws: an Intl failure (e.g.
// an unsupported locale on an older runtime) falls back to an ISO string.
export default function formatDueRelative(input: DueRelativeInput): string {
    const {dueMs, locale, isOverdue} = input;
    const now = input.nowMs ?? Date.now();
    try {
        const due = new Date(dueMs);
        const today = startOfDay(new Date(now));
        const dueDay = startOfDay(due);
        const dayDiff = Math.round((dueDay.getTime() - today.getTime()) / MS_PER_DAY);

        if (isOverdue && dayDiff < 0) {
            const days = Math.abs(dayDiff);
            return formatOverdue(days, locale);
        }
        if (dayDiff === 0) {
            const time = new Intl.DateTimeFormat(locale, {hour: '2-digit', minute: '2-digit'}).format(due);
            return formatTodayWithTime(time, locale);
        }
        if (dayDiff === 1) {
            return formatTomorrow(locale);
        }
        if (dayDiff === -1) {
            return formatYesterday(locale);
        }
        if (dayDiff > 1 && dayDiff <= 7) {
            return new Intl.DateTimeFormat(locale, {weekday: 'short', day: 'numeric', month: 'short'}).format(due);
        }
        if (due.getFullYear() === today.getFullYear()) {
            return new Intl.DateTimeFormat(locale, {weekday: 'short', day: 'numeric', month: 'short'}).format(due);
        }
        return new Intl.DateTimeFormat(locale, {weekday: 'short', day: 'numeric', month: 'short', year: 'numeric'}).format(due);
    } catch {
        return new Date(dueMs).toISOString();
    }
}

// startOfDay returns a Date clamped to local midnight of the input date.
function startOfDay(d: Date): Date {
    return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

// formatOverdue returns "%s days overdue" (or the singular form) in the
// locale's language. We use a single-plural rule that reads naturally for
// Vietnamese (which has no plural form) and English.
function formatOverdue(days: number, locale: string): string {
    const lang = locale.split(/[-_]/)[0];
    if (lang === 'vi') {
        return `Quá hạn ${days} ngày`;
    }
    return `${days} day${days === 1 ? '' : 's'} overdue`;
}

function formatTodayWithTime(time: string, locale: string): string {
    const lang = locale.split(/[-_]/)[0];
    if (lang === 'vi') {
        return `Hôm nay, ${time}`;
    }
    return `Today, ${time}`;
}

function formatTomorrow(locale: string): string {
    const lang = locale.split(/[-_]/)[0];
    return lang === 'vi' ? 'Ngày mai' : 'Tomorrow';
}

function formatYesterday(locale: string): string {
    const lang = locale.split(/[-_]/)[0];
    return lang === 'vi' ? 'Hôm qua' : 'Yesterday';
}
