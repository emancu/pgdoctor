# Connection Health Check

Monitors PostgreSQL connection pool health, identifying saturation, pool pressure, idle ratios, and stuck transactions.

## Overview

This check provides **real-time visibility** into your connection pool's current state by querying `pg_stat_activity`. It complements the `connection-efficiency` check, which analyzes historical trends from `pg_stat_database`.

## Understanding Connection Pooling

### Why Connection Pools Exist

Creating a PostgreSQL connection is expensive (~100-200ms) because it involves:
1. TCP handshake
2. SSL negotiation (if enabled)
3. Authentication
4. Backend process creation (fork)

Connection pools solve this by maintaining pre-established connections that applications can borrow and return.

### Two Types of Pools

```
┌─────────────────┐     ┌─────────────────┐     ┌─────────────────┐
│   Application   │────▶│    PgBouncer    │────▶│   PostgreSQL    │
│   (100 conns)   │     │   (20 conns)    │     │ (max_connections│
│                 │     │                 │     │     = 100)      │
└─────────────────┘     └─────────────────┘     └─────────────────┘
     Client Pool           Server Pool            Database
```

**Without PgBouncer**: Application connects directly to PostgreSQL
- Each application connection = 1 PostgreSQL backend
- Pool size limited by `max_connections`
- Memory usage: ~10MB per connection

**With PgBouncer**: Application connects to pooler, pooler connects to PostgreSQL
- Many client connections share few server connections
- Transaction pooling: connection returned after each transaction
- Server pool stays small (2-4x CPU cores)

### Common Misconception

> "I'm getting connection timeouts, I should increase `default_pool_size`"

**Wrong!** `default_pool_size` is the *server* pool (PgBouncer → PostgreSQL).
You likely need to increase `max_client_conn` (Application → PgBouncer).

The server pool should stay small because PostgreSQL performs best with few concurrent connections doing actual work.

## Subchecks

### connection-overview

Displays a summary of current connection pool status:
- Total connections / available connections
- Active, Idle, Idle-in-transaction, and Waiting counts

This is informational (always OK) and provides context for other subchecks.

### connection-saturation

Checks if the database is approaching `max_connections`.

**Thresholds:**
- Warning: >70% of available connections used
- Critical: >85% of available connections used

**What it means:**
Running out of connection slots prevents new connections entirely. This is different from pool pressure - saturation means you're hitting PostgreSQL's hard limit.

### pool-pressure

Detects when nearly all connections are busy and new queries may need to wait.

**Thresholds:**
- Warning: >90% connections active AND <3 idle connections
- Critical: >90% connections active AND ≤1 idle connection
- Skipped if fewer than 10 total connections

**What it means:**
Pool pressure indicates that your current connections are fully utilized. New queries arriving may need to wait for an existing query to complete before getting a connection.

**Important nuance for PgBouncer users:**
High pool pressure on the PostgreSQL side is **expected and healthy** when using PgBouncer. The pooler is designed to keep server connections busy. What matters is whether *clients* are waiting for connections from PgBouncer (check `SHOW POOLS` in PgBouncer admin).

**Why we detect this separately from historical busy ratio:**
The `connection-efficiency` check shows that connections are well-utilized over time (good!). This check shows whether RIGHT NOW there's capacity for new queries. Both metrics are valuable:

| Metric | Source | Question Answered |
|--------|--------|-------------------|
| Busy ratio (efficiency) | `pg_stat_database` | "Are connections well-utilized over time?" |
| Pool pressure (health) | `pg_stat_activity` | "Can new queries get a connection right now?" |

### idle-ratio

Detects when too many connections are idle, indicating an oversized pool.

**Thresholds:**
- Warning: >50% of connections idle (minimum 20 connections)
- Critical: >75% of connections idle

**What it means:**
Many idle connections waste memory and connection slots. This often indicates:
- `default_pool_size` too high in PgBouncer
- Application pool `min_size` too high
- Connections not being returned to pool properly

### idle-in-transaction

Identifies connections stuck in 'idle in transaction' state.

**Thresholds (based on `idle_in_transaction_session_timeout` setting):**
- Warning: Duration exceeds 50% of the timeout setting
- Critical: Duration exceeds 100% of the timeout setting
- If timeout is disabled (0), uses a 5-minute default

**Why this matters:**
Idle-in-transaction connections:
- Hold row locks, blocking other queries
- Prevent autovacuum from cleaning dead tuples
- Can cause transaction ID wraparound issues
- Waste a connection slot doing nothing

### long-idle

Detects connections that have been idle for >30 minutes.

**Thresholds:**
- Warning: ≥10 connections idle >30 minutes
- Critical: ≥50 connections idle >30 minutes

**What it means:**
Long-idle connections may indicate:
- Connection leak (app not returning connections)
- Oversized minimum pool size
- Abandoned connections from crashed clients

## How to Fix

### For `connection-saturation`

Increase PostgreSQL connection limits or add PgBouncer:

```bash
# Option 1: Increase max_connections (requires DB restart)
# Calculate: 2-4x CPU cores for direct connections
# For 4 vCPU instance: 8-16 connections recommended
1. Update your PostgreSQL configuration (e.g., postgresql.conf, terraform, or cloud provider settings)
2. Set `max_connections = 200` (adjust for your workload)
3. Restart the database to apply changes.

# Option 2: Add PgBouncer (recommended)
# See your connection pooler deployment configuration.
# Configure:
#   pool_size: 8
#   min_pool_size: 5
#   pool_mode: transaction
```

### For `pool-pressure`

**With PgBouncer (most common):**
High pool pressure on PostgreSQL side is expected and healthy when using PgBouncer.

 Check client-side instead, modifying Ecto/ActiveRecord settings

```bash
# Connect to PgBouncer admin console
psql "host=pgbouncer-host port=6432 dbname=pgbouncer"

# Check for client-side queuing
SHOW POOLS;
# Look at cl_waiting column - if >0, clients are waiting

# If clients are waiting, increase server pool:
# Edit PgBouncer config:
# default_pool_size = 16  # Increase from 8
```

**Without PgBouncer:**
Add PgBouncer or optimize slow queries to free up connections faster.

### For `idle-ratio`

Reduce connection pool sizes:

```bash
# Option 1: Reduce PgBouncer default_pool_size
# Edit PgBouncer config:
# default_pool_size = 8  # Reduce from 16

# Option 2: Reduce application pool min_size
# In your application config (Ecto example):
pool_size: 10,          # Down from 20
pool_overflow: 5,       # Add overflow instead
min_pool_size: 2        # Keep minimum low

# In Rails (database.yml):
pool: 10                # Down from 20
```

### For `idle-in-transaction`

Fix application transaction handling:

```bash
# Step 1: Enable timeout (prevents indefinite idle-in-transaction)
# NOTE: Reflect this in terraform
ALTER SYSTEM SET idle_in_transaction_session_timeout = '10sec';
SELECT pg_reload_conf();

# Step 2: Identify problematic queries
SELECT pid, state, query_start, query
FROM pg_stat_activity
WHERE state = 'idle in transaction'
  AND query_start < NOW() - INTERVAL '5 minutes';

# Step 3: Kill stuck connections (if timeout doesn't work)
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE state = 'idle in transaction'
  AND query_start < NOW() - INTERVAL '10 minutes';

# Step 4: Fix application code
# Common causes:
# - Missing COMMIT/ROLLBACK
# - Transactions kept open during external API calls
# - Long-running application logic inside transactions
```

### For `long-idle`

Fix connection leaks in application:

```bash
# Step 1: Identify leaked connections
SELECT pid, usename, application_name, state_change, query
FROM pg_stat_activity
WHERE state = 'idle'
  AND state_change < NOW() - INTERVAL '30 minutes'
ORDER BY state_change;

# Step 2: Kill old idle connections
SELECT pg_terminate_backend(pid)
FROM pg_stat_activity
WHERE state = 'idle'
  AND state_change < NOW() - INTERVAL '1 hour';

# Step 3: Enable connection timeout
# For PgBouncer:
# server_idle_timeout = 600  # Close idle server connections after 10 min

# For PostgreSQL (not recommended, PgBouncer handles this better):
ALTER SYSTEM SET idle_session_timeout = '1h';
SELECT pg_reload_conf();

# Step 4: Fix application connection pooling
# Ensure connections are returned to pool properly
# Check for:
# - Missing connection.close() in error handlers
# - Connection pool exhaustion causing app to hold connections
# - Long-running background jobs not releasing connections
```

## Decision Tree: Diagnosing Connection Issues

```
Connection problems?
│
├─► "Connection refused" / "too many connections"
│   └─► Check: connection-saturation
│       └─► Increase max_connections OR add PgBouncer
│
├─► "Slow to get connection" / High latency
│   └─► Check: pool-pressure
│       ├─► With PgBouncer: Check client-side queuing (SHOW POOLS)
│       └─► Without PgBouncer: Add PgBouncer or optimize queries
│
├─► "Connections mostly idle"
│   └─► Check: idle-ratio + connection-efficiency busy-ratio
│       └─► Reduce pool size (save memory)
│
├─► "Locks / blocked queries"
│   └─► Check: idle-in-transaction
│       └─► Fix application transaction handling
│
└─► "Connection count growing over time"
    └─► Check: long-idle
        └─► Fix connection leaks in application
```

## Optimal Connection Pool Sizing

### Rule of Thumb

**PostgreSQL server connections** (direct or via PgBouncer server pool):
```
connections = (CPU cores × 2) to (CPU cores × 4)
```

For a 4-vCPU instance: 8-16 connections is usually optimal.

**Why so few?**
- PostgreSQL uses process-per-connection model
- Context switching overhead increases with more connections
- Diminishing returns beyond CPU core count
- Memory usage: ~10MB per connection

### With PgBouncer

Databases using PgBouncer always use transaction pooling.

Configuration lives in your connection pooler deployment:
```
- default_pool_size = 8-16 (server connections, keep small, 2-4x vCPUs)
- max_client_conn = 1000+ (client connections, can be large)
- pool_mode = transaction
```

### Application Pool

Configuration lives in each service's source code (ORM settings). Consult your ORM documentation:
- **Ecto**: [Ecto.Repo pool configuration](https://hexdocs.pm/ecto/Ecto.Repo.html)
- **ActiveRecord**: [ActiveRecord connection pool](https://api.rubyonrails.org/classes/ActiveRecord/ConnectionAdapters/ConnectionPool.html)

## Related Checks

- **connection-efficiency** - Analyzes historical session statistics (PostgreSQL 14+): busy ratio trends and abnormal termination patterns
- **session-settings** - Validates timeout configurations that affect connection behavior

## References

- [PgBouncer Documentation](https://www.pgbouncer.org/config.html)
- [PostgreSQL Connection Settings](https://www.postgresql.org/docs/current/runtime-config-connection.html)
- [Why Connection Pooling Matters](https://brandur.org/postgres-connections)

