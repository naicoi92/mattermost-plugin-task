-- 000005_events: append-only audit trail (M2-6, design §3.6).
--
-- Every state transition on a task (create / status change / assign / due
-- change / delete / comment / reminder / subtask) writes exactly one row here,
-- in the SAME transaction as the change (M3-3 wires the atomicity). This gives
-- the plugin an audit trail: who changed what, when, and from/to what value.
--
-- from_value / to_value are JSON snapshots of the affected field (or the whole
-- task for created/deleted); NULL means "no prior" (create) or "no after"
-- (delete). Keeping them as TEXT (not JSONB) keeps the schema portable across
-- postgres/mysql/sqlite.
--
-- FK cascade: deleting a task removes its events. This is intentional for the
-- MVP hard-delete model; if a retention/soft-delete policy is added later the
-- cascade can be revisited.

CREATE TABLE {{prefix}}events (
    id          VARCHAR(26) PRIMARY KEY,
    task_id     VARCHAR(26) NOT NULL,
    actor_id    VARCHAR(26) NOT NULL,
    event_type  VARCHAR(64) NOT NULL,
    from_value  TEXT DEFAULT NULL,
    to_value    TEXT DEFAULT NULL,
    created_at  BIGINT NOT NULL,
    CONSTRAINT fk_events_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE
);

{{createIndex (printf "%sevents_task_idx" (prefix)) (printf "%sevents" (prefix)) "task_id, created_at DESC"}}
