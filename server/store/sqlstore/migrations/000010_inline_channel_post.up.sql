-- 000010_inline_channel_post: collapse the posts table into a single
-- channel_post_id column on task_tasks.
--
-- Under the all-channel model every task has exactly one card in its home
-- channel, so the generic task_posts table (designed for many card surfaces
-- keyed by kind) is redundant. This migration:
--   1. adds task_tasks.channel_post_id (NULL until backfilled);
--   2. backfills it from the single kind='channel' row per task in the posts
--      table (defensive coalesce for any stray kind='dm' row so no card is
--      lost during the transition);
--   3. drops the posts table.
--
-- The correlated UPDATEs use a scalar subquery over the fully-prefixed table
-- names (no table alias) so they run on both SQLite and Postgres.

ALTER TABLE {{prefix}}tasks ADD COLUMN channel_post_id VARCHAR(26);

UPDATE {{prefix}}tasks
SET channel_post_id = (
    SELECT post_id
    FROM {{prefix}}posts
    WHERE {{prefix}}posts.task_id = {{prefix}}tasks.id
      AND {{prefix}}posts.kind = 'channel'
);

-- Defensive: if a task somehow has no channel row but has a dm/share row,
-- keep its card pointer rather than orphaning it.
UPDATE {{prefix}}tasks
SET channel_post_id = (
    SELECT post_id
    FROM {{prefix}}posts
    WHERE {{prefix}}posts.task_id = {{prefix}}tasks.id
    ORDER BY {{prefix}}posts.created_at
    LIMIT 1
)
WHERE channel_post_id IS NULL
  AND EXISTS (
    SELECT 1 FROM {{prefix}}posts WHERE {{prefix}}posts.task_id = {{prefix}}tasks.id
  );

-- Unique partial index so the reverse lookup (post id -> task) stays correct
-- and a card post can't belong to two tasks. NULLs are excluded. Both SQLite
-- and Postgres support the partial-index WHERE clause.
CREATE UNIQUE INDEX IF NOT EXISTS {{prefix}}tasks_channel_post_uidx
    ON {{prefix}}tasks (channel_post_id)
    WHERE channel_post_id IS NOT NULL;

DROP INDEX IF EXISTS {{prefix}}posts_task_idx;
DROP TABLE {{prefix}}posts;
