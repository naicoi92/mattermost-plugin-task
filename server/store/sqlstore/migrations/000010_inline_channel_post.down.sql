-- 000010_inline_channel_post (down): restore the posts table and repopulate
-- it from task_tasks.channel_post_id, then drop the inline column.

CREATE TABLE {{prefix}}posts (
    id         VARCHAR(26) PRIMARY KEY,
    task_id    VARCHAR(26) NOT NULL,
    post_id    VARCHAR(26) NOT NULL,
    kind       VARCHAR(32) NOT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT fk_posts_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE,
    CONSTRAINT uq_posts_post UNIQUE (post_id),
    CONSTRAINT uq_posts_task_kind UNIQUE (task_id, kind)
);

{{createIndex (printf "%sposts_task_idx" (prefix)) (printf "%sposts" (prefix)) "task_id"}}

INSERT INTO {{prefix}}posts (id, task_id, post_id, kind, created_at)
SELECT
    -- synthesize a pseudo-ULID (26 chars) from the task id for reversibility;
    -- collision with a real ULID is acceptable in the rollback path.
    'rb' || substr(id, 3),
    id,
    channel_post_id,
    'channel',
    created_at
FROM {{prefix}}tasks
WHERE channel_post_id IS NOT NULL;

DROP INDEX IF EXISTS {{prefix}}tasks_channel_post_uidx;
ALTER TABLE {{prefix}}tasks DROP COLUMN channel_post_id;
