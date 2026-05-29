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

  -- Cluster-wide WAL retention budget as bytes; -1 means unlimited (passed through
  -- verbatim). max_slot_wal_keep_size reports unit 'MB' with setting as an integer
  -- count of MB, so multiply by 1 MiB. The -1 guard avoids misreading the sentinel
  -- as a size. (PG13+; cluster-wide GUC, so a single constant via CROSS JOIN.)
  , cap.max_slot_wal_keep_bytes AS max_slot_wal_keep_bytes

FROM pg_stat_replication AS sr
LEFT JOIN pg_replication_slots AS rs ON sr.pid = rs.active_pid
CROSS JOIN LATERAL (
  SELECT CASE
           WHEN s.setting = '-1' THEN -1::bigint
           ELSE s.setting::bigint * 1024 * 1024
         END AS max_slot_wal_keep_bytes
  FROM pg_settings AS s
  WHERE s.name = 'max_slot_wal_keep_size'
) AS cap
ORDER BY
  EXTRACT(EPOCH FROM sr.replay_lag) DESC NULLS LAST
  , sr.application_name;
