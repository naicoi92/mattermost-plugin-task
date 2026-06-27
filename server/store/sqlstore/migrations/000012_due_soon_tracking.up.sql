-- 000012_due_soon_tracking: add a nullable dedupe column used by the daily
-- due-soon notification scheduler (change due-color-and-scheduled-notify,
-- design D6).
--
-- Each time the 8h-GMT+7 job sends a due-soon DM for a task (deadline within
-- the next 24h, open), it stamps last_due_soon_sent_at with now-ms. On the
-- next scan the job skips any task whose stamp already falls within the
-- current GMT+7 day, so a task gets at most one due-soon DM per calendar day
-- even after a plugin restart. NULL (the default) means "never sent".
--
-- BIGINT mirrors the other ms-UTC timestamp columns. Nullable so existing
-- rows backfill cleanly without a default. Separate from last_overdue_sent_at
-- because due-soon and overdue are different notifications with independent
-- dedupe windows (a task can transition due-soon → overdue across days).

ALTER TABLE {{prefix}}tasks ADD COLUMN last_due_soon_sent_at BIGINT;
