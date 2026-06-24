-- 000009_assignee_backfill: forward-repair data bug for the assignee-403 gap
-- (Problem 2, AC4).
--
-- Root cause (diagnosed deterministically by TestListComments_AssigneeAllowedViaAssignAction
-- in server/api_test.go): the assignee is authorized to view/comment a task via
-- CanUserViewTask == (userID == task.AssigneeID), and task.AssigneeID is loaded
-- from the role='assignee' task_members row in assembleTask. The current Create
-- and Assign paths always write that member row, so newly-assigned tasks are
-- fine. The gap is HISTORICAL data: tasks assigned by an older plugin build
-- (before SetAssignee/AddMember persisted the role='assignee' edge) that have an
-- audit 'assigned' event but NO role='assignee' member row. For those tasks the
-- assignee sees a 403 because assembleTask leaves task.AssigneeID empty.
--
-- This migration is forward-only data repair: for each task with no
-- role='assignee' member row, take the MOST RECENT 'assigned' event whose
-- to_value carries a user id AND that is NOT followed by any later
-- 'assigned'/'unassigned' event (i.e. the task is currently assigned), and
-- materialize the (task_id, to_value, 'assignee') edge. If the latest
-- assign-related event is 'unassigned' (task currently has no assignee), no row
-- is inserted — the member edge should be absent, matching the current state.
--
-- to_value for EventAssigned is the assignee user id (see server/task/service.go
-- Assign: ToValue = ptrString(newAssigneeID)); it is NOT invented here.
--
-- Idempotent via ON CONFLICT DO NOTHING (postgres + sqlite; the MVP runs those
-- two dialects only, see members.go MySQL note). Re-running on a healthy DB
-- with the member row already present is a no-op.
--
-- No new schema object is created, so the down migration is a no-op: a backfill
-- cannot be safely reversed (we cannot tell a backfilled edge from one written
-- by the live Assign path), and dropping a real assignee edge would break
-- currently-assigned tasks. See 000009_assignee_backfill.down.sql.

INSERT INTO {{prefix}}members (task_id, user_id, role, created_at)
SELECT e.task_id, e.to_value, 'assignee', e.created_at
FROM {{prefix}}events e
WHERE e.event_type = 'assigned'
  AND e.to_value IS NOT NULL
  AND e.to_value <> ''
  AND NOT EXISTS (
      SELECT 1 FROM {{prefix}}members m
      WHERE m.task_id = e.task_id AND m.role = 'assignee'
  )
  AND NOT EXISTS (
      SELECT 1 FROM {{prefix}}events e2
      WHERE e2.task_id = e.task_id
        AND e2.event_type IN ('assigned', 'unassigned')
        AND (e2.created_at > e.created_at
             OR (e2.created_at = e.created_at AND e2.id > e.id))
  )
ON CONFLICT DO NOTHING;