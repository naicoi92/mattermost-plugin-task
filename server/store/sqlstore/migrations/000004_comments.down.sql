-- 000004_comments (down): drop the comment-mapping table. The actual posts
-- remain in Mattermost; only the task<->post mapping is removed.
DROP TABLE IF EXISTS {{prefix}}comments;
