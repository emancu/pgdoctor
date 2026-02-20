# Duplicate Indexes

Identifies exact and prefix duplicate indexes that waste disk space and slow down write operations without providing additional query benefits.

## What It Checks

### 1. Exact Duplicates
Indexes with identical definitions on the same table. These are completely redundant and one should always be dropped.

**Severity**: FAIL

**Example**:
```sql
CREATE INDEX idx_users_email ON users(email);
CREATE INDEX idx_users_email_duplicate ON users(email);  -- Exact duplicate!
```

### 2. Prefix Duplicates
Indexes where one is a left-prefix of another. PostgreSQL can use multi-column indexes for queries on leading columns, making the shorter index often redundant.

**Severity**:
- FAIL: Prefix index > 100 MB
- WARN: Any prefix relationship

**Example**:
```sql
CREATE INDEX idx_orders_customer ON orders(customer_id);
CREATE INDEX idx_orders_customer_date ON orders(customer_id, created_at);
-- First index is redundant - the second can satisfy queries on customer_id alone
```

## Important Considerations

### When Prefix Indexes Are NOT Duplicates

**Expression Indexes**:
```sql
CREATE INDEX idx_users_email_lower ON users(lower(email));
CREATE INDEX idx_users_email ON users(email);
-- NOT duplicates - different semantics
```

**Partial Indexes**:
```sql
CREATE INDEX idx_active_users ON users(status) WHERE status = 'active';
CREATE INDEX idx_all_users_status ON users(status);
-- NOT duplicates - different row sets
```

This check excludes expression and partial indexes from prefix detection.

### When to Keep Prefix Indexes

In rare cases, a prefix index may be intentionally kept for:
1. **Covering queries**: Smaller index fits more entries per page
2. **Query optimizer hints**: Specific query patterns benefit from simpler index
3. **Lock contention**: Smaller index reduces lock contention in specific workloads

Investigate before dropping.

## How to Fix

### For `exact-duplicates`

Exact duplicate indexes have no valid purpose and waste resources.

```sql
-- Compare definitions to choose which to drop
\d+ schema.table_name

-- Drop the duplicate (keep the one with the better name)
DROP INDEX CONCURRENTLY schema.duplicate_index_name;
```

### For `prefix-duplicates`

The shorter index columns are a prefix of the longer index. PostgreSQL can use the longer index for queries on leading columns, making the shorter index often redundant.

**Before dropping:**
1. Verify the shorter index is truly redundant:
   ```sql
   SELECT idx_scan FROM pg_stat_user_indexes WHERE indexrelname = 'shorter_index';
   ```
2. Check if it's a unique constraint (cannot drop)
3. Consider keeping if it has different fillfactor or serves covering queries

```sql
-- Drop the redundant prefix index
DROP INDEX CONCURRENTLY schema.prefix_index_name;
```

### Preventing Duplicates

Add checks to migration reviews:
- Search for existing indexes before creating new ones
- Document the purpose of indexes in migrations
- Use naming conventions that reflect index structure

## Query Details

Queries `pg_index`, `pg_class`, and `pg_attribute` to compare index definitions and detect structural duplication using column position arrays (`indkey`).
