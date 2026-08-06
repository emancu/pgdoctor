-- name: HasPgStatStatements :one
-- True only when pg_stat_statements can actually be read, not merely when it is
-- installed. CREATE EXTENSION succeeds without the library preloaded, and the
-- extension can be created in a schema outside search_path; in both cases the
-- pg_extension row exists but every read of the views raises an error, which would
-- skip this whole check instead of reporting the warning it already has for this.
--
-- The extension registers its GUCs only when its library initializes, so a
-- pg_stat_statements.max row means "loaded". to_regclass resolves through
-- search_path and returns NULL instead of erroring when the schema is not visible.
SELECT
  EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'pg_stat_statements')
  AND EXISTS(SELECT 1 FROM pg_settings WHERE name = 'pg_stat_statements.max')
  AND to_regclass('pg_stat_statements_info') IS NOT NULL;

-- name: HiddenQueryTextCount :one
-- Counts pg_stat_statements rows whose text the current role cannot read.
-- Only superusers and roles with pg_read_all_stats see other users' query
-- text; everyone else gets '<insufficient privilege>', which would silently
-- shrink the analyzed set and produce a confident PASS on partial data.
SELECT COUNT(*)::bigint
FROM pg_stat_statements
WHERE
  query = '<insufficient privilege>'
  AND dbid = (SELECT d.oid FROM pg_database AS d WHERE d.datname = current_database());

-- name: PartitionedTablesWithKeys :many
-- Gets partitioned tables and their partition key column(s).
-- Pre-aggregates all partition statistics in a single CTE for better performance
-- compared to multiple correlated subqueries.
WITH RECURSIVE relevant_parents AS (
  -- Restrict the size/stat aggregation below to the partitioned tables this
  -- check actually reports on. Without this, pg_total_relation_size() runs for
  -- every inherited relation in the database, including the excluded schemas.
  SELECT c.oid
  FROM pg_catalog.pg_class AS c
  INNER JOIN pg_catalog.pg_namespace AS n ON c.relnamespace = n.oid
  INNER JOIN pg_partitioned_table AS pt ON c.oid = pt.partrelid
  WHERE
    c.relkind = 'p'
    AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast', 'pgpartman', 'debezium', 'cron')
)

, descendants AS (
  -- Walk the whole partition tree, not just the direct children. An
  -- intermediate node of a sub-partitioned table is itself relkind = 'p', so it
  -- has no storage (pg_total_relation_size() returns 0) and no
  -- pg_stat_user_tables counters: stopping at depth one reports such a parent as
  -- 0 bytes with 0 scans.
  SELECT
    rp.oid AS root_oid
    , i.inhrelid AS relid
  FROM relevant_parents AS rp
  INNER JOIN pg_catalog.pg_inherits AS i ON i.inhparent = rp.oid

  UNION ALL

  SELECT
    d.root_oid
    , i.inhrelid
  FROM descendants AS d
  INNER JOIN pg_catalog.pg_inherits AS i ON i.inhparent = d.relid
)

, partition_stats AS (
  -- Single aggregation of all partition metrics from the leaf tables. Only
  -- leaves store rows, so intermediate partitioned nodes are excluded here and
  -- partition_count counts leaves.
  SELECT
    d.root_oid
    , COUNT(*)::bigint AS partition_count
    , COALESCE(SUM(pg_catalog.pg_total_relation_size(d.relid)), 0)::bigint AS total_size_bytes
    , COALESCE(SUM(s.n_live_tup), 0)::bigint AS estimated_rows
    , COALESCE(SUM(s.seq_scan), 0)::bigint AS total_seq_scans
    , COALESCE(SUM(s.idx_scan), 0)::bigint AS total_idx_scans
  FROM descendants AS d
  INNER JOIN pg_catalog.pg_class AS leaf ON d.relid = leaf.oid AND leaf.relkind <> 'p'
  LEFT JOIN pg_stat_user_tables AS s ON d.relid = s.relid
  GROUP BY d.root_oid
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
LEFT JOIN partition_stats AS ps ON c.oid = ps.root_oid
WHERE
  c.relkind = 'p'
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast', 'pgpartman', 'debezium', 'cron')
ORDER BY ps.total_size_bytes DESC NULLS LAST;

-- name: QueryStatsFromStatStatements :many
-- Gets query statistics from pg_stat_statements for partition key analysis.
-- Returns queries with significant usage to check against partitioned tables.
-- Samples both axes: a single "top 500 by total_exec_time" cut is biased towards
-- slow statements and systematically drops cheap high-frequency ones, which are
-- exactly where a missing partition filter compounds at scale. UNION deduplicates
-- the statements ranking on both axes, so the result is at most 1000 rows.
WITH candidates AS (
  SELECT
    queryid::bigint AS query_id
    , LOWER(REGEXP_REPLACE(query, '\s+', ' ', 'g'))::text AS query
    , calls::bigint AS calls
    , total_exec_time::double precision AS total_exec_time
    , mean_exec_time::double precision AS mean_exec_time
    , rows::bigint AS rows_returned
    -- Counters are cumulative since this reset, so the report has to say over
    -- what period. Available from pg_stat_statements 1.9, which the toplevel
    -- filter above already requires.
    , (SELECT i.stats_reset FROM pg_stat_statements_info AS i)::timestamptz AS stats_reset
  FROM pg_stat_statements
  WHERE
    dbid = (SELECT d.oid FROM pg_database AS d WHERE d.datname = current_database())
    AND toplevel
    AND calls > 10
    -- Only statements that scan the table can prune partitions, so match on the
    -- leading keyword. An INSERT routes each row to a partition by its key value
    -- and never prunes; matching '%UPDATE%' anywhere in the text used to accept
    -- every INSERT that carried an "updated_at" column. Anchoring here also
    -- excludes utility statements (COPY, SET, VACUUM, transaction control, DDL).
    AND query ~* '^\s*(WITH|SELECT|UPDATE|DELETE)\M'
    -- A CTE can still wrap an INSERT (WITH v AS (...) INSERT INTO ...), which the
    -- leading keyword alone does not catch. Excluding INSERT INTO anywhere also
    -- drops INSERT ... SELECT, whose target table is routed rather than pruned.
    AND query !~* '\minsert\s+into\M'
)

SELECT
  query_id
  , query
  , calls
  , total_exec_time
  , mean_exec_time
  , rows_returned
  , stats_reset
FROM (
  SELECT
    query_id
    , query
    , calls
    , total_exec_time
    , mean_exec_time
    , rows_returned
    , stats_reset
  FROM candidates
  ORDER BY total_exec_time DESC
  LIMIT 500
) AS by_exec_time

UNION

SELECT
  query_id
  , query
  , calls
  , total_exec_time
  , mean_exec_time
  , rows_returned
  , stats_reset
FROM (
  SELECT
    query_id
    , query
    , calls
    , total_exec_time
    , mean_exec_time
    , rows_returned
    , stats_reset
  FROM candidates
  ORDER BY calls DESC
  LIMIT 500
) AS by_calls

ORDER BY total_exec_time DESC;
