# PostgreSQL Version Check

Verifies that PostgreSQL databases are running a supported major version, requiring PostgreSQL 15 or higher.

## What it checks

- PostgreSQL major version (must be 15+)
- Version support lifecycle and end-of-life status

## Why it matters

Running outdated PostgreSQL versions poses serious risks:
- **Security vulnerabilities**: Older versions lack critical security patches
- **Missing features**: Newer versions provide essential performance improvements
- **Support lifecycle**: Unsupported versions receive no bug fixes or updates
- **Infrastructure compatibility**: Modern tools and extensions require recent versions

PostgreSQL 15+ provides critical features for modern infrastructure:
- **Faster WAL decoding**: Improves CDC pipeline performance
- **Replication slots from replicas**: Enables high-availability architectures
- **Enhanced logical replication**: Supports IDENTITY REPLICA FULL for better data integrity
- **Performance improvements**: Query optimization and resource management
- **Better monitoring**: Improved observability and diagnostic tools

Staying on old versions leads to:
- Accumulating technical debt
- Increasingly difficult and risky upgrades
- Security compliance issues
- Performance degradation compared to modern versions

## How to Fix

### For `pg-version`

Upgrade PostgreSQL using your preferred upgrade strategy:

1. **Review**: Check the [PostgreSQL release notes](https://www.postgresql.org/docs/release/) for your target version

2. **Upgrade options**:
   - **pg_upgrade**: In-place major version upgrade (requires downtime)
   - **Logical replication**: Set up replication to a new version instance for zero-downtime migration
   - **Managed service tools**: Use your cloud provider's upgrade tooling (e.g., AWS RDS, GCP Cloud SQL)

3. **Monitor** application behavior after upgrade

### Pre-Upgrade Checklist

Before upgrading:
- [ ] Review [PostgreSQL release notes](https://www.postgresql.org/docs/release/) for your versions
- [ ] Check application compatibility with PG15/16/17/18
- [ ] Review extension compatibility (PostGIS, pgvector, etc.)
- [ ] Verify replication setup will work with new version
- [ ] Plan maintenance window (even for zero-downtime upgrades)
- [ ] Prepare rollback plan
- [ ] Remove unnecessary replicas
- [ ] Use the opportunity to change instance size/family

### Post-Upgrade Steps

After upgrading:
1. Verify application functionality
2. Monitor query performance for regressions
3. Check replication lag and slot health

## References

- [PostgreSQL Versioning Policy](https://www.postgresql.org/support/versioning/)
- [PostgreSQL Release Notes](https://www.postgresql.org/docs/release/)
- [pg_upgrade Documentation](https://www.postgresql.org/docs/current/pgupgrade.html)
