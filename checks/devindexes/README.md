# Development Indexes

Identifies temporary development indexes (prefixed with `_dev`) that should either be promoted to permanent indexes or dropped.

## What It Checks

### Development Index Usage

Finds all indexes whose names start with `_dev` and categorizes them by usage:

**Used Development Indexes** (WARN):
- Indexes with ≥ 1,000 scans
- Should be promoted to permanent indexes in schema migrations

**Unused Development Indexes** (WARN):
- Indexes with < 1,000 scans
- Should be dropped to reduce maintenance overhead

## Why Development Indexes Matter

### Purpose of _dev Prefix

Development indexes are temporary indexes created for:
- Testing query performance improvements
- Experimenting with index strategies
- Quick debugging of slow queries
- Prototyping new features

The `_dev` prefix convention helps identify these temporary indexes.

### Problems with Leaving Them in Production

1. **Unclear ownership**: No one knows if they're still needed
2. **No version control**: Not tracked in schema migrations
3. **Maintenance cost**: Slow down writes without documented benefits
4. **Deployment issues**: Missing on replicas or new environments
5. **Confusion**: Future developers don't know their purpose

## How to Fix

### For `used-dev-indexes`

These development indexes have significant usage and should be promoted to permanent indexes.

**To promote a development index:**

1. Add the index to your schema migrations with a proper name
2. Create the permanent index using CONCURRENTLY
3. Drop the development index after verifying the permanent one works

```sql
-- In a migration file
CREATE INDEX CONCURRENTLY idx_users_email ON users(email);

-- After deployment, drop the dev index
DROP INDEX CONCURRENTLY _dev_users_email;
```

Development indexes are temporary and should not remain in production.

### For `unused-dev-indexes`

These development indexes have minimal usage and can likely be dropped.

```sql
-- Drop unused development indexes
DROP INDEX CONCURRENTLY _dev_index_name;
```

**Before dropping:**
- Verify the index isn't needed for specific queries
- Check if the index was created for testing and is no longer relevant
- Confirm no other indexes cover the same columns

Development indexes are meant for temporary testing and should be cleaned up.

### Prevention

Establish team conventions:
1. **Document purpose**: Always add comments when creating dev indexes
2. **Time limit**: Review and clean up dev indexes weekly
3. **Naming**: Use `_dev_<feature>_<date>` format (e.g., `_dev_email_search_20250115`)
4. **Code reviews**: Flag dev indexes in production deployments

### Local Development

For local experimentation:
```sql
-- Create temporary index for testing
CREATE INDEX CONCURRENTLY _dev_test_email_idx ON users(email);

-- Test queries...

-- Clean up when done
DROP INDEX CONCURRENTLY _dev_test_email_idx;
```

## Query Details

Queries `pg_class`, `pg_index`, and `pg_stat_user_indexes` to find indexes with names starting with `_dev`, along with their usage statistics.
