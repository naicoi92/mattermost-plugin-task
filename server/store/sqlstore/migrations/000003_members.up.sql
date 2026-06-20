-- 000003_members: task membership / roles (M2-2).
--
-- Tracks who is related to a task and how: creator, assignee, and (future)
-- follower. Kept in its own table rather than as columns on task_tasks so the
-- schema is ready for multi-assignee / follower without a later migration.
-- The composite PK (task_id, user_id, role) also makes AddMember idempotent:
-- re-inserting the same edge is a no-op (INSERT OR IGNORE / ON CONFLICT).
--
-- FK cascade: deleting a task row removes its members automatically; there is
-- no user-side FK because users live in the Mattermost server's user table
-- (cross-table FK into a server table is not feasible for a plugin).

CREATE TABLE {{prefix}}task_members (
    task_id    VARCHAR(26) NOT NULL,
    user_id    VARCHAR(26) NOT NULL,
    role       VARCHAR(32) NOT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT pk_members PRIMARY KEY (task_id, user_id, role),
    CONSTRAINT fk_members_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}task_tasks(id) ON DELETE CASCADE
);

{{createIndex (printf "%smembers_user_idx" (prefix)) (printf "%stask_members" (prefix)) "user_id, role"}}
{{createIndex (printf "%smembers_task_idx" (prefix)) (printf "%stask_members" (prefix)) "task_id"}}
