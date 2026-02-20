-- name: HighSeqScanTables :many
-- Identifies tables with excessive sequential scans relative to index scans.
-- Excludes: small tables, system schemas, tables with no indexes.
WITH table_indexes AS (
  SELECT
    idx.indrelid AS table_oid
    , count(*) AS index_count
  FROM pg_index AS idx
  WHERE idx.indisvalid
  GROUP BY idx.indrelid
)

SELECT
  (n.nspname || '.' || c.relname)::text AS table_name
  , coalesce(s.seq_scan, 0) AS seq_scan
  , coalesce(s.idx_scan, 0) AS idx_scan
  , CASE
    WHEN coalesce(s.idx_scan, 0) = 0 THEN NULL
    ELSE round(s.seq_scan::numeric / s.idx_scan, 2)
  END AS seq_to_idx_ratio
  , coalesce(s.n_live_tup, 0) AS estimated_rows
  , pg_relation_size(c.oid) AS table_size_bytes
  , coalesce(ti.index_count, 0) AS index_count
FROM pg_class AS c
INNER JOIN pg_namespace AS n ON c.relnamespace = n.oid
LEFT JOIN pg_stat_user_tables AS s ON c.oid = s.relid
LEFT JOIN table_indexes AS ti ON c.oid = ti.table_oid
WHERE
  c.relkind IN ('r', 'p')
  AND n.nspname = 'public'
  AND coalesce(s.n_live_tup, 0) > 10000
  AND coalesce(s.seq_scan, 0) > 100
ORDER BY
  coalesce(s.seq_scan, 0) DESC;
