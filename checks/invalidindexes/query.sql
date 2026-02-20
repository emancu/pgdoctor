-- name: BrokenIndexes :many
SELECT
  tblclass.relname AS table_name
  , idxclass.relname AS index_name
FROM pg_index
INNER JOIN pg_class AS idxclass ON pg_index.indexrelid = idxclass.oid
INNER JOIN pg_class AS tblclass ON pg_index.indrelid = tblclass.oid
WHERE NOT pg_index.indisvalid;
