-- name: DevIndexes :many
-- Identifies indexes created during development (prefixed with _dev).
-- These should either be promoted to permanent indexes or dropped.
SELECT
  (n.nspname || '.' || t.relname)::text AS table_name
  , i.relname::text AS index_name
  , pg_relation_size(i.oid) AS index_size_bytes
  , coalesce(s.idx_scan, 0) AS idx_scan
  , coalesce(s.idx_tup_read, 0) AS idx_tup_read
  , pg_get_indexdef(idx.indexrelid) AS indexdef
FROM pg_class AS i
INNER JOIN pg_index AS idx ON i.oid = idx.indexrelid
INNER JOIN pg_class AS t ON idx.indrelid = t.oid
INNER JOIN pg_namespace AS n ON t.relnamespace = n.oid
LEFT JOIN pg_stat_user_indexes AS s ON i.oid = s.indexrelid
WHERE
  i.relkind = 'i'
  AND idx.indisvalid
  AND i.relname LIKE '\_dev%'
  AND n.nspname = 'public'
ORDER BY
  coalesce(s.idx_scan, 0) DESC, pg_relation_size(i.oid) DESC;
