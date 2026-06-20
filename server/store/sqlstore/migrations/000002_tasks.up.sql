-- 000002_tasks: the central task entity table (M2-1).
--
-- Only core 1:1 attributes live here. Relations that are 1:N or have their
-- own lifecycle are split into their own tables (task_members, task_reminders,
-- task_posts, task_comments, task_events) added by later migrations.
--
-- Conventions (see docs/SQL_MIGRATION_PLAN.md §3):
--   * PK/FK are VARCHAR(26) ULIDs.
--   * Timestamps are BIGINT ms UTC (Mattermost core + Boards convention).
--   * parent_task_id is a self-FK so subtasks cascade on parent delete.
--   * channel_id = '' means a personal (non-channel-scoped) task.

CREATE TABLE {{prefix}}tasks (
    id             VARCHAR(26) PRIMARY KEY,
    summary        TEXT NOT NULL,
    description    TEXT NOT NULL DEFAULT '',
    channel_id     VARCHAR(26) NOT NULL DEFAULT '',
    parent_task_id VARCHAR(26) DEFAULT NULL,
    status         VARCHAR(32) NOT NULL DEFAULT 'todo',
    order_key      VARCHAR(64) NOT NULL,
    is_all_day     BOOLEAN NOT NULL DEFAULT FALSE,
    due_at         BIGINT DEFAULT NULL,
    completed_at   BIGINT DEFAULT NULL,
    cancelled_at   BIGINT DEFAULT NULL,
    created_at     BIGINT NOT NULL,
    updated_at     BIGINT NOT NULL,
    CONSTRAINT fk_tasks_parent FOREIGN KEY (parent_task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE
);

{{createIndex (printf "%stasks_channel_idx" (prefix))    (printf "%stasks" (prefix)) "channel_id, status"}}
{{createIndex (printf "%stasks_parent_idx" (prefix))     (printf "%stasks" (prefix)) "parent_task_id"}}
{{createIndex (printf "%stasks_order_key_idx" (prefix))  (printf "%stasks" (prefix)) "order_key"}}
{{createIndex (printf "%stasks_status_due_idx" (prefix)) (printf "%stasks" (prefix)) "status, due_at"}}
