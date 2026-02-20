-- name: TableActivity :many
-- Retrieves table write activity metrics from pg_stat_user_tables
-- Used to identify high-churn tables and HOT update efficiency issues
SELECT
  schemaname
  , relname
  , n_tup_ins
  , n_tup_upd
  , n_tup_del
  , n_tup_hot_upd
  , n_live_tup
  , pg_table_size(relid) AS table_size_bytes
FROM pg_stat_user_tables
WHERE n_tup_ins + n_tup_upd + n_tup_del > 0
ORDER BY n_tup_ins + n_tup_upd + n_tup_del DESC;
