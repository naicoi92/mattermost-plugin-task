-- 000009_assignee_backfill (down): no-op. This migration only INSERTs data
-- (forward repair of historical tasks missing the role='assignee' member
-- edge); it creates no schema object to drop. Reversing it would require
-- deleting the assignee member row, but a row materialized by the backfill is
-- indistinguishable from one written by the live Assign path, so dropping it
-- would strip a currently-assigned task of its assignee edge and reintroduce
-- the assignee-403 bug. The backfill is intentionally forward-only.
SELECT 1;
