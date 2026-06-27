// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// dueBand and its thresholds are shared between the webapp (due chip CSS class
// + emoji) and the Go server (card color + notification emoji prefix). Both
// implementations MUST return the same band for the same input — keep
// thresholds in sync with server/due_band.go (change due-color-and-scheduled-
// notify, design D1).

export type Band = "muted" | "warning" | "danger";

// A task is:
//   - muted   when due is more than WARNING_HOURS (72h) away, OR terminal, OR has no due
//   - warning when due is between DANGER_HOURS (24h) and WARNING_HOURS (72h) away
//   - danger  when due is less than DANGER_HOURS (24h) away (includes overdue)
export const DANGER_HOURS = 24;
export const WARNING_HOURS = 72;

const MS_PER_HOUR = 60 * 60 * 1000;
const DANGER_MS = DANGER_HOURS * MS_PER_HOUR;
const WARNING_MS = WARNING_HOURS * MS_PER_HOUR;

const TERMINAL_STATUSES = new Set(["done", "cancelled"]);

// dueBand returns the proximity band for a task's due date.
//   - dueMs falsy (0/undefined) → muted (no due date)
//   - terminal status → muted regardless of due
//   - delta = dueMs - nowMs; danger when delta < DANGER_MS, warning when
//     delta <= WARNING_MS, otherwise muted. Overdue (delta < 0) → danger.
export function dueBand(
	dueMs: number | undefined | null,
	nowMs: number,
	status: string,
): Band {
	if (!dueMs) {
		return "muted";
	}
	if (TERMINAL_STATUSES.has(status)) {
		return "muted";
	}
	const delta = dueMs - nowMs;
	if (delta < DANGER_MS) {
		return "danger";
	}
	if (delta <= WARNING_MS) {
		return "warning";
	}
	return "muted";
}
