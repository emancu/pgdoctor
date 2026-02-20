# UUID Type Validation

Validates that UUID columns use PostgreSQL's native `uuid` type instead of string types (`varchar`, `text`).

## Why This Matters

Storing UUIDs as strings wastes storage and degrades query performance:

**Storage overhead:**
- String UUID: 36 bytes (`'550e8400-e29b-41d4-a716-446655440000'`)
- Native UUID: 16 bytes (binary representation)
- **Waste**: 55% larger storage per value

**Performance impact:**
- String comparisons: Character-by-character scanning (slow)
- UUID comparisons: 16-byte binary comparison (fast, CPU-optimized)
- Index size: 2.25x larger indexes for string UUIDs
- Query optimizer: Poor statistics for string columns, better cardinality estimates for uuid

**Real-world example**: A 10M row table with 3 UUID columns stored as varchar(36):
- Wasted storage: 10M × 3 × 20 bytes = **600MB unnecessary storage**
- Index overhead: 3 indexes × 600MB = **1.8GB wasted on indexes**
- Query performance: 2-5x slower UUID lookups compared to native type

**Prevention cost**: 2 minutes (use uuid type from the start)
**Reactive fix cost**: 2-8 hours (migration + testing + deployment)

## Detection Criteria

The check identifies columns with **high confidence** they store UUIDs:
- Column name contains "uuid" (case-insensitive): `user_uuid`, `request_uuid`, `UUID`, `external_uuid`
- Column uses string type: `varchar`, `text`, `char`, `bpchar`

**Why name-based detection:**
- Developers consistently name UUID columns with "uuid" in the name
- Alternative (value inspection) would require table scans and is expensive
- False positives are rare—if column is named `*_uuid`, it should be UUID type

## Validation Threshold

- **FAIL**: Any column matching the pattern (all findings are actionable)

There are no warnings—if a column is named with "uuid" and uses a string type, it should be migrated.

## Native UUID Benefits

### Storage Efficiency

```sql
-- String UUID: 36 bytes
CREATE TABLE events (
  id varchar(36) PRIMARY KEY  -- '550e8400-e29b-41d4-a716-446655440000'
);

-- Native UUID: 16 bytes (55% smaller)
CREATE TABLE events (
  id uuid PRIMARY KEY  -- Stored as 16-byte binary
);

-- Savings on 10M rows: (36 - 16) × 10M = 200MB saved
-- Savings on index: Same 200MB saved on PK index
```

### Query Performance

```sql
-- String comparison (slow: character-by-character)
SELECT * FROM events WHERE user_uuid = '550e8400-e29b-41d4-a716-446655440000';
-- Index scan: 36-byte key comparison × rows scanned

-- UUID comparison (fast: 16-byte binary comparison)
SELECT * FROM events WHERE user_uuid = '550e8400-e29b-41d4-a716-446655440000'::uuid;
-- Index scan: 16-byte key comparison × rows scanned
-- 2-5x faster for point lookups, 5-10x faster for range scans
```

### Data Validation

```sql
-- String type: No validation, can store garbage
INSERT INTO events (user_uuid) VALUES ('not-a-valid-uuid');  -- Succeeds!

-- UUID type: Built-in validation
INSERT INTO events (user_uuid) VALUES ('not-a-valid-uuid');
-- ERROR: invalid input syntax for type uuid: "not-a-valid-uuid"

-- Valid UUIDs work fine
INSERT INTO events (user_uuid) VALUES ('550e8400-e29b-41d4-a716-446655440000'::uuid);  -- Success
```

### Query Optimizer

```sql
-- String type: Poor statistics
EXPLAIN SELECT * FROM events WHERE user_uuid = '550e8400-...';
-- Optimizer guesses selectivity based on string prefix statistics

-- UUID type: Accurate statistics
EXPLAIN SELECT * FROM events WHERE user_uuid = '550e8400-...'::uuid;
-- Optimizer uses UUID-specific statistics for better query plans
```

## How to Fix

### For `uuid-types`

All UUID columns stored as varchar/text must be migrated to native `uuid` type.

**Strategy 1: Direct migration (small tables <1M rows)**

```sql
-- Single transaction (acquires exclusive lock during conversion)
BEGIN;
  -- Convert string to UUID (validates all values!)
  ALTER TABLE events
    ALTER COLUMN user_uuid TYPE uuid USING user_uuid::uuid;
COMMIT;

-- Restart connection pools to invalidate prepared statements
-- Restart connection pools to invalidate prepared statements
```

**Lock duration:** ~1-5 seconds for type conversion

**Risks:**
- If any value is invalid UUID, migration fails
- Exclusive lock blocks reads and writes during conversion
- Test on staging first!

**Strategy 2: New column migration (large tables >1M rows, zero downtime)**

See the "Migration Guide" section below for detailed zero-downtime migration strategy.

## Migration Guide

### For New Columns (2 minutes)

Always use native uuid type:

```sql
-- Option 1: UUID with default random value
ALTER TABLE events
  ADD COLUMN request_uuid uuid DEFAULT gen_random_uuid();

-- Option 2: UUID without default (application generates)
ALTER TABLE events
  ADD COLUMN correlation_uuid uuid;

-- Option 3: UUIDv7 for time-ordered UUIDs (PostgreSQL 17+)
-- Better for B-tree index locality
CREATE EXTENSION IF NOT EXISTS pg_uuidv7;
ALTER TABLE events
  ADD COLUMN event_uuid uuid DEFAULT uuid_generate_v7();
```

### For Existing Columns (2-8 hours)

Migration depends on table size and downtime tolerance.

#### Strategy 1: Direct Migration (Small Tables <1M rows, Brief Lock OK)

**Lock duration**: ~1-5 seconds for type conversion

```sql
-- Single transaction (acquires exclusive lock during conversion)
BEGIN;

-- Convert string to UUID (validates all values!)
ALTER TABLE events
  ALTER COLUMN user_uuid TYPE uuid USING user_uuid::uuid;

COMMIT;

-- Restart connection pools to invalidate prepared statements
-- Restart connection pools to invalidate prepared statements
```

**Risks:**
- If any value is invalid UUID, migration fails
- Exclusive lock blocks reads and writes during conversion
- Test on staging first!

#### Strategy 2: New Column Migration (Large Tables >1M rows, Zero Downtime)

**Timeline**: 2-8 hours (testing + backfill + cutover + cleanup)

```sql
-- Step 1: Add new uuid column (non-blocking)
ALTER TABLE events ADD COLUMN user_uuid_new uuid;

-- Step 2: Backfill in batches (no locks, safe)
DO $$
DECLARE
  batch_size INT := 10000;
  affected INT;
BEGIN
  LOOP
    UPDATE events
    SET user_uuid_new = user_uuid::uuid
    WHERE user_uuid_new IS NULL
      AND user_uuid IS NOT NULL
    LIMIT batch_size;

    GET DIAGNOSTICS affected = ROW_COUNT;
    EXIT WHEN affected = 0;

    RAISE NOTICE 'Migrated % rows', affected;
    COMMIT;
    PERFORM pg_sleep(0.1);  -- Avoid vacuum pressure
  END LOOP;
END $$;

-- Step 3: Verify migration (critical!)
SELECT COUNT(*) FROM events WHERE user_uuid IS NOT NULL AND user_uuid_new IS NULL;
-- Should return 0

-- Step 4: Swap columns (brief lock, <1 second)
BEGIN;
  ALTER TABLE events RENAME COLUMN user_uuid TO user_uuid_deprecated;
  ALTER TABLE events RENAME COLUMN user_uuid_new TO user_uuid;
COMMIT;

-- Step 5: Update application to use user_uuid (now uuid type)
-- Deploy application changes

-- Step 6: Drop old column (after 24-48 hours of monitoring)
ALTER TABLE events DROP COLUMN user_uuid_deprecated;
```

#### Strategy 3: Handling Invalid Values

If you have invalid UUIDs in the data:

```sql
-- Find invalid UUIDs before migration
SELECT user_uuid, COUNT(*)
FROM events
WHERE user_uuid IS NOT NULL
  AND user_uuid !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
GROUP BY user_uuid;

-- Options for invalid values:
-- Option A: Clean them up first
UPDATE events
SET user_uuid = NULL
WHERE user_uuid !~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';

-- Option B: Use conditional casting
ALTER TABLE events
  ALTER COLUMN user_uuid TYPE uuid
  USING CASE
    WHEN user_uuid ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
    THEN user_uuid::uuid
    ELSE NULL
  END;
```

### Updating Application Code

Migration requires updating application code:

```python
# Before: String UUID
user_uuid = "550e8400-e29b-41d4-a716-446655440000"
cursor.execute("INSERT INTO events (user_uuid) VALUES (%s)", (user_uuid,))

# After: Native UUID (same code, database handles it)
user_uuid = "550e8400-e29b-41d4-a716-446655440000"
cursor.execute("INSERT INTO events (user_uuid) VALUES (%s)", (user_uuid,))
# Most database drivers automatically convert string to uuid

# Or use UUID objects (recommended)
import uuid
user_uuid = uuid.UUID("550e8400-e29b-41d4-a716-446655440000")
cursor.execute("INSERT INTO events (user_uuid) VALUES (%s)", (user_uuid,))
```

## Related Checks

Run these checks together for comprehensive schema health:

- **`pk-types`** - Validates primary key types
- **`sequence-health`** - Monitors sequence exhaustion
- **`toast-storage`** - Identifies large value storage issues

**Run all schema checks together:**
```bash
pgdoctor run --dsn "..." --only pk-types,uuid-types,sequence-health,toast-storage
```

## Common Questions

**Q: What about UUIDv1, UUIDv4, UUIDv7?**
A: All UUID versions use the same 16-byte native `uuid` type:
- **UUIDv1**: Time-based + MAC address (privacy concerns, predictable)
- **UUIDv4**: Random (most common, good for most use cases)
- **UUIDv7**: Time-ordered + random (PostgreSQL 17+, best for B-tree indexes)

**Recommendation**: Use UUIDv7 if available (PostgreSQL 17+), otherwise UUIDv4.

**Q: Can we store UUIDs without hyphens to save space?**
A: No need—native uuid type is already optimal:
- String with hyphens: 36 bytes (`'550e8400-e29b-41d4-a716-446655440000'`)
- String without hyphens: 32 bytes (`'550e8400e29b41d4a716446655440000'`)
- Native uuid: **16 bytes** (binary, most efficient)

Just use native uuid type and let PostgreSQL handle the storage format.

**Q: Will this break our API responses (JSON)?**
A: No—JSON serialization works the same:
```sql
-- String type: Returns as JSON string
SELECT json_build_object('user_uuid', user_uuid) FROM events;
-- {"user_uuid": "550e8400-e29b-41d4-a716-446655440000"}

-- UUID type: Also returns as JSON string (with hyphens)
SELECT json_build_object('user_uuid', user_uuid) FROM events;
-- {"user_uuid": "550e8400-e29b-41d4-a716-446655440000"}
```

Both produce identical JSON output. The difference is only in PostgreSQL's internal storage.

**Q: What about NULL values?**
A: Native uuid type handles NULLs the same as string types:
```sql
-- Both work fine
INSERT INTO events (user_uuid) VALUES (NULL);  -- String or UUID type
```

**Q: Can we use uuid for foreign keys?**
A: Yes—uuid is a first-class type suitable for primary and foreign keys:
```sql
CREATE TABLE users (
  id uuid DEFAULT gen_random_uuid() PRIMARY KEY
);

CREATE TABLE events (
  id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id)
);

-- Foreign key constraints work identically to bigint
```

**Q: What about GUIDs in other databases (SQL Server)?**
A: PostgreSQL's `uuid` type is compatible with GUIDs:
- **SQL Server**: UNIQUEIDENTIFIER (16 bytes, same as PostgreSQL uuid)
- **MySQL**: BINARY(16) or CHAR(36) (prefer BINARY(16) for efficiency)
- **Oracle**: RAW(16) (equivalent to PostgreSQL uuid)

Migration between databases preserves values when using native binary types.
