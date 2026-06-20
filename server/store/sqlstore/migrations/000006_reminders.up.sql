-- 000006_reminders: task reminders, multi-ready (M2-3, design §3.3).
--
-- Reminders live in their own table (not as columns on task_tasks) so the
-- per-minute scheduler job scans only this small table WHERE fired_at IS NULL
-- instead of touching every task. The schema is also ready for multi-reminder
-- per task (the MVP enforces one reminder per task at the application layer).
--
-- fired_at IS NULL means "pending"; the scheduler sets it when it fires the DM.
-- The index on fired_at makes the pending-scan cheap.
--
-- Constraints:
--   * CHECK (offset_ms >= 0): a negative offset has no semantic meaning (it
--     would make the reminder fire after the due time). Rejected at the DB so
--     a buggy caller can't persist nonsense.
--   * UNIQUE (task_id): enforces the one-reminder-per-task MVP invariant at
--     the DB. When multi-reminder is enabled, drop this constraint and let the
--     service layer manage the set.

CREATE TABLE {{prefix}}reminders (
    id         VARCHAR(26) PRIMARY KEY,
    task_id    VARCHAR(26) NOT NULL,
    offset_ms  BIGINT NOT NULL CHECK (offset_ms >= 0),
    fired_at   BIGINT DEFAULT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT fk_reminders_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE,
    CONSTRAINT uq_reminders_task UNIQUE (task_id)
);

{{createIndex (printf "%sreminders_pending_idx" (prefix)) (printf "%sreminders" (prefix)) "fired_at"}}
