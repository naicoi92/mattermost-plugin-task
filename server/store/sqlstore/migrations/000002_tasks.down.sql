-- 000002_tasks (down): drop the central task table.
-- FK CASCADE on parent_task_id and the child tables (members/reminders/posts/
-- comments/events, added later) ensures dependent rows are removed first, but
-- a plain DROP TABLE is safe regardless because SQLite/Postgres defer the
-- constraint check until the table itself is gone.
DROP TABLE IF EXISTS {{prefix}}tasks;
