-- name: ReplicationSlots :many
-- For PostgreSQL 17+: includes inactive_since, conflicting, invalidation_reason
SELECT
  slot_name
  , slot_type
  , plugin
  , database
  , active
  , active_pid
  , wal_status
  , safe_wal_size
  , temporary
  , conflicting
  , invalidation_reason
  , PG_WAL_LSN_DIFF(PG_CURRENT_WAL_LSN(), restart_lsn)::BIGINT AS restart_lsn_lag_bytes
  , PG_WAL_LSN_DIFF(PG_CURRENT_WAL_LSN(), confirmed_flush_lsn)::BIGINT AS confirmed_flush_lsn_lag_bytes
  , CASE
    WHEN active THEN NULL
    ELSE EXTRACT(EPOCH FROM (NOW() - inactive_since))::BIGINT
  END AS inactive_seconds

FROM pg_replication_slots
ORDER BY
  CASE
    WHEN NOT active THEN 1
    WHEN wal_status = 'lost' THEN 2
    WHEN wal_status = 'unreserved' THEN 3
    ELSE 4
  END
  , restart_lsn_lag_bytes DESC NULLS LAST;

-- name: ReplicationSlotsPG15 :many
-- For PostgreSQL 15/16: columns conflicting, invalidation_reason, inactive_since don't exist
SELECT
  slot_name
  , slot_type
  , plugin
  , database
  , active
  , active_pid
  , wal_status
  , safe_wal_size
  , temporary
  , NULL::BOOLEAN AS conflicting
  , NULL::TEXT AS invalidation_reason
  , PG_WAL_LSN_DIFF(PG_CURRENT_WAL_LSN(), restart_lsn)::BIGINT AS restart_lsn_lag_bytes
  , PG_WAL_LSN_DIFF(PG_CURRENT_WAL_LSN(), confirmed_flush_lsn)::BIGINT AS confirmed_flush_lsn_lag_bytes
  , NULL::BIGINT AS inactive_seconds

FROM pg_replication_slots
ORDER BY
  CASE
    WHEN NOT active THEN 1
    WHEN wal_status = 'lost' THEN 2
    WHEN wal_status = 'unreserved' THEN 3
    ELSE 4
  END
  , restart_lsn_lag_bytes DESC NULLS LAST;
