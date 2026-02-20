-- name: HasPgStatStatements :one
-- Checks if pg_stat_statements extension is installed.
SELECT EXISTS(
  SELECT 1 FROM pg_extension
  WHERE extname = 'pg_stat_statements'
);

-- name: PartitionedTablesWithKeys :many
-- Gets partitioned tables and their partition key column(s).
-- Pre-aggregates all partition statistics in a single CTE for better performance
-- compared to multiple correlated subqueries.
WITH partition_stats AS (
  -- Single aggregation of all partition metrics from child tables
  SELECT
    i.inhparent
    , COUNT(*)::bigint AS partition_count
    , COALESCE(SUM(pg_catalog.pg_total_relation_size(i.inhrelid)), 0)::bigint AS total_size_bytes
    , COALESCE(SUM(s.n_live_tup), 0)::bigint AS estimated_rows
    , COALESCE(SUM(s.seq_scan), 0)::bigint AS total_seq_scans
    , COALESCE(SUM(s.idx_scan), 0)::bigint AS total_idx_scans
  FROM pg_inherits AS i
  LEFT JOIN pg_stat_user_tables AS s ON i.inhrelid = s.relid
  GROUP BY i.inhparent
)

SELECT
  n.nspname::text AS schema_name
  , c.relname::text AS table_name
  , pt.partstrat::text AS partition_strategy
  -- Get partition key column names as comma-separated string
  , (
    SELECT STRING_AGG(a.attname, ',' ORDER BY k.n)
    FROM UNNEST(pt.partattrs) WITH ORDINALITY AS k (attnum, n)
    INNER JOIN pg_attribute AS a ON a.attrelid = c.oid AND k.attnum = a.attnum
    WHERE k.attnum > 0
  )::text AS partition_key_columns
  -- Check if partition key includes expressions (attnum = 0 means expression)
  , (SELECT BOOL_OR(k.attnum = 0) FROM UNNEST(pt.partattrs) AS k (attnum)) AS has_expression_key
  -- All partition metrics from pre-aggregated CTE
  , COALESCE(ps.partition_count, 0) AS partition_count
  , COALESCE(ps.total_size_bytes, 0) AS total_size_bytes
  , COALESCE(ps.estimated_rows, 0) AS estimated_rows
  , COALESCE(ps.total_seq_scans, 0) AS total_seq_scans
  , COALESCE(ps.total_idx_scans, 0) AS total_idx_scans
FROM pg_catalog.pg_class AS c
INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
INNER JOIN pg_partitioned_table AS pt ON c.oid = pt.partrelid
LEFT JOIN partition_stats AS ps ON c.oid = ps.inhparent
WHERE
  c.relkind = 'p'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast', 'pgpartman', 'debezium', 'cron')
ORDER BY ps.total_size_bytes DESC NULLS LAST;

-- name: QueryStatsFromStatStatements :many
-- Gets query statistics from pg_stat_statements for partition key analysis.
-- Returns queries with significant usage to check against partitioned tables.
SELECT
  queryid::bigint AS query_id
  , LEFT(REGEXP_REPLACE(query, '\s+', ' ', 'g'), 80)::text AS query
  , calls::bigint AS calls
  , total_exec_time::double precision AS total_exec_time
  , mean_exec_time::double precision AS mean_exec_time
  , rows::bigint AS rows_returned
FROM pg_stat_statements
WHERE
  calls > 10
  AND query NOT LIKE 'COPY%'
  AND query NOT LIKE 'SET %'
  AND query !~ '^(BEGIN|COMMIT|ROLLBACK|SAVEPOINT|PREPARE|DEALLOCATE)'
  AND query !~ '^(VACUUM|ANALYZE|REINDEX|CLUSTER)'
  AND query !~ '^(CREATE|DROP|ALTER|TRUNCATE)'
  AND (query ILIKE '%SELECT%' OR query ILIKE '%UPDATE%' OR query ILIKE '%DELETE%')
ORDER BY total_exec_time DESC
LIMIT 500;
