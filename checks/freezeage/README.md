# Freeze Age Check

Monitors PostgreSQL transaction ID age to prevent transaction ID wraparound issues.

## Background

PostgreSQL uses 32-bit transaction IDs that wrap around at approximately 2 billion.
To prevent data visibility issues, PostgreSQL must periodically "freeze" old transaction IDs during vacuum operations.
If the oldest unfrozen transaction ID gets too old, PostgreSQL will refuse new transactions to protect data integrity.

## Subchecks

### database-freeze-age
Checks the oldest unfrozen transaction ID age at the database level (`pg_database.datfrozenxid`).

**Thresholds:**
- Warning: Age > 500 million transactions
- Critical: Age > 1 billion transactions
- Emergency: Age > 1.5 billion (approaching shutdown threshold)

### table-freeze-age
Checks the oldest unfrozen transaction ID age at the table level (`pg_class.relfrozenxid`).
Tables can be vacuumed individually, so thresholds are slightly lower.

**Thresholds:**
- Warning: Age > 400 million transactions
- Critical: Age > 800 million transactions

## PostgreSQL Limits

- Transaction ID wraparound occurs at ~2 billion
- PostgreSQL will refuse new transactions when age approaches this limit
- Default `autovacuum_freeze_max_age` is 200 million
- Aggressive autovacuum kicks in when age exceeds `autovacuum_freeze_max_age`

## How to Fix

### For `database-freeze-age`

High transaction ID age indicates autovacuum is not keeping up with freezing old rows. PostgreSQL will SHUT DOWN when age approaches 2 billion to prevent data corruption.

**Immediate action:**
```sql
-- Run aggressive vacuum to freeze old tuples
VACUUM FREEZE database_name;

-- Or per-table for large databases
VACUUM FREEZE VERBOSE schema.large_table;
```

**Check for blocking transactions:**
```sql
SELECT pid, age(backend_xid), query
FROM pg_stat_activity
WHERE backend_xid IS NOT NULL
ORDER BY age(backend_xid) DESC;
```

**Long-term fixes:**
1. Ensure autovacuum is enabled and running
2. Lower `autovacuum_freeze_max_age` if possible
3. Increase `autovacuum_max_workers`
4. Check for long-running transactions blocking vacuum
5. Schedule manual VACUUM FREEZE during low-traffic periods

### For `table-freeze-age`

**Vacuum specific tables:**
```sql
VACUUM FREEZE VERBOSE schema.table_name;
```

**For very large tables:**
1. Run during low-traffic periods
2. Increase `maintenance_work_mem` temporarily
3. Use parallel vacuum (PostgreSQL 13+):
```sql
SET max_parallel_maintenance_workers = 4;
VACUUM FREEZE schema.large_table;
```

**Tune autovacuum for these tables:**
```sql
ALTER TABLE schema.table_name SET (
  autovacuum_freeze_max_age = 100000000,  -- Lower threshold
  autovacuum_freeze_table_age = 50000000   -- Freeze entire table earlier
);
```
