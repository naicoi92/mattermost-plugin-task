-- 000007_posts: card-post tracking (M2-4, design §3.4).
--
-- Tracks every post that renders a task's card. A separate table (not two
-- hard columns channel_post_id/dm_post_id on task_tasks) so the plugin can
-- post a card in a third location (e.g. a follower's DM) later without a
-- schema migration. `kind` distinguishes the post's role (channel / dm /
-- future kinds).
--
-- post_id is UNIQUE: a single post can't be the card for two tasks. The
-- FK cascade removes tracking rows when the task is deleted (the posts
-- themselves stay in Mattermost; best-effort DeletePost handles orphans).

CREATE TABLE {{prefix}}posts (
    id         VARCHAR(26) PRIMARY KEY,
    task_id    VARCHAR(26) NOT NULL,
    post_id    VARCHAR(26) NOT NULL,
    kind       VARCHAR(32) NOT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT fk_posts_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE,
    CONSTRAINT uq_posts_post UNIQUE (post_id)
);

{{createIndex (printf "%sposts_task_idx" (prefix)) (printf "%sposts" (prefix)) "task_id"}}
