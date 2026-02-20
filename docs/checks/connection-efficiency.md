# Connection Efficiency Check

Analyzes PostgreSQL 14+ session statistics to identify connection pool efficiency issues and abnormal session terminations.

## Overview

PostgreSQL 14 introduced session-level statistics in `pg_stat_database` that track how connections are used over time. This check uses these metrics to identify:

1. **Oversized connection pools** - Connections spending most time idle
2. **Abandoned sessions** - Network issues or client crashes
3. **Fatal terminations** - Server errors or stability issues
4. **Killed sessions** - Manual terminations indicating stuck queries

## Requirements

- PostgreSQL 14 or later (session statistics don't exist in earlier versions)
- For versions < 14, the check reports OK and skips validation

## Subchecks

### busy-ratio

**Purpose**: Detect oversized connection pools by analyzing the session busy ratio.

**Metric**: `active_time / session_time * 100`

**Thresholds**:
- `>=20%` - Healthy utilization (OK)
- `<20%` - Oversized pool, wasting resources (WARN)

**What it means**:
- A low busy ratio means connections spend most of their time idle
- This indicates the pool is larger than necessary
- Oversized pools waste memory and connection slots

**Why we don't warn on high utilization**:
High busy ratio (even 90-100%) is **not inherently bad** - it means your connections are well-utilized. The real problems occur when:
1. Queries start **queuing** waiting for a connection
2. Connection **timeouts** occur
3. **Latency increases** due to pool exhaustion

These real-time symptoms are detected by the `connection-health` check's `pool-pressure` subcheck, which monitors current connection state rather than historical averages.

**Example scenarios**:
- Busy ratio 2% with 100 connections: Reduce to ~10-20 connections
- Busy ratio 15% with 50 connections: Acceptable, could optimize to 30-40
- Busy ratio 35% with 20 connections: Excellent utilization
- Busy ratio 95% with 20 connections: Excellent utilization (check `connection-health` for real-time pressure)

### sessions-abandoned

**Purpose**: Detect network issues or client crashes causing improper connection cleanup.

**Metric**: Count of sessions where client disconnected without proper cleanup.

**Thresholds**:
- `<=1%` - Normal (OK)
- `>1%` - Elevated abandonment (WARN)
- `>5%` - Critical abandonment rate (FAIL)

**What it means**:
- Client closed connection without sending termination message
- Often caused by network interruptions or application crashes
- Can indicate unstable network between app and database

**Example scenarios**:
- 50 abandoned out of 10,000 sessions (0.5%) - Normal
- 200 abandoned out of 10,000 sessions (2%) - Check network stability
- 600 abandoned out of 10,000 sessions (6%) - Critical network issues

### sessions-fatal

**Purpose**: Detect server errors or stability issues causing session terminations.

**Metric**: Count of sessions terminated due to server-side errors.

**Thresholds**:
- `<=1%` - Normal (OK)
- `>1%` - Elevated error rate (WARN)
- `>5%` - Critical error rate (FAIL)

**What it means**:
- Server encountered error and terminated session
- Can indicate out-of-memory conditions
- May point to database stability issues or resource exhaustion

**Example scenarios**:
- 10 fatal out of 10,000 sessions (0.1%) - Normal
- 150 fatal out of 10,000 sessions (1.5%) - Review error logs
- 600 fatal out of 10,000 sessions (6%) - Critical stability issues

### sessions-killed

**Purpose**: Detect high rates of manual query termination.

**Metric**: Count of sessions terminated via `pg_terminate_backend()`.

**Thresholds**:
- `<=1%` - Normal (OK)
- `>1%` - Elevated intervention (WARN)
- `>5%` - Critical kill rate (FAIL)

**What it means**:
- Admin or monitoring system manually killed the session
- Usually indicates stuck queries or long-running operations
- May suggest need for statement_timeout configuration

**Example scenarios**:
- 5 killed out of 10,000 sessions (0.05%) - Normal occasional cleanup
- 200 killed out of 10,000 sessions (2%) - Frequent stuck queries
- 600 killed out of 10,000 sessions (6%) - Serious query performance issues

## Understanding Session Statistics

PostgreSQL tracks cumulative statistics since last reset:

```sql
SELECT
  datname,
  sessions,              -- Total sessions established
  session_time,          -- Total time sessions were connected (ms)
  active_time,           -- Total time sessions were executing queries (ms)
  sessions_abandoned,    -- Sessions ended by client disconnect
  sessions_fatal,        -- Sessions ended by server error
  sessions_killed        -- Sessions terminated by admin
FROM pg_stat_database
WHERE datname NOT IN ('template0', 'template1');
```

**Important notes**:
- Statistics are cumulative since database start or last `pg_stat_reset()`
- The busy ratio is calculated across ALL sessions, not just current ones
- Rates are based on total historical sessions, not point-in-time snapshots

## How to Fix

### For `busy-ratio`

Low busy ratio indicates oversized pool:

1. **Reduce PgBouncer server pool size** (if using PgBouncer):
   - Databases with PgBouncer always use transaction pooling
   - Adjust `default_pool_size` based on vCPUs (2-4x CPU cores)
   - Configuration lives in your connection pooler deployment

2. **Reduce application pool size**:
   - Configuration lives in each service's source code (ORM settings)
   - Consult your ORM documentation (Ecto, ActiveRecord, etc.)

3. **Review connection needs**:
   - Are background workers holding idle connections?
   - Are there services with oversized pool configurations?

### For `sessions-abandoned`

1. **Network diagnostics**:
   ```bash
   # Check packet loss
   ping -c 100 database-host

   # Check connection stability
   mtr --report database-host
   ```

2. **Review application timeout settings**:
   - Check your ORM's connection timeout configuration
   - Consider TCP keepalive settings for long-lived connections
   - Ensure proper connection cleanup in error handlers

### For `sessions-fatal`

1. **Check PostgreSQL logs**:
   ```bash
   # Look for OOM, crashes, or errors
   aws logs tail /aws/rds/instance/<dbinstancename>/postgresql --follow | grep -i "fatal\|error\|out of memory"
   ```

2. **Memory management**:
   ```sql
   -- Reduce work_mem if seeing OOM
   ALTER SYSTEM SET work_mem = '64MB';  -- Down from 256MB
   SELECT pg_reload_conf();

   -- Check current memory usage
   SELECT * FROM pg_stat_database WHERE datname = current_database();
   ```

3. **Query optimization**:
   - Identify queries causing errors
   - Optimize memory-intensive operations
   - Add appropriate indexes to reduce memory usage

### For `sessions-killed`

1. **Set appropriate timeouts**:
   - User timeouts are set in your infrastructure configuration

2. **Identify problematic queries**:
   ```sql
   -- Find long-running queries
   SELECT pid, now() - query_start as duration, query
   FROM pg_stat_activity
   WHERE state = 'active'
   ORDER BY duration DESC;
   ```

3. **Optimize slow queries**:
   - Use `EXPLAIN ANALYZE` to find bottlenecks
   - Add indexes for frequently filtered columns
   - Review query plans for sequential scans on large tables

## Monitoring Queries

### Track busy ratio trends:
```sql
SELECT
  datname,
  sessions,
  ROUND(100.0 * active_time / NULLIF(session_time, 0), 1) as busy_pct,
  pg_size_pretty(pg_database_size(datname)) as db_size
FROM pg_stat_database
WHERE datname NOT IN ('template0', 'template1')
ORDER BY busy_pct;
```

### Track termination trends:
```sql
SELECT
  datname,
  sessions,
  sessions_abandoned,
  sessions_fatal,
  sessions_killed,
  ROUND(100.0 * sessions_abandoned / NULLIF(sessions, 0), 1) as abandon_pct,
  ROUND(100.0 * sessions_fatal / NULLIF(sessions, 0), 1) as fatal_pct,
  ROUND(100.0 * sessions_killed / NULLIF(sessions, 0), 1) as kill_pct
FROM pg_stat_database
WHERE datname NOT IN ('template0', 'template1')
ORDER BY sessions DESC;
```

### Reset statistics (when needed):
```sql
-- Reset all database statistics (for current database)
SELECT pg_stat_reset();
```

**Warning**: Resetting statistics clears historical data. Only do this if:
- You've completed analysis and want fresh baseline
- Statistics are skewed by one-time events
- You're testing pool size changes and want clean comparison

## References

- [PostgreSQL 14 Release Notes - Session Statistics](https://www.postgresql.org/docs/14/release-14.html)
- [pg_stat_database Documentation](https://www.postgresql.org/docs/current/monitoring-stats.html#MONITORING-PG-STAT-DATABASE-VIEW)
- [Connection Pooling Best Practices](https://www.postgresql.org/docs/current/runtime-config-connection.html)

## Related Checks

- `connection-health` - Monitors connection pool saturation and stuck transactions
- `session-settings` - Validates session timeout configurations
