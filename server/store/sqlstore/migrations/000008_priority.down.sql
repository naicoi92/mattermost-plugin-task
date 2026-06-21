-- 000008_priority (down): drop the priority column. Reverses 000008_priority.up.
-- The index must be dropped FIRST: SQLite refuses DROP COLUMN on an indexed
-- column (the schema would reference a missing column), while postgres/mysql
-- cascade the index drop automatically. Dropping the index unconditionally
-- first works on all three dialects.
DROP INDEX IF EXISTS {{prefix}}tasks_priority_idx;
ALTER TABLE {{prefix}}tasks DROP COLUMN priority;
