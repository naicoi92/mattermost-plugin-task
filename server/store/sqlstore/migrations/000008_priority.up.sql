-- 000008_priority: add the priority column to task_tasks.
--
-- Priority mirrors the Mattermost message-priority enum
-- (developers.mattermost.com/integrate/reference/message-priority):
-- standard (the implicit default), important, urgent. The column is NOT NULL
-- with DEFAULT 'standard' so every existing task is upgraded in place without
-- a backfill step (postgres, mysql and sqlite all backfill the DEFAULT on
-- ADD COLUMN).
ALTER TABLE {{prefix}}tasks ADD COLUMN priority VARCHAR(16) NOT NULL DEFAULT 'standard';

-- A priority filter is common in the RHS (filter tabs), so index it. A single
-- non-composite index is enough because priority is always combined with the
-- scope-driven predicates (channel_id or members join) that already have their
-- own indexes; the planner picks the more selective one.
{{createIndex (printf "%stasks_priority_idx" (prefix)) (printf "%stasks" (prefix)) "priority"}}
