-- name: PGVersion :one
SELECT
  current_setting('server_version_num')::integer / 10000 AS major
  , current_setting('server_version_num')::integer % 100 AS minor;
