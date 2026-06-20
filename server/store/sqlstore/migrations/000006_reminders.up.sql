-- 000006_reminders: task reminders, multi-ready (M2-3, design §3.3).
--
-- Reminders live in their own table (not as columns on task_tasks) so the
-- per-minute scheduler job scans only this small table WHERE fired_at IS NULL
-- instead of touching every task. The schema is also ready for multi-reminder
-- per task (the MVP enforces one reminder per task at the application layer).
--
-- fired_at IS NULL means "pending"; the scheduler sets it when it fires the DM.
-- The index on fired_at makes the pending-scan cheap.

CREATE TABLE {{prefix}}reminders (
    id         VARCHAR(26) PRIMARY KEY,
    task_id    VARCHAR(26) NOT NULL,
    offset_ms  BIGINT NOT NULL,
    fired_at   BIGINT DEFAULT NULL,
    created_at BIGINT NOT NULL,
    CONSTRAINT fk_reminders_task FOREIGN KEY (task_id)
        REFERENCES {{prefix}}tasks(id) ON DELETE CASCADE
);

{{createIndex (printf "%sreminders_pending_idx" (prefix)) (printf "%sreminders" (prefix)) "fired_at"}}
