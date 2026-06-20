-- 000001_init (down): revert the bootstrap migration.
--
-- The up migration only ran "SELECT 1" and created no tables, so there is
-- nothing to drop here. morph will still remove the version row from
-- schema_migrations, which is the only observable effect.
SELECT 1;
