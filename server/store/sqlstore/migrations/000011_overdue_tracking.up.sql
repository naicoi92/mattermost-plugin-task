-- 000011_overdue_tracking: add a nullable dedupe column used by the daily
-- overdue notification scheduler (change notification-overdue-and-context,
-- design D2).
--
-- Each time the overdue job sends a DM for a task it stamps
-- last_overdue_sent_at with now-ms (UTC). On the next scan the job skips any
-- task whose stamp already falls within the current UTC day, so a task gets at
-- most one overdue DM per calendar day even if the scheduler restarts and
-- re-scans the same day. NULL (the default) means "never sent", so the first
-- scan after a task goes overdue always notifies.
--
-- BIGINT mirrors the other ms-UTC timestamp columns (due_at, created_at, ...).
-- Nullable so existing rows backfill cleanly without a default.

ALTER TABLE {{prefix}}tasks ADD COLUMN last_overdue_sent_at BIGINT;
