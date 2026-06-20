-- 000003_members (down): drop the membership table. FK ON DELETE CASCADE on
-- task_id guarantees no orphan member rows survive a parent task delete, so a
-- plain DROP TABLE is safe.
DROP TABLE IF EXISTS {{prefix}}task_members;
