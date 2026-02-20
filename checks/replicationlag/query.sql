-- name: ReplicationLag :many
-- Monitors replication lag for both physical and logical replication streams.
-- Joins with pg_replication_slots to get authoritative replication type and slot information.
SELECT
  -- Consumer/replica identity
  sr.application_name::text AS application_name
  , sr.state::text AS state

  -- Replication type detection (authoritative: slot_type, fallback: sync_state presence)
  , CASE
    WHEN rs.slot_type IS NOT NULL THEN rs.slot_type
    WHEN sr.sync_state IS NOT NULL THEN 'physical'
    ELSE 'unknown'
  END::text AS replication_type

  -- Lag metrics (bytes) - cast to bigint to get native int64
  , COALESCE(PG_WAL_LSN_DIFF(PG_CURRENT_WAL_LSN(), sr.replay_lsn), 0)::bigint AS replay_lag_bytes

  -- Lag metrics (seconds) - cast to float8 (double precision) to get native float64
  , COALESCE(EXTRACT(EPOCH FROM sr.replay_lag), 0)::float8 AS replay_lag_seconds

  -- Associated replication slot (NULL if no slot)
  , rs.slot_name::text AS slot_name
  , rs.wal_status::text AS wal_status

FROM pg_stat_replication AS sr
LEFT JOIN pg_replication_slots AS rs ON sr.pid = rs.active_pid
ORDER BY
  EXTRACT(EPOCH FROM sr.replay_lag) DESC NULLS LAST
  , sr.application_name;
