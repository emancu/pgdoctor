# TOAST Storage Analysis Check

Analyzes PostgreSQL TOAST (The Oversized-Attribute Storage Technique) usage to identify storage inefficiencies, performance issues, and schema design problems.

## Why This Matters

TOAST storage issues compound over time and impact multiple aspects of your database:

- **Storage costs**: TOAST overhead can increase storage by 20-40% for tables with large values
- **Query performance**: Fetching TOASTed values requires additional I/O (2-10x slower than in-line values)
- **Backup/restore time**: Large TOAST tables significantly increase backup duration (hours → days for 1TB+ tables)
- **Maintenance overhead**: Autovacuum struggles with high TOAST churn, leading to bloat

**Real-world impact**: A service storing full API request/response logs in JSONB columns accumulates 500GB of TOAST data over 6 months. Backups take 8 hours instead of 2. Query latency increases from 50ms to 300ms. Moving old data to S3 reduces storage by 80% and improves backup time to 2.5 hours.

**Prevention cost**: 2-4 hours to implement data retention + external storage
**Reactive fix cost**: 24-48 hours of emergency optimization + application changes + data migration

## What is TOAST?

PostgreSQL uses TOAST to handle values that exceed the page size (~8KB):

**How TOAST works:**
1. Values larger than ~2KB are compressed using pglz or lz4 algorithms
2. If compressed value still exceeds ~2KB, it's stored out-of-line in a separate TOAST table
3. Main table row stores only a pointer (toast_pointer) to the TOAST data
4. When querying, PostgreSQL fetches TOAST data on-demand

**Storage strategies (per column):**

PostgreSQL offers four storage strategies that control how large values are handled:

- **`EXTENDED`** (default for text, jsonb, json, bytea)
  - Compress first using the configured algorithm (pglz or lz4)
  - If still >~2KB after compression, move to TOAST table
  - **Best for**: Most use cases—provides both compression and out-of-line storage
  - **Trade-off**: CPU cost for compression, but saves storage and I/O

- **`EXTERNAL`**
  - Store out-of-line in TOAST table WITHOUT compression
  - **Best for**: Pre-compressed data (images, gzipped files, videos)
  - **Trade-off**: Saves CPU (no compression), but uses more storage
  - **Use when**: Data is already compressed or incompressible

- **`MAIN`**
  - Compress but prefer to keep in main table (avoid TOAST)
  - Only goes to TOAST if absolutely necessary
  - **Best for**: Frequently accessed values <8KB
  - **Trade-off**: Larger main table pages, slower table scans
  - **Use when**: Most values fit after compression, avoid TOAST overhead

- **`PLAIN`**
  - No compression, no TOAST (must fit in main table)
  - **Used for**: Fixed-size types (integers, timestamps, booleans)
  - **Cannot be changed**: Not applicable to variable-length types

**Compression algorithms (PostgreSQL 14+):**

- **`default`** (no explicit setting)
  - Uses `pglz` for compatibility with older PostgreSQL versions
  - Applies to all columns with EXTENDED or MAIN storage
  - **Performance**: Slower compression/decompression
  - **Ratio**: 2-3x compression for typical data

- **`lz4`** (recommended for PostgreSQL 14+)
  - Modern, fast compression algorithm
  - **Performance**: 3-5x faster than pglz
  - **Ratio**: 2-4x compression (often better than pglz)
  - **Best for**: JSON, logs, text data
  - Set with: `ALTER TABLE t ALTER COLUMN c SET COMPRESSION lz4;`

- **`pglz`** (legacy)
  - Original PostgreSQL compression algorithm
  - **Performance**: Slower, CPU-intensive
  - **Ratio**: 2-3x compression
  - **When to use**: Rarely—only for compatibility with older PG versions

**What types use TOAST:**
- `text`, `varchar` (unlimited length)
- `jsonb`, `json`
- `bytea` (binary data)
- `array` types (if large)

## Subchecks

### toast-ratio

Identifies tables where TOAST storage dominates total table size:
- **FAIL**: TOAST >80% of total size (storage severely inefficient)
- **WARN**: TOAST >50% of total size (storage inefficient)

**Why critical**: When TOAST exceeds 50% of storage, it indicates:
- Schema design issues (large values that shouldn't be in the database)
- Query performance degradation (extra I/O for most rows)
- Backup inefficiency (backing up data that could be archived or externalized)

### large-toast

Identifies tables with absolute TOAST storage exceeding reasonable limits:
- **FAIL**: TOAST >100GB (major storage and backup impact)
- **WARN**: TOAST >10GB (significant storage cost)

**Why critical**: Large TOAST tables:
- Increase cloud storage costs ($10-30 per 100GB/month)
- Slow down backups exponentially (100GB TOAST ≈ 30-60 min backup time)
- Make schema migrations risky (VACUUM FULL on 100GB table = hours of downtime)

### toast-bloat

Identifies TOAST tables with excessive dead tuples:
- **FAIL**: Dead tuples >50% (critical bloat, immediate action)
- **WARN**: Dead tuples >30% (autovacuum not keeping up)

**Why critical**: TOAST table bloat occurs when:
- Rows are updated/deleted but TOAST data isn't vacuumed
- Autovacuum thresholds too conservative for large tables
- High update frequency on columns with TOASTed values

**Impact**: Dead tuples in TOAST waste storage and slow sequential scans of TOAST data.

### wide-columns

Identifies specific columns causing TOAST usage:
- **WARN**: JSONB columns with avg_width >5KB
- **WARN**: Any columns with avg_width >10KB

**Why important**: Identifying which columns cause TOAST helps target optimization:
- Extract frequently-queried fields from JSONB to separate columns
- Move large text/binary data to external storage (S3)
- Implement data retention policies for growing columns

### compression-algorithm

Identifies columns using suboptimal compression algorithms (PostgreSQL 14+ only):
- **WARN**: Columns using `default` (which means pglz) instead of explicit lz4 compression

**Why important**: When columns have no explicit compression setting, PostgreSQL uses `default`, which means the legacy pglz algorithm. LZ4 (available in PostgreSQL 14+) is significantly faster and often more effective.

**What "default" means**:
- If you haven't explicitly set compression with `ALTER COLUMN SET COMPRESSION`, the column uses `default`
- `default` = pglz algorithm for backward compatibility
- Applies to columns with `EXTENDED` or `MAIN` storage strategies

**Comparison** (see "Compression algorithms" section above for details):
- **default (pglz)**: Slower, 2-3x compression, legacy algorithm
- **lz4**: 3-5x faster, 2-4x compression, recommended for JSON/text/logs

**Impact of using default/pglz instead of lz4**:
- Wastes CPU cycles during TOAST operations (compression/decompression)
- May result in larger TOAST storage (pglz often compresses worse than lz4)
- Slower read/write performance for TOASTed values
- Especially noticeable on high-traffic tables with large JSON/text columns

**Storage strategy context**:
This check looks at columns with `EXTENDED` or `MAIN` storage (where compression applies).
- `EXTERNAL` storage doesn't use compression (by design, for pre-compressed data)
- `PLAIN` storage doesn't support compression (fixed-size types)

See "Storage strategies" section above for detailed explanations of each strategy.

**This subcheck only runs on PostgreSQL 14+** (where lz4 is available)

## How to Fix

### For `toast-ratio`

Tables with high TOAST ratio (>50%) indicate large values dominating storage. Solutions depend on data type:

**For JSONB columns:**
```sql
-- Extract frequently-accessed fields to dedicated columns
ALTER TABLE events ADD COLUMN event_type text
  GENERATED ALWAYS AS (payload->>'event_type') STORED;

ALTER TABLE events ADD COLUMN user_id bigint
  GENERATED ALWAYS AS ((payload->>'user_id')::bigint) STORED;

-- Create indexes on extracted columns
CREATE INDEX CONCURRENTLY ON events(event_type);
CREATE INDEX CONCURRENTLY ON events(user_id);
```

**For large text/binary data:**
```sql
-- Option 1: Move to separate table (hot/cold split)
CREATE TABLE document_content (
  document_id bigint PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
  content text COMPRESSION lz4
);

-- Option 2: Move to external storage (S3)
-- Store only S3 key reference
ALTER TABLE documents DROP COLUMN content;
ALTER TABLE documents ADD COLUMN content_s3_key text;
```

### For `large-toast`

Tables with absolute TOAST >10GB need data lifecycle management:

**Implement data retention:**
```sql
-- Step 1: Archive old data to S3
COPY (
  SELECT id, title, content, created_at
  FROM documents
  WHERE created_at < NOW() - INTERVAL '2 years'
) TO PROGRAM 'aws s3 cp - s3://archive-bucket/documents-2022.csv';

-- Step 2: Delete archived records
DELETE FROM documents WHERE created_at < NOW() - INTERVAL '2 years';

-- Step 3: Reclaim space
VACUUM documents;
```

### For `toast-bloat`

TOAST tables with >30% dead tuples need immediate vacuum and autovacuum tuning:

**Immediate fix:**
```sql
-- Manual vacuum to reclaim space
VACUUM schema.table_name;
```

**Permanent fix: Tune autovacuum for high TOAST churn:**
```sql
-- Make autovacuum more aggressive for this table
ALTER TABLE schema.table_name
  SET (autovacuum_vacuum_scale_factor = 0.05);  -- Vacuum at 5% dead tuples (default 20%)

-- For very large tables, use threshold instead of scale
ALTER TABLE schema.table_name
  SET (autovacuum_vacuum_threshold = 10000);  -- Vacuum after 10K dead tuples
```

**For severe bloat (>50%):**
```sql
-- VACUUM FULL rebuilds table, reclaims space
-- WARNING: Requires exclusive lock, plan maintenance window
VACUUM FULL schema.table_name;

-- Alternative: pg_repack (online rebuild, minimal locking)
pg_repack --table schema.table_name --no-order
```

### For `wide-columns`

Columns with large average widths (>5KB for JSONB, >10KB for text) need optimization:

**For JSONB columns:**
```sql
-- Strip nulls to save space
CREATE OR REPLACE FUNCTION strip_nulls_trigger()
RETURNS trigger AS $$
BEGIN
  NEW.payload = jsonb_strip_nulls(NEW.payload);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER strip_nulls_before_insert
  BEFORE INSERT OR UPDATE ON events
  FOR EACH ROW EXECUTE FUNCTION strip_nulls_trigger();

-- Extract hot fields to separate columns (see toast-ratio fix above)
```

**For large text columns:**
```sql
-- Split hot metadata from cold content (see large-toast fix above)
-- Or implement data retention (archive old data)
```

### For `compression-algorithm`

Columns using default/pglz compression should use lz4 (PostgreSQL 14+):

```sql
-- Change compression algorithm to lz4
ALTER TABLE events ALTER COLUMN payload SET COMPRESSION lz4;
ALTER TABLE logs ALTER COLUMN message SET COMPRESSION lz4;

-- Existing rows still use old compression
-- Force recompression (requires rewrite, use during maintenance window)
VACUUM FULL events;  -- Exclusive lock! Plan carefully.

-- Or use pg_repack for minimal locking
pg_repack --table events --no-order
```

**For already-compressed data (images, videos, gzipped content):**
```sql
-- Disable compression, use EXTERNAL storage
ALTER TABLE media ALTER COLUMN file_data SET STORAGE EXTERNAL;
-- Stores out-of-line without compression, saving CPU
```

## Decision Tree: Which Issue to Fix First?

```
CRITICAL (Fix immediately - hours of backup time or $$$ storage):
├─► large-toast >100GB (especially if backup time >6 hours)
├─► toast-bloat >50% (wasting storage, degrading performance)
└─► toast-ratio >80% with >50GB total size

HIGH PRIORITY (Plan fix within 2-4 weeks):
├─► large-toast >10GB with >100K inserts/day
├─► toast-bloat >30% (autovacuum issues)
├─► toast-ratio >50% with >10GB total size
└─► wide-columns with JSONB >10KB average

MEDIUM PRIORITY (Plan within quarter):
├─► large-toast 1-10GB (monitor growth rate)
├─► toast-ratio 30-50% (suboptimal but not critical)
├─► wide-columns with text >20KB average
└─► compression-algorithm using pglz (easy fix, performance improvement)

LOW PRIORITY (Monitor, optimize opportunistically):
└─► All other TOAST usage <30% ratio and <1GB absolute size
```

**Rule of thumb**: Prioritize by multiplication of (TOAST size in GB) × (growth rate). A 5GB table growing 1GB/week is higher priority than a 50GB table growing 100MB/week.

## Optimization Strategies

### For JSONB Columns

**Problem**: Large JSON documents (>5KB average) causing TOAST overhead.

**Solution 1: Extract frequently-accessed fields**
```sql
-- Before: Every query fetches entire JSONB document
SELECT * FROM events WHERE payload->>'event_type' = 'purchase';

-- After: Extract hot fields to dedicated columns
ALTER TABLE events ADD COLUMN event_type text
  GENERATED ALWAYS AS (payload->>'event_type') STORED;

ALTER TABLE events ADD COLUMN user_id bigint
  GENERATED ALWAYS AS ((payload->>'user_id')::bigint) STORED;

-- Now query uses indexed column, avoids TOAST fetch
SELECT * FROM events WHERE event_type = 'purchase';
CREATE INDEX CONCURRENTLY ON events(event_type);
```

**Solution 2: Strip nulls and unnecessary data**
```sql
-- JSONB stores nulls explicitly, wasting space
-- Strip nulls on insert/update
CREATE OR REPLACE FUNCTION strip_nulls_trigger()
RETURNS trigger AS $$
BEGIN
  NEW.payload = jsonb_strip_nulls(NEW.payload);
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER strip_nulls_before_insert
  BEFORE INSERT OR UPDATE ON events
  FOR EACH ROW EXECUTE FUNCTION strip_nulls_trigger();
```

**Solution 3: Partial indexes instead of full GIN**
```sql
-- Before: Full GIN index on entire JSONB (expensive, large)
CREATE INDEX ON events USING GIN (payload);

-- After: Partial index on specific paths
CREATE INDEX ON events((payload->>'status'))
  WHERE payload->>'status' IN ('pending', 'processing');

-- Index only commonly-queried values, ignore rare ones
```

### For Large Text/Binary Data

**Problem**: Storing full documents, logs, or binary files in database.

**Solution 1: Move to separate table**
```sql
-- Split hot metadata from cold content
-- Before: Single table with large content column
CREATE TABLE documents (
  id bigserial PRIMARY KEY,
  title text,
  created_at timestamptz,
  content text  -- Large, rarely accessed
);

-- After: Separate hot and cold data
CREATE TABLE documents (
  id bigserial PRIMARY KEY,
  title text,
  created_at timestamptz
  -- content moved out
);

CREATE TABLE document_content (
  document_id bigint PRIMARY KEY REFERENCES documents(id) ON DELETE CASCADE,
  content text COMPRESSION lz4
);

-- Query metadata without fetching content
SELECT id, title FROM documents WHERE created_at > NOW() - INTERVAL '7 days';

-- Fetch content only when needed
SELECT dc.content FROM documents d
JOIN document_content dc ON dc.document_id = d.id
WHERE d.id = 123;
```

**Solution 2: External storage (S3, CloudFront)**
```sql
-- Store only reference to external object
ALTER TABLE documents DROP COLUMN content;
ALTER TABLE documents ADD COLUMN content_s3_key text;
ALTER TABLE documents ADD COLUMN content_size_bytes bigint;

-- Application uploads to S3, stores key
INSERT INTO documents (title, content_s3_key, content_size_bytes)
VALUES ('Report.pdf', 'documents/2024/report-abc123.pdf', 5242880);

-- Fetch from S3 when needed (application-level)
```

**Solution 3: Implement data retention**
```sql
-- Archive old data to cold storage
-- Step 1: Identify old records
SELECT id, created_at, pg_column_size(content) AS size_bytes
FROM documents
WHERE created_at < NOW() - INTERVAL '2 years'
ORDER BY created_at;

-- Step 2: Export to archive (CSV, Parquet, S3)
COPY (
  SELECT id, title, content, created_at
  FROM documents
  WHERE created_at < NOW() - INTERVAL '2 years'
) TO PROGRAM 'aws s3 cp - s3://archive-bucket/documents-2022.csv';

-- Step 3: Delete archived records
DELETE FROM documents WHERE created_at < NOW() - INTERVAL '2 years';

-- Step 4: Reclaim space
VACUUM documents;
```

### PostgreSQL 14+ Compression Options

**Problem**: Default pglz compression is CPU-intensive and not very effective.

**Solution: Use LZ4 compression (PostgreSQL 14+)**
```sql
-- LZ4 is faster and compresses better than pglz for most workloads
ALTER TABLE events ALTER COLUMN payload SET COMPRESSION lz4;
ALTER TABLE logs ALTER COLUMN message SET COMPRESSION lz4;

-- Existing rows still use old compression
-- Force recompression (requires rewrite, use during maintenance window)
VACUUM FULL events;  -- Exclusive lock! Plan carefully.

-- Or use pg_repack for minimal locking
pg_repack --table events --no-order
```

**Compression comparison:**
| Algorithm | Speed         | Ratio       | Best For                      |
|-----------|---------------|-------------|-------------------------------|
| `pglz`    | Slow          | 2-3x        | Default, legacy compatibility |
| `lz4`     | Very fast     | 2-4x        | JSON, logs, text (recommended)|
| `none`    | N/A           | 1x          | Pre-compressed data (images)  |

**When to disable compression:**
```sql
-- For already-compressed data (images, videos, gzipped content)
ALTER TABLE media ALTER COLUMN file_data SET STORAGE EXTERNAL;
-- Stores out-of-line without compression, saving CPU
```

### For TOAST Bloat

**Problem**: Dead tuples accumulating in TOAST tables, wasting space.

**Immediate fix:**
```sql
-- Manual vacuum to reclaim space
VACUUM schema.table_name;

-- Check if autovacuum is running
SELECT schemaname, relname, last_autovacuum, last_vacuum
FROM pg_stat_user_tables
WHERE schemaname || '.' || relname = 'schema.table_name';
```

**Permanent fix: Tune autovacuum**
```sql
-- For tables with high TOAST churn, make autovacuum more aggressive
ALTER TABLE schema.table_name
  SET (autovacuum_vacuum_scale_factor = 0.05);  -- Vacuum at 5% dead tuples (default 20%)

-- For very large tables, use threshold instead of scale
ALTER TABLE schema.table_name
  SET (autovacuum_vacuum_threshold = 10000);  -- Vacuum after 10K dead tuples

-- Monitor improvement
SELECT schemaname, relname, n_dead_tup, last_autovacuum
FROM pg_stat_user_tables
WHERE schemaname || '.' || relname = 'schema.table_name';
```

**For severe bloat (>50%):**
```sql
-- VACUUM FULL rebuilds table, reclaims space
-- WARNING: Requires exclusive lock, plan maintenance window
VACUUM FULL schema.table_name;

-- Alternative: pg_repack (online rebuild, minimal locking)
pg_repack --table schema.table_name --no-order
```

## Related Checks

Run these checks together for comprehensive storage health:

- **`vacuum-settings`** - Autovacuum configuration affects TOAST cleanup
- **`pk-types`** - Validates primary key types
- **`sequence-health`** - Sequence capacity issues
- **`table-bloat`** - Dead tuples in main tables

**Run all storage checks together:**
```bash
pgdoctor run --dsn "..." --only pk-types,sequence-health,toast-storage,vacuum-settings
```

## Common Questions

**Q: What does "default" compression mean?**
A: `default` means no explicit compression algorithm has been set, so PostgreSQL uses pglz for backward compatibility.

Check what compression your columns are using:
```sql
-- PostgreSQL 14+: Check compression settings
SELECT
  attname AS column_name,
  CASE attcompression
    WHEN 'p' THEN 'pglz'
    WHEN 'l' THEN 'lz4'
    ELSE 'default (pglz)'
  END AS compression_algorithm,
  CASE attstorage
    WHEN 'p' THEN 'PLAIN'
    WHEN 'e' THEN 'EXTERNAL'
    WHEN 'x' THEN 'EXTENDED'
    WHEN 'm' THEN 'MAIN'
  END AS storage_strategy
FROM pg_attribute
WHERE attrelid = 'schema.table_name'::regclass
  AND attnum > 0
  AND NOT attisdropped
ORDER BY attnum;
```

To see actual compression in stored data:
```sql
-- Check compression of actual data values (PostgreSQL 14+)
SELECT attname, pg_column_compression(attname::regclass::text) AS actual_compression
FROM pg_attribute
WHERE attrelid = 'schema.table_name'::regclass AND attnum > 0;
```

**Q: What's the difference between EXTENDED and EXTERNAL storage?**
A: Both move large values to TOAST tables, but handle compression differently:
- **EXTENDED**: Compresses first, then TOASTs if still large (uses CPU, saves storage)
- **EXTERNAL**: TOASTs without compression (saves CPU, uses more storage)

Use EXTERNAL for pre-compressed data (images, gzipped files) to avoid wasting CPU on already-compressed data.

**Q: Should I move all large data to S3?**
A: It depends on access patterns:
- **Keep in DB**: Frequently accessed, transactional data, <1MB per row
- **Move to S3**: Rarely accessed, archival, >1MB per row, binary files (images, PDFs)
- **Hybrid**: Recent data in DB, old data in S3 with lifecycle rules

**Q: What's the performance impact of TOASTed values?**
A: Fetching TOASTed values requires additional I/O:
- In-line value: 1 page read
- TOASTed value: 1 page read (main table) + 1-100 page reads (TOAST table)
- **Impact**: 2-10x slower for `SELECT *` queries, no impact if column not selected

**Q: Does TOAST affect all queries?**
A: No. TOAST is fetched on-demand:
```sql
-- No TOAST fetch (payload not selected)
SELECT id, event_type FROM events WHERE user_id = 123;

-- TOAST fetch (payload selected)
SELECT * FROM events WHERE user_id = 123;
```

**Q: How do I monitor TOAST growth over time?**
A: Track TOAST size with a scheduled query:
```sql
-- Save to monitoring table
CREATE TABLE toast_size_history (
  recorded_at timestamptz DEFAULT NOW(),
  schema_name text,
  table_name text,
  toast_size_bytes bigint
);

-- Run daily
INSERT INTO toast_size_history (schema_name, table_name, toast_size_bytes)
SELECT schemaname, tablename, pg_relation_size(schemaname || '.' || tablename || '_toast')
FROM pg_tables
WHERE schemaname NOT IN ('pg_catalog', 'information_schema');

-- Analyze growth
SELECT table_name,
       toast_size_bytes / 1024^3 AS toast_gb,
       (toast_size_bytes - LAG(toast_size_bytes) OVER (PARTITION BY table_name ORDER BY recorded_at))
         / 1024^3 AS growth_gb
FROM toast_size_history
ORDER BY recorded_at DESC;
```

**Q: Can I prevent TOAST entirely?**
A: For small values, yes:
```sql
-- Use MAIN storage to avoid out-of-line storage
ALTER TABLE events ALTER COLUMN payload SET STORAGE MAIN;
-- Values up to ~8KB stay in-line (compressed)
-- Larger values still go to TOAST

-- Trade-off: Larger main table pages, slower updates
-- Only use if most values <8KB
```

**Q: What's the relationship between TOAST and table bloat?**
A: They're related but separate:
- **Table bloat**: Dead tuples in main table (fixed by VACUUM)
- **TOAST bloat**: Dead tuples in TOAST table (fixed by VACUUM on main table)
- Updating/deleting rows with TOASTed values creates bloat in BOTH tables
- Check autovacuum settings if both are bloated

**Q: Should I use JSONB or separate columns?**
A: Hybrid approach is usually best:
```sql
-- Hot fields: Separate columns (fast, indexed)
-- Cold fields: JSONB (flexible, rarely queried)
CREATE TABLE events (
  id bigserial PRIMARY KEY,
  event_type text NOT NULL,        -- Hot: frequently queried
  user_id bigint NOT NULL,          -- Hot: frequently queried
  metadata jsonb,                   -- Cold: rare queries, variable schema
  created_at timestamptz NOT NULL   -- Hot: frequently filtered
);

-- Fast queries on indexed columns
CREATE INDEX ON events(event_type, created_at);
-- Flexible schema for metadata
```
