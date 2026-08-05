-- name: InstalledExtensions :many
-- Inventories every installed extension with its installed version, the version bundled on disk (default_version; NULL when the control file is absent), and the server version. Read-only, AccessShareLock on catalogs only, sub-ms, PG14-17.
SELECT
  e.extname::text AS extension_name
  , e.extversion::text AS installed_version
  , ae.default_version
  , current_setting('server_version_num')::int AS server_version_num
FROM pg_catalog.pg_extension AS e
LEFT JOIN pg_catalog.pg_available_extensions AS ae ON e.extname = ae.name
ORDER BY e.extname;
