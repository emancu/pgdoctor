# Session Settings Check

Verifies that PostgreSQL role-level session settings (timeouts and logging) are properly configured for app_ro and app_rw roles.

## What it checks

- **statement_timeout**: Maximum time a single statement can run
- **transaction_timeout**: Maximum duration of a transaction (PostgreSQL 17+)
- **idle_in_transaction_session_timeout**: Timeout for idle transactions
- **log_min_duration_statement**: Threshold for logging slow queries

## Why it matters

Proper session settings prevent critical production issues:
- **Long-running queries**: Without timeouts, queries can block other operations indefinitely
- **Connection pool exhaustion**: Idle transactions hold connections and locks
- **Application hangs**: Missing timeouts cause user-facing timeouts and errors
- **Poor observability**: Without query logging, slow queries go undetected

Common production problems caused by misconfigured settings:
- Connection pool depletion from abandoned transactions
- Lock queues causing cascading failures
- Memory pressure from long-running aggregations
- Inability to identify performance regressions

## How to Fix

Configure role settings using `ALTER ROLE`:

```sql
-- For read-only role
ALTER ROLE app_ro SET statement_timeout = '3000ms';
ALTER ROLE app_ro SET transaction_timeout = '3000ms';
ALTER ROLE app_ro SET idle_in_transaction_session_timeout = '60000ms';
ALTER ROLE app_ro SET log_min_duration_statement = '2000ms';

-- For read-write role
ALTER ROLE app_rw SET statement_timeout = '3000ms';
ALTER ROLE app_rw SET transaction_timeout = '3000ms';
ALTER ROLE app_rw SET idle_in_transaction_session_timeout = '60000ms';
ALTER ROLE app_rw SET log_min_duration_statement = '2000ms';
```

### Recommended Values

- **statement_timeout**: `500-5000ms`
  - Prevents long-running queries from blocking
  - Typical web requests should complete within seconds
  - Adjust based on your p95 query latency

- **transaction_timeout**: `500-5000ms` (requires PostgreSQL 17+)
  - Should match statement_timeout
  - Protects against transaction-level hangs
  - Critical for preventing long-held locks

- **idle_in_transaction_session_timeout**: `60000ms` (60 seconds)
  - Closes connections left in "idle in transaction" state
  - Prevents connection pool exhaustion
  - Frees locks held by abandoned transactions

- **log_min_duration_statement**: `2000ms`
  - Logs queries taking longer than 2 seconds
  - Essential for identifying slow queries
  - Balance between visibility and log volume

### Verification

After applying settings, verify they're active:

```sql
SELECT rolname, rolconfig
FROM pg_roles
WHERE rolname IN ('app_ro', 'app_rw');
```

### Important Notes

- Settings apply to **new connections only** - existing connections keep old values
- Consider application deployment to cycle connections
- Monitor application error rates after changes

## References

- [PostgreSQL Documentation: ALTER ROLE](https://www.postgresql.org/docs/current/sql-alterrole.html)
- [PostgreSQL Documentation: Runtime Configuration](https://www.postgresql.org/docs/current/runtime-config-client.html)
