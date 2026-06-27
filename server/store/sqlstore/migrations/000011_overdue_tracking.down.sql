-- 000011_overdue_tracking (down): drop the overdue dedupe column added by the
-- matching up migration. Re-applying the up migration restores it as NULL.

ALTER TABLE {{prefix}}tasks DROP COLUMN last_overdue_sent_at;
