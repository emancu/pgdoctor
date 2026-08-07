-- name: QueryStatsCapacity :one
-- Reports how full the pg_stat_statements hash table is and how fast it is
-- discarding entries.
-- Counting through pg_stat_statements(false) skips the external query-text
-- file: reading that text is what makes the view expensive, and a count does
-- not need it.
-- Entries and pg_stat_statements.max are both cluster-wide, so neither is
-- filtered by database - filtering the count would compare a subset against a
-- whole-cluster capacity.
-- pg_stat_statements.max is read through pg_settings rather than
-- current_setting() so an unloaded library yields NULL instead of an error.
SELECT
  (SELECT COUNT(*) FROM pg_stat_statements(false))::bigint AS entries
  , (
    SELECT s.setting
    FROM pg_catalog.pg_settings AS s
    WHERE s.name = 'pg_stat_statements.max'
  )::bigint AS max_entries
  -- dealloc counts eviction events, not entries. entry_dealloc() in
  -- pg_stat_statements.c discards Max(10, max * USAGE_DEALLOC_PERCENT / 100)
  -- least-recently-used entries per event, so the entries lost are
  -- dealloc * Max(10, max * 5 / 100). The floor matters: pg_stat_statements.max
  -- bottoms out at 100, where 5% is 5 and the real batch is twice that.
  , i.dealloc AS eviction_events
  -- The counters are cumulative since this reset, so a rate needs it. Unlike
  -- pg_stat_database.stats_reset it is normally set, but it is nullable and a
  -- rate over an unknown window is not a rate.
  , i.stats_reset
  , EXTRACT(EPOCH FROM (NOW() - i.stats_reset))::double precision AS seconds_since_reset
FROM pg_stat_statements_info AS i;
