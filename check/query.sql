-- name: InstalledExtensions :many
-- Lists the extensions installed in the current database, with their version.
-- Runner-level query, not owned by any single check: pgdoctor.Run() executes it
-- once per run and publishes the result on the context, so no check has to
-- discover extension availability for itself.
SELECT
  extname::text AS name
  , extversion::text AS version
FROM pg_catalog.pg_extension;
