-- 000004_comments: comment-as-thread mapping (M2-5, design §3.5).
--
-- A task's discussion lives in the Mattermost thread rooted at the task's card
-- post; each reply in that thread is a "comment". This table stores only the
-- (task_id, post_id) mapping so the plugin knows which thread replies belong
-- to which task — the comment content, reactions, edits, and notifications
-- all stay in Mattermost. This removes ~70% of comment logic from the plugin
-- and gives users native chat UX without leaving the channel.
--
-- author_id and created_at are snapshot columns: copied from the post at link
-- time so audit/sort still work even if the post is later deleted or edited.
-- (Content is intentionally NOT stored — it lives in the post.)
--
-- post_id is UNIQUE so a reply can't be linked to two tasks.

CREATE TABLE {{prefix}}comments (
    id         VARCHAR(26) PRIMARY KEY,
    task_id    VARCHAR(26) NOT NULL,
    post_id    VARCHAR(26) NOT NULL,
    author_id  VARCHAR(26) NOT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT fk_comments_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE,
    CONSTRAINT uq_comments_post UNIQUE (post_id)
);

{{createIndex (printf "%scomments_task_idx" (prefix)) (printf "%scomments" (prefix)) "task_id, created_at"}}
