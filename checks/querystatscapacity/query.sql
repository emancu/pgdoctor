-- name: QueryStatsCapacity :one
-- entries and pg_stat_statements.max are cluster-wide, so neither is filtered by
-- database. pg_stat_statements(false) skips the query-text file, which is what
-- makes the view expensive; a count does not need it. max is read through
-- pg_settings so an unloaded library yields NULL instead of an error.
WITH counters AS (
  SELECT
    (SELECT count(*) FROM pg_stat_statements(false))::bigint AS entries
    , (
      SELECT s.setting
      FROM pg_catalog.pg_settings AS s
      WHERE s.name = 'pg_stat_statements.max'
    )::bigint AS max_entries
    , i.dealloc AS eviction_events
    , i.stats_reset
    , extract(EPOCH FROM (now() - i.stats_reset)) AS window_seconds
  FROM pg_stat_statements_info AS i
)

-- dealloc counts events, not entries. entry_dealloc() discards
-- Max(10, max * USAGE_DEALLOC_PERCENT / 100) per event; the floor matters because
-- max bottoms out at 100, where 5% is 5 and the real batch is twice that.
, sized AS (
  SELECT
    c.entries
    , c.max_entries
    , c.eviction_events
    , c.stats_reset
    , c.window_seconds
    , greatest(10, c.max_entries * 5 / 100) AS entries_per_event
  FROM counters AS c
)

SELECT
  entries
  , max_entries
  , (eviction_events * entries_per_event)::bigint AS entries_discarded
  , stats_reset
  , window_seconds::double precision
  -- Hours to turn the table over once. NULL when nothing was evicted.
  , (CASE
    WHEN eviction_events > 0
      THEN max_entries * window_seconds / (eviction_events * entries_per_event * 3600)
  END)::double precision AS recycle_hours
  , (CASE
    WHEN max_entries IS NULL OR max_entries <= 0
      THEN 'pg_stat_statements.max is unreadable, so occupancy has no capacity to be a share of.'
  END)::text AS usage_skip_reason
  -- Below the window floor one eviction alone reaches the warn threshold, and
  -- pgdoctor's own availability probe can cause one. 0.5 is that threshold as
  -- turnover per day; 1.1 keeps a single event clear of it rather than on it.
  , (CASE
    WHEN max_entries IS NULL OR max_entries <= 0
      THEN 'pg_stat_statements.max is unreadable, so turnover has no capacity to be a share of.'
    WHEN stats_reset IS NULL
      THEN 'Counter window unknown, so the eviction rate cannot be computed.'
    WHEN window_seconds < greatest(
      3600, entries_per_event::numeric / max_entries / 0.5 * 86400 * 1.1
    )
      THEN 'Counters cover only '
        || CASE
          WHEN window_seconds < 3600 THEN round((window_seconds / 60)::numeric) || 'm'
          ELSE round((window_seconds / 3600)::numeric, 1) || 'h'
        END
        || ', too short to tell a rate from a single eviction.'
  END)::text AS rate_skip_reason
FROM sized;
