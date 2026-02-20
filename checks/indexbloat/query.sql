-- name: IndexBloat :many
-- Balanced B-tree index bloat estimation using pg_stats column widths
-- Accuracy: ±15% (good enough for health checks, not precision measurement)
WITH index_info AS (
  SELECT
    n.nspname::text AS schemaname
    , t.relname::text AS tablename
    , i.relname::text AS indexname
    , t.oid AS table_oid
    , i.relpages AS actual_pages
    , i.reltuples
    , ix.indkey
    , CURRENT_SETTING('block_size')::int AS bs
    , COALESCE(
      SUBSTRING(ARRAY_TO_STRING(i.reloptions, ' ') FROM 'fillfactor=([0-9]+)')::int
      , 90
    ) AS fill_factor
  FROM pg_index AS ix
  INNER JOIN pg_class AS i ON ix.indexrelid = i.oid
  INNER JOIN pg_class AS t ON ix.indrelid = t.oid
  INNER JOIN pg_namespace AS n ON i.relnamespace = n.oid
  INNER JOIN pg_am AS am ON i.relam = am.oid
  WHERE
    am.amname = 'btree'
    AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
    AND i.relpages > 100  -- Skip tiny indexes (<800KB)
    AND ix.indisvalid
    AND i.reltuples > 0
)

, index_columns AS (
  SELECT
    ii.schemaname
    , ii.tablename
    , ii.indexname
    , ii.actual_pages
    , ii.reltuples
    , ii.fill_factor
    , ii.bs
    -- Sum avg_width from pg_stats for indexed columns
    -- Fallback to 24 bytes if no stats (reasonable for UUID/timestamp)
    , COALESCE(
      (
        SELECT SUM(COALESCE(s.avg_width, 8))
        FROM UNNEST(ii.indkey) WITH ORDINALITY AS u (attnum, ord)
        INNER JOIN pg_attribute AS a ON a.attrelid = ii.table_oid AND u.attnum = a.attnum
        LEFT JOIN pg_stats AS s
          ON
            s.schemaname = ii.schemaname
            AND s.tablename = ii.tablename
            AND a.attname = s.attname
        WHERE u.attnum > 0
      )
      , 24
    ) AS data_width
  FROM index_info AS ii
)

, bloat_calc AS (
  SELECT
    schemaname
    , tablename
    , indexname
    , actual_pages
    , reltuples
    , bs
    , data_width
    -- Index tuple size: ItemPointer(6) + info(2) + data ≈ 8 + data_width
    -- Simplified: skip per-column MAXALIGN, add ~20% padding estimate
    , CEIL((8 + data_width) * 1.2) AS tuple_size
    -- Usable space: block_size - PageHeader(24) - BTPageOpaque(16), apply fill_factor
    , FLOOR((bs - 40) * fill_factor / 100.0) AS usable_space
  FROM index_columns
)

, bloat_estimate AS (
  SELECT
    schemaname
    , tablename
    , indexname
    , actual_pages
    , bs
    -- Expected pages = ceil(tuples / (usable_space / (line_pointer(4) + tuple_size)))
    , GREATEST(1, CEIL(reltuples / FLOOR(usable_space / (4 + tuple_size))))::bigint AS est_pages
    , (actual_pages::bigint * bs) AS actual_bytes
  FROM bloat_calc
  WHERE tuple_size > 0 AND usable_space > (4 + tuple_size)
)

SELECT
  schemaname
  , tablename
  , indexname
  , actual_pages
  , est_pages
  , actual_bytes
  , ((actual_pages - est_pages)::bigint * bs) AS bloat_bytes
  , CASE
    WHEN actual_pages > 0 AND actual_pages > est_pages
      THEN ROUND(100.0 * (actual_pages - est_pages) / actual_pages, 1)
    ELSE 0
  END AS bloat_percent
FROM bloat_estimate
WHERE actual_pages > est_pages
ORDER BY bloat_percent DESC, bloat_bytes DESC;
