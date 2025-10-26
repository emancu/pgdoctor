# Vacuum Settings Check

**ID**: `vacuum_settings`
**Category**: `performance`

## Description

This check validates PostgreSQL vacuum-related configuration settings. Proper vacuum configuration is critical for database performance, preventing table bloat, keeping statistics current, and ensuring efficient query execution. Poorly tuned vacuum settings can lead to performance degradation, excessive disk usage, and suboptimal query plans.

## Query

```sql
SELECT
    name::varchar,
    setting,
    unit
FROM pg_settings
WHERE name IN (
    'autovacuum_analyze_scale_factor',
    'autovacuum_max_workers',
    'autovacuum_vacuum_scale_factor',
    'maintenance_work_mem',
    'vacuum_cost_delay',
    'vacuum_cost_limit',
    'work_mem'
)
```

This query retrieves the current values of key vacuum and memory-related settings from the PostgreSQL configuration.

## Validations

### 1. Autovacuum Scale Factors

#### autovacuum_analyze_scale_factor
- **WARN**: Value > 0.1
  - High values delay ANALYZE on large tables
  - Can result in outdated statistics and poor query plans
  - Recommended: 0.05-0.1 for most workloads

- **OK**: Value ≤ 0.1
  - Statistics will be updated frequently enough

#### autovacuum_vacuum_scale_factor
- **WARN**: Value > 0.2
  - High values allow more dead tuples before vacuum triggers
  - Can cause significant table bloat on large tables
  - Recommended: 0.05-0.1 for busy tables

- **OK**: Value ≤ 0.2
  - Reasonable threshold for vacuum triggering

### 2. Autovacuum Workers

#### autovacuum_max_workers
- **WARN**: Value < 3
  - Too few workers can't keep up with multiple large tables
  - May cause vacuum backlog

- **WARN**: Value > 10
  - Too many workers cause resource contention
  - Diminishing returns beyond 6-8 workers

- **OK**: 3-10 workers
  - Appropriate for most workloads

### 3. Maintenance Work Memory

#### maintenance_work_mem
- **CRITICAL**: Value < 64 MB
  - Severely impacts VACUUM and index creation performance
  - Will cause excessive I/O operations
  - Immediate increase recommended

- **WARN**: Value < 256 MB
  - Suboptimal for maintenance operations
  - Consider increasing to 256 MB or higher

- **WARN**: Value > 2 GB
  - Excessive memory allocation
  - Benefits plateau beyond 1-2 GB

- **OK**: 256 MB - 2 GB
  - Optimal range for most databases

### 4. Vacuum Cost Settings

#### vacuum_cost_delay
- **WARN**: Value > 10 ms
  - Vacuum is heavily throttled
  - May not keep up with write workload
  - Consider reducing if vacuum is lagging

- **OK**: Value ≤ 10 ms
  - Reasonable throttling

#### vacuum_cost_limit
- **WARN**: Value < 200
  - Vacuum is too aggressive with throttling
  - Will run very slowly
  - Increase to at least 200

- **OK**: Value ≥ 200
  - Allows vacuum to make reasonable progress

### 5. Work Memory

#### work_mem
- **WARN**: Value < 4 MB
  - Very low, causes excessive disk sorts
  - Query performance will suffer
  - Increase to at least 4-8 MB

- **WARN**: Value > 1 GB
  - Very high per-operation memory
  - Risk of OOM with many concurrent connections
  - Each sort/hash operation can use this much memory

- **OK**: 4 MB - 1 GB
  - Reasonable range depending on workload and available memory

## Example Output

### Well-Tuned Configuration
```
✓ [OK] autovacuum_analyze_scale_factor is 0.05 (optimal).
✓ [OK] autovacuum_vacuum_scale_factor is 0.1 (optimal).
✓ [OK] autovacuum_max_workers is 5 (optimal).
✓ [OK] maintenance_work_mem is 512 MB (optimal).
✓ [OK] vacuum_cost_delay is 2 ms (acceptable).
✓ [OK] vacuum_cost_limit is 200 (acceptable).
✓ [OK] work_mem is 16 MB (acceptable).
```

### Configuration Needing Tuning
```
⚠ [WARN] autovacuum_analyze_scale_factor is 0.2. Values > 0.1 may delay ANALYZE on large tables.
⚠ [WARN] autovacuum_vacuum_scale_factor is 0.3. Values > 0.2 may cause bloat in large tables.
⚠ [WARN] autovacuum_max_workers is 2. Consider increasing to at least 3.
✗ [CRITICAL] maintenance_work_mem is 32 MB. This is too low and will significantly slow down VACUUM.
```

## Troubleshooting

### How to check current settings manually

```sql
SELECT name, setting, unit, context
FROM pg_settings
WHERE name LIKE '%vacuum%' OR name LIKE '%work_mem%'
ORDER BY name;
```

### How to modify settings

Settings can be changed at different levels depending on the `context`:

**PostgreSQL configuration file (postgresql.conf):**
```ini
# Autovacuum settings
autovacuum_max_workers = 5
autovacuum_analyze_scale_factor = 0.05
autovacuum_vacuum_scale_factor = 0.1

# Memory settings
maintenance_work_mem = 512MB
work_mem = 16MB

# Cost-based vacuum delay
vacuum_cost_delay = 2ms
vacuum_cost_limit = 200
```

After editing postgresql.conf, reload the configuration:
```sql
SELECT pg_reload_conf();
```

**Session-level (temporary):**
```sql
SET work_mem = '32MB';
SET maintenance_work_mem = '1GB';
```

**Per-table autovacuum settings:**
```sql
ALTER TABLE large_table SET (
    autovacuum_vacuum_scale_factor = 0.01,
    autovacuum_analyze_scale_factor = 0.005
);
```

### Monitoring Vacuum Activity

Check autovacuum status:
```sql
SELECT
    schemaname,
    relname,
    n_dead_tup,
    n_live_tup,
    round(n_dead_tup * 100.0 / NULLIF(n_live_tup + n_dead_tup, 0), 2) as dead_ratio,
    last_vacuum,
    last_autovacuum
FROM pg_stat_user_tables
WHERE n_dead_tup > 0
ORDER BY n_dead_tup DESC;
```

Check running vacuum operations:
```sql
SELECT
    pid,
    datname,
    usename,
    state,
    query,
    query_start
FROM pg_stat_activity
WHERE query LIKE '%VACUUM%' OR query LIKE '%ANALYZE%';
```

### Tuning Guidelines

1. **For High-Write Workloads**
   - Lower scale factors (0.02-0.05)
   - More workers (5-8)
   - Higher maintenance_work_mem (1-2 GB)
   - Consider per-table tuning for busiest tables

2. **For Large Databases**
   - Lower scale factors to prevent accumulation
   - Increase maintenance_work_mem
   - Monitor vacuum run times
   - Consider table partitioning

3. **For OLTP Systems**
   - Balance vacuum aggressiveness with I/O impact
   - Lower cost_delay, higher cost_limit
   - More workers during off-peak hours
   - Per-table settings for hot tables

4. **For Memory-Constrained Systems**
   - Be conservative with work_mem
   - Calculate: max_connections × work_mem × max_parallel_workers
   - Monitor for OOM conditions
   - Consider connection pooling

## References

- [PostgreSQL Autovacuum Documentation](https://www.postgresql.org/docs/current/routine-vacuuming.html#AUTOVACUUM)
- [Resource Consumption Settings](https://www.postgresql.org/docs/current/runtime-config-resource.html)
- [Vacuum Cost-Based Delay](https://www.postgresql.org/docs/current/runtime-config-resource.html#RUNTIME-CONFIG-RESOURCE-VACUUM-COST)
- [PostgreSQL Wiki: Tuning Autovacuum](https://wiki.postgresql.org/wiki/Tuning_Your_PostgreSQL_Server)
- [Monitoring Vacuum Activity](https://www.postgresql.org/docs/current/monitoring-stats.html)
