# Session Settings Check

Verifies that PostgreSQL role-level session settings (timeouts and logging) are properly configured for application roles.

By default, application roles are **discovered dynamically** — any login-capable, non-system role is checked. You can also specify exact roles via configuration (see Library Configuration below).

## What it checks

- **statement_timeout**: Maximum time a single statement can run
- **transaction_timeout**: Maximum duration of a transaction (PostgreSQL 17+)
- **idle_in_transaction_session_timeout**: Timeout for idle transactions
- **log_min_duration_statement**: Threshold for logging slow queries

## Precedence model

For each `(role, setting)` pair, the check resolves the value the role would actually see when it connects to the current database, walking the PostgreSQL GUC hierarchy from most-specific to least-specific:

| Priority | Source                                              | `pg_db_role_setting` row              |
|----------|-----------------------------------------------------|---------------------------------------|
| 1        | `ALTER ROLE r IN DATABASE d SET ...`                | `setrole=r.oid`, `setdatabase=d.oid`  |
| 2        | `ALTER ROLE r SET ...`                              | `setrole=r.oid`, `setdatabase=0`      |
| 3        | `ALTER DATABASE d SET ...` / `ALTER ROLE ALL IN DATABASE d SET ...` | `setrole=0`, `setdatabase=d.oid` |
| 4        | `ALTER ROLE ALL SET ...`                            | `setrole=0`, `setdatabase=0`          |
| 5        | Cluster default (`postgresql.conf` / `ALTER SYSTEM` / built-in) | `pg_settings.reset_val` fallback |

The `status` column in the SQL result reports which level the value came from (`OVERRIDE_ROLE_DB`, `OVERRIDE_ROLE`, `OVERRIDE_DATABASE`, `OVERRIDE_ALL`, `DEFAULT`) so the origin of any value is debuggable from the raw query.

### Caveats

- Level-5 fallback uses `pg_settings.reset_val`, which reflects what the *connecting* session would receive on `RESET` — including any `ALTER ROLE` overrides on the role pgdoctor itself connects as. To keep that fallback accurate for other roles, **run pgdoctor as a role with no overrides on the inspected settings**.
- Switching the cluster-default fallback to `pg_file_settings` + `boot_val` is a planned follow-up. It removes the caveat above but requires granting pgdoctor membership in the `pg_read_all_settings` predefined role, which is why it's intentionally out of scope for this check today.

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
-- For each application role
ALTER ROLE <role_name> SET statement_timeout = '3000ms';
ALTER ROLE <role_name> SET transaction_timeout = '3000ms';
ALTER ROLE <role_name> SET idle_in_transaction_session_timeout = '60000ms';
ALTER ROLE <role_name> SET log_min_duration_statement = '2000ms';
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
SELECT r.rolname, r.rolconfig
FROM pg_roles AS r
WHERE r.rolcanlogin = true
  AND r.rolsuper = false
  AND r.rolreplication = false
  AND r.rolname NOT LIKE 'pg_%';
```

### Important Notes

- Settings apply to **new connections only** - existing connections keep old values
- Consider application deployment to cycle connections
- Monitor application error rates after changes

## Library Configuration

When using pgdoctor as a library, you can configure roles and timeout thresholds:

```go
cfg := check.Config{
    "session-settings": {
        "roles":        "app_ro,app_rw",
        "timeout_warn": "2000",   // above this → WARN (default: 5000)
        "timeout_fail": "5000",   // above this → FAIL (default: 10000)
    },
}
pgdoctor.Run(ctx, conn, pgdoctor.Options{
    Config: cfg,
})
```

| Key | Description | Default |
|-----|-------------|---------|
| `roles` | Comma-separated list of roles to check | Discovered dynamically |
| `timeout_warn` | Threshold (ms) above which `statement_timeout` and `transaction_timeout` produce a WARN | `5000` |
| `timeout_fail` | Threshold (ms) above which `statement_timeout` and `transaction_timeout` produce a FAIL | `10000` |

When no config is provided, roles are discovered dynamically and default thresholds apply.

## References

- [PostgreSQL Documentation: ALTER ROLE](https://www.postgresql.org/docs/current/sql-alterrole.html)
- [PostgreSQL Documentation: Runtime Configuration](https://www.postgresql.org/docs/current/runtime-config-client.html)
