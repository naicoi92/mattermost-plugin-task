-- 000003_members: task membership / roles (M2-2).
--
-- Tracks who is related to a task and how: creator, assignee, and (future)
-- follower. Kept in its own table rather than as columns on task_tasks so the
-- schema is ready for multi-assignee / follower without a later migration.
--
-- The composite PK (task_id, user_id, role) — NOT (task_id, role) — is
-- deliberate: it allows several users to hold the same role on a task, which
-- is the future-proofing decision (SQL_MIGRATION_PLAN.md §3.2). The MVP
-- enforces one creator + one assignee per task at the application layer; if
-- multi-assignee is enabled later the schema needs no change. The same PK
-- also makes AddMember idempotent: re-inserting an existing edge is a no-op
-- (INSERT OR IGNORE / ON CONFLICT).
--
-- FK cascade: deleting a task row removes its members automatically; there is
-- no user-side FK because users live in the Mattermost server's user table
-- (cross-table FK into a server table is not feasible for a plugin).

CREATE TABLE {{prefix}}members (
    task_id    VARCHAR(26) NOT NULL,
    user_id    VARCHAR(26) NOT NULL,
    role       VARCHAR(32) NOT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT pk_members PRIMARY KEY (task_id, user_id, role),
    CONSTRAINT fk_members_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE
);

{{createIndex (printf "%smembers_user_idx" (prefix)) (printf "%smembers" (prefix)) "user_id, role"}}
{{createIndex (printf "%smembers_task_idx" (prefix)) (printf "%smembers" (prefix)) "task_id"}}
