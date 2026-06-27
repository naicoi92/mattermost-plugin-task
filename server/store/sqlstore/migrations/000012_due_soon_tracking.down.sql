-- 000012_due_soon_tracking (down): drop the due-soon dedupe column added by
-- the matching up migration. Re-applying the up migration restores it as NULL.

ALTER TABLE {{prefix}}tasks DROP COLUMN last_due_soon_sent_at;
