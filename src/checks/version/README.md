# PostgreSQL Version Check

**ID**: `pg_version`
**Category**: `settings`

## Description

This check validates that your PostgreSQL server is running a supported version. Running unsupported versions poses security risks and may lack critical bug fixes and performance improvements.

## Query

```sql
SELECT version()
```

This query returns the full PostgreSQL version string, including the version number and platform information.

## Validations

### 1. Version Support Status

Checks if the PostgreSQL major version is still supported:

- **CRITICAL**: Version < 10
  - These versions are end-of-life and no longer receive security updates
  - Immediate upgrade is strongly recommended

- **WARN**: Version < 12
  - These versions are approaching end-of-life
  - Plan an upgrade in the near future

- **OK**: Version >= 12
  - Version is currently supported by the PostgreSQL community

## Example Output

### Healthy Database (PG 15)
```
✓ [OK] PostgreSQL version 15 is supported.
```

### Warning (PG 11)
```
⚠ [WARN] PostgreSQL version 11 is approaching end-of-life. Consider upgrading.
```

### Critical (PG 9)
```
✗ [CRITICAL] PostgreSQL version 9 is end-of-life and unsupported. Please upgrade immediately.
```

## Troubleshooting

### How to check your PostgreSQL version manually

```bash
psql -c "SELECT version();"
```

Or from within psql:
```sql
SELECT version();
```

### How to upgrade PostgreSQL

The upgrade process depends on your installation method and operating system:

- **pg_upgrade**: For in-place major version upgrades
- **pg_dump/pg_restore**: For logical migration
- **Replication**: For zero-downtime upgrades

**Important**: Always test upgrades in a non-production environment first and ensure you have valid backups.

## References

- [PostgreSQL Versioning Policy](https://www.postgresql.org/support/versioning/)
- [PostgreSQL Release Support Schedule](https://www.postgresql.org/support/versioning/)
- [pg_upgrade Documentation](https://www.postgresql.org/docs/current/pgupgrade.html)
- [PostgreSQL Release Notes](https://www.postgresql.org/docs/release/)
