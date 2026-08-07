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
  -- dealloc counts eviction events, not entries. Each event discards the
  -- least-recently-used 5% of max (USAGE_DEALLOC_PERCENT in
  -- pg_stat_statements.c), so the entries lost are dealloc * 0.05 * max.
  , i.dealloc AS eviction_events
  -- The counters are cumulative since this reset, so a rate needs it. Unlike
  -- pg_stat_database.stats_reset it is normally set, but it is nullable and a
  -- rate over an unknown window is not a rate.
  , i.stats_reset
  , EXTRACT(EPOCH FROM (NOW() - i.stats_reset))::double precision AS seconds_since_reset
FROM pg_stat_statements_info AS i;
