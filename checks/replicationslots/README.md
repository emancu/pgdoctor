# Replication Slots Check

Verifies that PostgreSQL replication slots are healthy, active, and not lagging behind the write-ahead log (WAL).

## What It Checks

### invalid-slots

Detects replication slots that have been marked invalid by PostgreSQL (PG17+ only).

**Severity:** FAIL

**Threshold:** Any slot with an invalidation reason

**Why this matters:** Invalid slots indicate the slot is unusable and will never recover. The slot must be dropped and recreated. Common causes:
- Slot exceeded `max_slot_wal_keep_size` and lost required WAL
- Configuration changes broke slot requirements
- Replication protocol version mismatch

### lost-wal-slots

Detects slots where required WAL files have been removed or are no longer reserved.

**Severity:** FAIL

**Thresholds:**
- `wal_status = 'lost'` - WAL files were already removed
- `wal_status = 'unreserved'` - WAL files may be removed at any time

**Why this matters:** Slots with lost or unreserved WAL cannot catch up. The consumer must be recreated from a fresh backup. This is often caused by:
- Inactive slot holding back WAL cleanup for too long
- `max_slot_wal_keep_size` set too low for replication lag
- Disk space constraints forcing premature WAL removal

### conflicting-slots

Detects slots in a conflicting state (PG17+ only).

**Severity:** WARN

**Threshold:** `conflicting = true`

**Why this matters:** Conflicting slots indicate potential issues with the slot's requirements conflicting with database operations. This is typically a transient state but may indicate configuration problems.

### inactive-slots

Detects replication slots that are not actively consuming changes.

**Severity:** WARN

**Threshold:** `active = false`

**Why this matters:** Inactive slots prevent WAL cleanup, causing disk space to fill. Common causes:
- Subscriber application crashed or was decommissioned
- Network connectivity issues
- CDC pipeline stopped consuming changes
- Standby server disconnected

Inactive slots will eventually lead to disk exhaustion if not addressed.

### critical-lag

Detects slots with severe replication lag.

**Severity:** FAIL

**Threshold:** >= 5GB lag

**Why this matters:** Critical lag indicates consumers are severely behind and may never catch up. This can be caused by:
- Consumer processing bottlenecks
- Network bandwidth limitations
- Large bulk operations on publisher
- Insufficient consumer resources

At this level of lag, consider whether the consumer can realistically catch up or needs to be recreated.

### high-lag

Detects slots with elevated replication lag.

**Severity:** WARN

**Threshold:** >= 1GB and < 5GB lag

**Why this matters:** High lag indicates consumers are falling behind. While not yet critical, this should be investigated to prevent escalation. Monitor consumer health and processing rates.

## PostgreSQL Version Compatibility

This check supports PostgreSQL 15+. Some features require PostgreSQL 17:

| Feature | PG 15/16 | PG 17+ |
|---------|----------|--------|
| Inactive slots detection | ✓ | ✓ |
| Lost/unreserved WAL | ✓ | ✓ |
| Lag threshold monitoring | ✓ | ✓ |
| Invalid slots (with reason) | ✗ | ✓ |
| Conflicting slots | ✗ | ✓ |
| Inactive duration display | ✗ | ✓ |

## Why it matters

Unhealthy replication slots cause serious problems:
- **Disk space exhaustion**: Inactive slots prevent WAL cleanup, filling disk
- **Replication lag**: Stale slots indicate broken replication
- **System instability**: WAL accumulation can cause database crashes
- **Data loss risk**: Invalid slots may lose replication state

Common scenarios:
- Logical replication subscribers that crashed or were decommissioned
- CDC pipelines that stopped consuming changes
- Standby servers disconnected for extended periods

## How to Fix

### For `invalid-slots`

Invalid slots must be dropped and recreated:

1. **Identify the invalid slot:**
   ```sql
   SELECT slot_name, invalidation_reason
   FROM pg_replication_slots
   WHERE invalidation_reason IS NOT NULL;
   ```

2. **Drop the slot** (it's unusable):
   ```sql
   SELECT pg_drop_replication_slot('slot_name');
   ```

3. **Recreate the consumer** from a fresh backup with a new slot.

### For `lost-wal-slots`

Slots with lost or unreserved WAL cannot recover:

1. **Identify affected slots:**
   ```sql
   SELECT slot_name, wal_status
   FROM pg_replication_slots
   WHERE wal_status IN ('lost', 'unreserved');
   ```

2. **Drop the slot** (cannot catch up):
   ```sql
   SELECT pg_drop_replication_slot('slot_name');
   ```

3. **Recreate the consumer** from a fresh backup.

**Prevention:**
- Increase `max_slot_wal_keep_size` if slots frequently lose WAL
- Monitor slot lag proactively
- Keep consumers running or drop unused slots

### For `conflicting-slots`

Conflicting slots indicate configuration issues:

1. **Identify conflicting slots:**
   ```sql
   SELECT slot_name, conflicting
   FROM pg_replication_slots
   WHERE conflicting = true;
   ```

2. **Investigate the conflict** - check PostgreSQL logs for details
3. **Resolve configuration issues** causing the conflict

### For `inactive-slots`

Inactive slots prevent WAL cleanup:

1. **Identify inactive slots:**
   ```sql
   SELECT slot_name, slot_type, active, wal_status
   FROM pg_replication_slots
   WHERE active = false;
   ```

2. **Check if the subscriber/consumer is still needed:**
   - If YES: Fix the subscriber/consumer application and restart it
   - If NO: Drop the slot (see "Dropping Unused Slots" below)

### For `critical-lag`

Slots with >= 5GB lag may never catch up:

1. **Check lag:**
   ```sql
   SELECT slot_name,
          pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS lag
   FROM pg_replication_slots
   WHERE active = true
   ORDER BY pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn) DESC;
   ```

2. **Investigate consumer:**
   - Check consumer application health and logs
   - Review network connectivity
   - Check for processing bottlenecks
   - Monitor consumer error logs

3. **Consider recreating consumer** if lag is unrecoverable.

### For `high-lag`

Slots with >= 1GB lag need attention:

1. **Monitor lag trends** - is it growing or stable?
2. **Investigate consumer health** (same as critical-lag)
3. **Optimize consumer processing** if possible
4. **Increase consumer resources** if needed

### Dropping Unused Slots

**⚠️ Warning**: Only drop slots that are no longer needed!

```sql
SELECT pg_drop_replication_slot('slot_name');
```

### Emergency: Disk Space Critical

If disk space is critical and slots are unused:

```sql
-- List all slots
SELECT * FROM pg_replication_slots;

-- Drop unused slot (CAUTION!)
SELECT pg_drop_replication_slot('unused_slot_name');
```

## References

- [PostgreSQL Documentation: Replication Slots](https://www.postgresql.org/docs/current/warm-standby.html#STREAMING-REPLICATION-SLOTS)
- [PostgreSQL Documentation: pg_replication_slots](https://www.postgresql.org/docs/current/view-pg-replication-slots.html)
