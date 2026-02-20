# Sequence Health Check

Monitors PostgreSQL sequences for capacity issues that can cause production emergencies.

## Why This Matters

**Sequence exhaustion is a production emergency:**
- No advance warning (unless actively monitored)
- **ALL INSERT operations fail immediately**
- Cannot be fixed quickly (requires migration under pressure)
- Downtime required for int4 → bigint migration
- Migration complexity scales with table size and foreign key count

**Real-world scenario**: A high-traffic service exhausts its sequence overnight. Every new user signup fails. Emergency migration requires read-only mode for 2-4 hours during peak business hours. Revenue lost, customer trust damaged, team exhausted.

**Prevention cost**: 30 minutes of proactive migration
**Emergency fix cost**: 4-24 hours of downtime + customer impact + team stress

## What Are Sequences?

PostgreSQL sequences generate unique integers for ID columns (SERIAL, BIGSERIAL, IDENTITY):
- Each INSERT consumes the next value: 1, 2, 3, ...
- Integer (int4) sequences max out at **2,147,483,647**
- Bigint (int8) sequences max out at **9,223,372,036,854,775,807**

When a sequence reaches its maximum:
```
ERROR: nextval: reached maximum value of sequence "users_id_seq" (2147483647)
```

All INSERT operations fail until the sequence is migrated to bigint.

## Subchecks

### near-exhaustion

Identifies sequences approaching their maximum value:
- **FAIL**: Usage >90% (imminent failure, migrate immediately)
- **WARN**: Usage >75% (plan migration soon)

**Cyclic sequences** (rarely used) are skipped as they wrap around instead of failing.

### integer-columns

Identifies integer (int4) columns with sequences at >50% capacity:
- **FAIL**: Usage >75% (migrate within weeks)
- **WARN**: Usage >50% (plan migration within months)

Integer columns max at 2.1B. High-traffic tables can exhaust this surprisingly quickly:
- 10K inserts/day = 575 years to exhaustion (safe)
- 100K inserts/day = 58 years to exhaustion (probably safe)
- 1M inserts/day = 5.8 years to exhaustion (migrate soon!)
- 10M inserts/day = 215 days to exhaustion (critical!)

### type-mismatch

Identifies sequences that can generate values exceeding their column's capacity:
- **FAIL**: Sequence max > column type max

This occurs when a bigint sequence feeds an integer column. The sequence will eventually generate values too large for the column, causing INSERT failures.

### int4-pk-fk

Identifies integer (int4) primary key columns with foreign key references:
- **WARN**: Any int4 column that is a PK or has FK references

**Why this matters**: Migration complexity scales with FK count:
- 0 FKs: Simple migration (~10 seconds lock)
- 5 FKs: Coordinate 6 table migrations (5-10 minutes planning)
- 20 FKs: Complex coordination (days of planning, careful rollout)

Proactive migration (before capacity crisis) is exponentially easier.

## How to Fix

### For `near-exhaustion`

Migrate sequence to bigint immediately:

```sql
-- Simple migration (small tables <10M rows, brief lock acceptable)
BEGIN;
  ALTER TABLE users ALTER COLUMN id TYPE bigint;
  ALTER SEQUENCE users_id_seq AS bigint MAXVALUE 9223372036854775807;
COMMIT;

-- Restart connection pools to invalidate prepared statements
-- Restart connection pools to invalidate prepared statements
```

For large tables or zero-downtime requirements, see the "Migration Guide" section below.

### For `integer-columns`

Migrate integer columns to bigint before reaching 75% capacity:

```sql
-- Same migration as near-exhaustion
BEGIN;
  ALTER TABLE events ALTER COLUMN id TYPE bigint;
  ALTER SEQUENCE events_id_seq AS bigint MAXVALUE 9223372036854775807;
COMMIT;

-- Restart connection pools
-- Restart connection pools to invalidate prepared statements
```

**Time to migration based on insert rate:**
- 1M inserts/day: Migrate within 1 month of hitting 50%
- 100K inserts/day: Migrate within 6 months
- <10K inserts/day: Plan migration opportunistically

### For `type-mismatch`

Fix sequence bounds to match column type:

```sql
-- If sequence max > column max, migrate column first
ALTER TABLE orders ALTER COLUMN id TYPE bigint;

-- Then adjust sequence
ALTER SEQUENCE orders_id_seq AS bigint MAXVALUE 9223372036854775807;

-- Restart connection pools
-- Restart connection pools to invalidate prepared statements
```

## Decision Tree: Which Issue to Fix First?

```
CRITICAL (Migrate immediately - days to failure):
├─► near-exhaustion >90%
└─► integer-columns >75% with >1M inserts/day

HIGH PRIORITY (Plan migration - weeks to months):
├─► near-exhaustion >75%
├─► integer-columns >50% with >100K inserts/day
└─► int4-pk-fk with >10 foreign key references

MEDIUM PRIORITY (Plan proactively - months to years):
├─► integer-columns >50% with <100K inserts/day
├─► int4-pk-fk with 1-10 foreign key references
└─► type-mismatch (fix before it becomes critical)

LOW PRIORITY (Monitor):
└─► All other sequences <50% usage
```

## Time to Exhaustion Calculator

Estimate when a sequence will exhaust based on recent growth:

```sql
-- Find sequences with growth rate
SELECT
  schemaname || '.' || sequencename AS sequence,
  last_value,
  max_value,
  max_value - last_value AS remaining,
  -- Rough estimate (assumes linear growth)
  CASE
    WHEN last_value > 1000
    THEN ROUND((max_value - last_value) / (last_value / 30.0)) -- ~30 days of history
    ELSE NULL
  END AS estimated_days_remaining
FROM pg_sequences
WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
  AND NOT cycle
ORDER BY estimated_days_remaining NULLS LAST;
```

**Action triggers:**
- <30 days remaining: **CRITICAL** - migrate immediately
- <90 days remaining: **HIGH** - plan migration this sprint
- <365 days remaining: **MEDIUM** - plan migration this quarter

## Prevention

### For New Tables

Always use bigint for auto-incrementing PKs:

```sql
-- Recommended: IDENTITY syntax (SQL standard, ORM-compatible)
CREATE TABLE users (
  id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  provider_id bigint NOT NULL,
  ...
);

-- Legacy: SERIAL syntax (still works, avoid for new tables)
CREATE TABLE events (
  id bigserial PRIMARY KEY,  -- Creates bigint + sequence
  ...
);
```

### Monitor Growth Rates

Track inserts/day to predict exhaustion:

```sql
-- Estimate daily insert rate
SELECT
  schemaname || '.' || tablename AS table,
  COALESCE(n_tup_ins / NULLIF(EXTRACT(EPOCH FROM (NOW() - stats_reset)) / 86400, 0), 0)
    AS inserts_per_day
FROM pg_stat_user_tables
ORDER BY inserts_per_day DESC;
```

## Migration Guide

### Simple Migration (Small Tables, Can Tolerate Lock)

For tables <10M rows where brief downtime is acceptable:

```sql
-- 1. Migrate column (acquires exclusive lock)
ALTER TABLE users ALTER COLUMN id TYPE bigint;

-- 2. Migrate sequence
ALTER SEQUENCE users_id_seq AS bigint MAXVALUE 9223372036854775807;

-- 3. Restart connection pools to invalidate prepared statements
-- Restart connection pools to invalidate prepared statements
```

**Lock duration**: ~1ms per 1,000 rows (10M rows ≈ 10 seconds)
**Blocked operations**: ALL reads and writes during lock

### Zero-Downtime Migration (Large Tables)

For tables >10M rows or 24/7 services:

**Option A: pg_repack** (recommended)
```bash
# Rebuilds table in background, minimal locking
pg_repack -t users -k --no-order
```

**Option B: New Column Strategy**
```sql
-- 1. Add new bigint column
ALTER TABLE users ADD COLUMN new_id bigint;

-- 2. Backfill in batches (no lock)
UPDATE users SET new_id = id WHERE new_id IS NULL LIMIT 10000;
-- Repeat until complete

-- 3. During maintenance window (quick):
BEGIN;
-- Set default
ALTER TABLE users ALTER COLUMN new_id
  SET DEFAULT nextval('users_new_id_seq');
-- Swap names
ALTER TABLE users RENAME COLUMN id TO id_old;
ALTER TABLE users RENAME COLUMN new_id TO id;
-- Update constraints
ALTER TABLE users DROP CONSTRAINT users_pkey;
ALTER TABLE users ADD PRIMARY KEY (id);
COMMIT;

-- 4. Drop old column after FK migration
ALTER TABLE users DROP COLUMN id_old;
```

**Option C: Logical Replication**
For critical systems, replicate to new schema with bigint columns, then swap.

### Critical: Update ALL Foreign Keys

When migrating a PK, you MUST migrate all referencing FKs:

```sql
-- Find all FK references
SELECT
  conrelid::regclass AS referencing_table,
  conname AS fk_constraint,
  a.attname AS fk_column
FROM pg_constraint c
JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
WHERE confrelid = 'users'::regclass
  AND contype = 'f';

-- Migrate each FK column
ALTER TABLE orders ALTER COLUMN user_id TYPE bigint;
ALTER TABLE sessions ALTER COLUMN user_id TYPE bigint;
-- ... repeat for all FKs
```

**Migration checklist:**
- [ ] Identify all FK references
- [ ] Test migration on staging
- [ ] Schedule maintenance window (or use zero-downtime strategy)
- [ ] Migrate PK and all FKs together
- [ ] Restart connection pools
- [ ] Verify inserts work
- [ ] Monitor for 24 hours

## Related Checks

Run these checks together for comprehensive schema health:

- **`pk-types`** - Validates PK types proactively (catches int4 PKs before they become urgent)
- **`toast-storage`** - Large value storage (unrelated but part of schema health cluster)

**Run all schema checks together:**
```bash
pgdoctor run --dsn "..." --only pk-types,sequence-health,toast-storage
```

## Common Questions

**Q: Can I just increase the sequence max without migrating to bigint?**
A: Only if the column is already bigint. If the column is int4, you MUST migrate the column type first, otherwise inserts will fail when the sequence exceeds 2.1B.

**Q: What about cyclic sequences?**
A: Cyclic sequences wrap around to the minimum value instead of failing. They're rarely used (mainly for rotating logs or token systems) and are skipped by this check.

**Q: Will migration invalidate prepared statements?**
A: Yes. Type changes invalidate prepared statements. You must restart connection pools after migration.

**Q: How long does the migration lock last?**
A: For `ALTER TABLE ... ALTER COLUMN TYPE bigint`, roughly 1ms per 1,000 rows:
- 100K rows ≈ 100ms
- 1M rows ≈ 1 second
- 10M rows ≈ 10 seconds
- 100M rows ≈ 100 seconds

For large tables, use zero-downtime strategies (pg_repack, new column, logical replication).
