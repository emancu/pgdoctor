-- name: TempUsage :one
-- Monitors temporary file creation indicating work_mem exhaustion
WITH temp_stats AS (
  SELECT
    datname::text AS database_name
    , temp_files
    , temp_bytes
    , stats_reset
    , EXTRACT(EPOCH FROM (NOW() - stats_reset)) AS seconds_since_reset
    , CASE
      WHEN EXTRACT(EPOCH FROM (NOW() - stats_reset)) > 0
        THEN temp_files::numeric / (EXTRACT(EPOCH FROM (NOW() - stats_reset)) / 3600)
      ELSE 0
    END AS temp_files_per_hour
    , CASE
      WHEN EXTRACT(EPOCH FROM (NOW() - stats_reset)) > 0
        THEN temp_bytes::numeric / (EXTRACT(EPOCH FROM (NOW() - stats_reset)) / 3600)
      ELSE 0
    END AS temp_bytes_per_hour
  FROM pg_stat_database
  WHERE datname = CURRENT_DATABASE()
)

, memory_settings AS (
  SELECT
    (
      SELECT setting FROM pg_settings
      WHERE name = 'work_mem'
    ) AS work_mem
    , (
      SELECT setting FROM pg_settings
      WHERE name = 'temp_file_limit'
    ) AS temp_file_limit
    , (
      SELECT setting FROM pg_settings
      WHERE name = 'log_temp_files'
    ) AS log_temp_files
    , (
      SELECT setting FROM pg_settings
      WHERE name = 'max_connections'
    ) AS max_connections
    , (
      SELECT setting FROM pg_settings
      WHERE name = 'shared_buffers'
    ) AS shared_buffers
)

SELECT
  ts.database_name
  , ts.temp_files
  , ts.temp_bytes
  , ts.stats_reset
  , ts.seconds_since_reset
  , ms.work_mem
  , ms.temp_file_limit
  , ms.log_temp_files
  , ms.max_connections
  , ms.shared_buffers
  , ROUND(ts.temp_files_per_hour::numeric, 2) AS temp_files_per_hour
  , ROUND(ts.temp_bytes_per_hour::numeric, 0) AS temp_bytes_per_hour
FROM temp_stats AS ts
CROSS JOIN memory_settings AS ms;
