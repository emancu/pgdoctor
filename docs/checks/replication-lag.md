# Replication Lag Check

Monitors active replication streams to ensure subscribers are keeping up with the publisher.

## What It Checks

### no-replication

Reports OK status when no replication is configured on the database.

**Severity:** OK

**Why this check exists:** Differentiates between "no replication configured" (OK) and "replication configured but failing" (WARN/FAIL). Many databases intentionally have no replication.

### replication-state

Validates that all active replication streams are in the `streaming` state.

**Severity:**
- WARN: Stream in `catchup` state
- FAIL: Stream in `backup` or `stopping` state

**Replication states:**
- **streaming**: Normal operation (healthy)
- **catchup**: Falling behind, trying to catch up (early warning)
- **backup**: Taking base backup (expected during initial setup)
- **stopping**: Shutting down (expected during maintenance)

**Why this matters:** Non-streaming states indicate replication is not operating normally. While `backup` and `stopping` are expected during maintenance, they should be transient. Persistent non-streaming states suggest problems.

### wal-retention

Validates that replication slots have healthy WAL retention status.

**Severity:**
- WARN: Slot has `extended` status
- FAIL: Slot has `unreserved` or `lost` status

**WAL status values:**
- **reserved**: Healthy - WAL files are properly retained
- **extended**: Warning - Using more disk than `max_slot_wal_keep_size`
- **unreserved**: Critical - WAL files may be removed, risking data loss
- **lost**: Critical - WAL files were removed, slot is unusable

**Why this matters:** If WAL files are removed before a replica can consume them, the replica cannot catch up and must be recreated from a fresh backup. The `lost` status means the slot is permanently broken and must be dropped.

### physical-replication-lag

Monitors replay lag for physical standby servers (streaming replication to standbys).

**Severity:**
- FAIL: >= 1 second
- WARN: >= 250ms
- OK: < 250ms

**Why strict thresholds?** Physical standbys should be nearly synchronous (<250ms). High lag indicates:
- Network issues between primary and standby
- Standby under heavy load (can't apply WAL fast enough)
- Risk during failover (data loss or long recovery time)

Physical replication uses streaming replication protocol where WAL is sent directly to the standby and applied immediately. Any lag above 250ms suggests infrastructure problems.

### logical-replication-lag

Monitors replay lag for logical replication subscribers (CDC, selective replication, Debezium).

**Severity:**
- FAIL: >= 35 seconds
- WARN: >= 20 seconds
- OK: < 20 seconds

**Why much looser than physical?** Logical replication (especially Debezium/CDC) involves:
- Decoding WAL into logical changes
- Publishing to external systems (e.g., Kafka)
- Waiting for Kafka acknowledgments (with `acks=all` replication)
- Debezium batch processing and LSN flushing (can wait up to 30 seconds)

**Debezium-specific behavior:** The Debezium PostgreSQL connector batches changes and flushes LSN positions back to PostgreSQL periodically. During low-activity periods, it can legitimately hold WAL positions for 10-30 seconds before acknowledging consumption. This is normal operation, not a failure.

**Thresholds explained:**
- **< 20s**: Normal operation, including Debezium batch processing
- **20-35s**: Something may be slow (Kafka backpressure, consumer lag) - investigate
- **>= 35s**: Consumer is genuinely stuck or misconfigured - requires intervention

## Lag Metrics Explained

PostgreSQL tracks three types of lag from the **publisher's perspective**:

### Write Lag
Time until subscriber's OS receives WAL data (network latency).

### Flush Lag
Time until subscriber's OS flushes WAL to disk (I/O latency).

### Replay Lag
Time until subscriber applies changes to database (processing latency).

**Most important**: **Replay lag** - actual data freshness on subscriber.

## What Replay Lag Measures for Debezium (CDC)

For Debezium and other CDC tools using logical replication, `replay_lag` measures the **end-to-end latency from PostgreSQL commit to LSN acknowledgment**, which includes:

### The Complete Timeline

1. **PostgreSQL commits transaction** → Generates WAL
2. **Debezium decodes WAL** → Creates change events
3. **Debezium sends events to Kafka** → Network transfer
4. **Kafka acknowledges receipt** → Broker confirms write with replication
5. **Kafka Connect triggers commit** → Signals source connector
6. **Debezium flushes LSN back to PostgreSQL** → Updates `confirmed_flush_lsn`
7. **PostgreSQL updates `replay_lsn`** → Visible in `pg_stat_replication`

**Only after step 7 does `replay_lag` decrease.**

### What This Means

When you see "30 seconds of lag" for Debezium, this includes:
- WAL decoding time (usually <100ms)
- Kafka publish time (usually <1 second)
- Kafka acknowledgment time (depends on `acks=all` and replication)
- **Any backpressure or processing delays**

### Normal vs Problematic Lag

**Normal Debezium latency:** 1-2 seconds
- Includes WAL decode + Kafka round-trip with `acks=all`
- Kafka typically responds in milliseconds to low seconds

**Problematic lag:** 5+ seconds (your threshold)
- Indicates actual processing backlog
- Could be caused by:
  - High write volume (too many changes to process)
  - Kafka broker issues (slow responses, replication lag)
  - Network latency (Debezium to Kafka connection)
  - Debezium configuration (batch sizes too small)
  - Downstream consumer lag (backpressure from slow consumers)

### Diagnosing High Debezium Lag

**Check replication slot status:**
```sql
SELECT
  slot_name,
  pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn) AS lag_bytes,
  pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS retained_wal
FROM pg_replication_slots
WHERE slot_name LIKE '%debezium%' OR slot_name LIKE '%cdc%';
```

**Check Kafka consumer group lag:**
```bash
# If Debezium consumer group is lagging, it's not PostgreSQL's fault
kafka-consumer-groups --bootstrap-server localhost:9092 \
  --describe --group {debezium-connect-group}
```

**Check Debezium connector metrics** (if JMX enabled):
- `source-record-poll-total` - Records read from PostgreSQL
- `source-record-write-total` - Records written to Kafka
- Gap between these indicates Kafka publish bottleneck

### Recommendations for Debezium Lag

1. **Tune batch sizes:**
   ```properties
   max.batch.size=2048
   max.queue.size=8192
   ```

2. **Enable heartbeat events** (prevents LSN stagnation):
   ```properties
   heartbeat.interval.ms=10000
   heartbeat.action.query=INSERT INTO debezium.heartbeat_events (ts) VALUES (NOW())
   ```

3. **Monitor Kafka broker health** - Slow brokers cause backpressure

4. **Check transaction size** - Large transactions delay acknowledgment

**References:**
- [Debezium PostgreSQL Connector](https://debezium.io/documentation/reference/stable/connectors/postgresql.html)
- [PostgreSQL Replication Slots: LSN Flushing](https://www.morling.dev/blog/postgres-replication-slots-confirmed-flush-lsn-vs-restart-lsn/)
- [Debezium Lag Troubleshooting](https://medium.com/@pawanpg0963/postgres-replication-lag-using-debezium-connector-4ba50e330cd6)

## Replication Type Detection

The check distinguishes between physical and logical replication by joining `pg_stat_replication` with `pg_replication_slots` and using `slot_type`:
- **Logical replication**: `slot_type = 'logical'` (e.g., Debezium, native logical subscriptions)
- **Physical replication**: `slot_type = 'physical'` OR `sync_state IS NOT NULL` (for legacy physical replication without slots)

**Why not use `sync_state`?** The `sync_state` column indicates synchronous replication mode (async/sync/potential/quorum), NOT the replication type. Debezium and other logical replication tools typically use `sync_state = 'async'`, which would incorrectly classify them as physical if we only checked `sync_state`.

The check displays:
- **Slot name**: The replication slot associated with the stream (can infer which Debezium connector from the name)
- **State**: Connection state (streaming/catchup/backup/stopping) - validated by `replication-state` subcheck
- **WAL Status**: Health of the slot (reserved/extended/unreserved/lost) - validated by `wal-retention` subcheck

## Identifying Problematic Streams

When replication lag is detected, identify the source:

```sql
-- Get full replication stream details
SELECT
  application_name,
  client_addr,
  usename as user,
  backend_start,
  CASE WHEN sync_state IS NULL THEN 'logical' ELSE 'physical' END AS type,
  state,
  replay_lag
FROM pg_stat_replication
ORDER BY replay_lag DESC NULLS LAST;
```

**For logical replication**, correlate with slots and subscriptions:

```sql
-- Find which slot and subscription
SELECT
  s.application_name,
  s.client_addr,
  rs.slot_name,
  rs.slot_type,
  rs.database,
  rs.plugin,
  pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), rs.restart_lsn)) AS retained_wal
FROM pg_stat_replication s
JOIN pg_replication_slots rs ON rs.active_pid = s.pid
WHERE s.replay_lag IS NOT NULL
ORDER BY s.replay_lag DESC;
```

**For physical replication**, check standby identity:

```sql
-- Get standby server info
SELECT
  application_name,
  client_addr,
  client_hostname,
  backend_start,
  state,
  sync_state,
  pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)) AS lag
FROM pg_stat_replication
WHERE sync_state IS NOT NULL
ORDER BY pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) DESC;
```

## Why This Matters

### For Physical Replication
- **Failover readiness**: High lag = data loss during failover
- **Read replica staleness**: Users see outdated data
- **HA reliability**: Defeats purpose of having a standby

### For Logical Replication
- **CDC pipelines**: Stale data warehouse = wrong analytics
- **Data synchronization**: Applications relying on replicated data see delays
- **Backpressure**: Slow subscribers can eventually cause publisher issues

## Common Causes

### Physical Replication Lag
1. **Network issues**: Bandwidth or latency between servers
2. **Standby overloaded**: Too many read queries on standby
3. **Disk I/O bottleneck**: Standby can't write WAL fast enough
4. **Configuration**: `max_wal_senders`, `wal_sender_timeout` too restrictive

### Logical Replication Lag
1. **Slow subscriber queries**: Complex transformations or slow inserts
2. **Conflicts**: Unique constraint violations, missing tables
3. **Network issues**: Same as physical
4. **Resource constraints**: Subscriber CPU/memory/disk saturated

## How to Fix

### For all subchecks: Investigate Current Lag

```sql
-- Check all replication lag
SELECT
  application_name,
  client_addr,
  state,
  CASE
    WHEN backend_type = 'walsender' THEN 'physical'
    ELSE 'logical'
  END AS type,
  pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)) AS lag_bytes,
  replay_lag
FROM pg_stat_replication
ORDER BY pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) DESC;
```

### For `physical-replication-lag`

**1. Check standby resource usage:**
```sql
-- On standby
SELECT * FROM pg_stat_activity WHERE state = 'active';
```

**2. Reduce read load on standby:**
- Move heavy queries to dedicated read replicas
- Use `hot_standby_feedback = on` carefully (can bloat primary)

**3. Network diagnostics:**
```bash
# Check bandwidth and latency
iperf3 -c standby-host
ping standby-host
```

**4. Increase WAL buffers (on primary):**
```sql
ALTER SYSTEM SET wal_buffers = '32MB';
SELECT pg_reload_conf();
```

### For `logical-replication-lag`

**1. Check subscriber errors:**
```sql
-- On subscriber
SELECT * FROM pg_stat_subscription;

-- Check for apply errors
SELECT * FROM pg_stat_subscription_errors;
```

**2. Identify slow queries on subscriber:**
```sql
-- On subscriber
SELECT query, state, wait_event_type, wait_event
FROM pg_stat_activity
WHERE application_name LIKE '%subscription%';
```

**3. Check for conflicts:**
```sql
-- On subscriber (PG14+)
SELECT * FROM pg_stat_subscription_errors;
```

**4. Optimize subscriber:**
- Add indexes for frequently updated columns
- Increase `work_mem` for apply workers
- Use `REPLICA IDENTITY FULL` on publisher for better conflict resolution

**5. Increase apply workers (on subscriber):**
```sql
-- Allow more parallel apply
ALTER SUBSCRIPTION my_sub SET (streaming = parallel);
```

### For `replication-state`

If streams are not in 'streaming' state, investigate why:

```sql
-- Check state and troubleshoot
SELECT application_name, state, sync_state
FROM pg_stat_replication
WHERE state != 'streaming';
```

Common non-streaming states:
- `catchup` - Stream is behind, trying to catch up (check lag subchecks)
- `backup` - Taking base backup (expected during setup)
- `stopping` - Shutting down (expected during maintenance)

### For `wal-retention`

If WAL status is problematic:

```sql
-- Check WAL retention status
SELECT slot_name, wal_status,
       pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)) AS retained_wal
FROM pg_replication_slots;
```

**For `lost` or `unreserved` status:**
- Slot is broken, consumer must be recreated from fresh backup
- Drop the slot: `SELECT pg_drop_replication_slot('slot_name');`
- Recreate consumer and replication slot

**For `extended` status:**
- Increase `max_slot_wal_keep_size` if possible
- Investigate why consumer is lagging (see lag subchecks)

### Emergency Response

If lag is critical and growing:

**Physical replication:**
```sql
-- Consider temporarily promoting standby if primary is failing
-- Or stop read queries on standby to let it catch up
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE pid != pg_backend_pid()
  AND application_name NOT LIKE '%replication%';
```

**Logical replication:**
```sql
-- Temporarily disable subscription to prevent further lag
ALTER SUBSCRIPTION my_sub DISABLE;

-- Investigate and fix root cause
-- Then re-enable
ALTER SUBSCRIPTION my_sub ENABLE;
```

## Monitoring Best Practices

### Set Up Alerting
Monitor these queries continuously:

```sql
-- Alert if any replication lag > 5 seconds
SELECT application_name,
       EXTRACT(EPOCH FROM replay_lag) as replay_seconds
FROM pg_stat_replication
WHERE EXTRACT(EPOCH FROM replay_lag) > 5;
```

### Track Trends
- Graph replay lag over time
- Correlate with primary load
- Identify peak lag periods

### Regular Health Checks
```sql
-- Daily health check
SELECT
  application_name,
  state,
  sync_state,
  pg_size_pretty(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn)) AS lag,
  replay_lag
FROM pg_stat_replication
ORDER BY pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) DESC;
```

## Related Checks

- **`replication-slots`** - Monitors slot health and WAL retention
  - Inactive slots filling disk
  - WAL status (lost/unreserved)
  - Slot invalidation

- **`publications`** - Publication configuration validation
  - What data is published
  - Schema filtering
  - FOR ALL TABLES usage

- **`cdc-warehouse`** - CDC warehouse publication validation
  - Publication coverage
  - Replica identity configuration

## References

- [PostgreSQL Documentation: pg_stat_replication](https://www.postgresql.org/docs/current/monitoring-stats.html#MONITORING-PG-STAT-REPLICATION-VIEW)
- [PostgreSQL Documentation: Streaming Replication](https://www.postgresql.org/docs/current/warm-standby.html#STREAMING-REPLICATION)
- [PostgreSQL Documentation: Logical Replication Monitoring](https://www.postgresql.org/docs/current/logical-replication-monitoring.html)
