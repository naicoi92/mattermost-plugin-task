-- 000008_priority (down): drop the priority column. Reverses 000008_priority.up.
-- DROP COLUMN is supported by postgres, mysql (8.0+) and sqlite (3.35+, well
-- past the plugin's minimum).
ALTER TABLE {{prefix}}tasks DROP COLUMN priority;
