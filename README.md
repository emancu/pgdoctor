# pgdoctor - The opinionated check-up your database deserves

A command-line tool written in Rust to detect and diagnose the health of PostgreSQL databases.

## Features

- **Modular Check System**: Each check is isolated and testable
- **Categorized Checks**: Checks are organized into categories (performance, storage, indexes, settings, architecture)
- **Flexible Filtering**: Include/exclude specific checks or filter by category
- **Status-based Results**: Each check returns OK, WARN, or CRITICAL status
- **Exit Codes**: Returns appropriate exit codes for CI/CD integration (0=OK, 1=WARN, 2=CRITICAL)

## Installation

```bash
cargo build --release
```

The binary will be available at `target/release/pgdoctor`.

## Usage

Basic usage:

```bash
pgdoctor --connection "postgresql://user:password@localhost/dbname"
```

### Connection String Format

The connection string follows the PostgreSQL URI format:

```
postgresql://[user[:password]@][host][:port][/dbname][?param1=value1&...]
```

Examples:
- `postgresql://postgres:password@localhost/mydb`
- `postgresql://user@localhost:5432/production`
- `postgresql://user:pass@db.example.com/analytics?sslmode=require`

### Filtering Checks

Include only specific checks:

```bash
pgdoctor --connection "postgresql://..." --include pg_version,table_sizes
```

Exclude specific checks:

```bash
pgdoctor --connection "postgresql://..." --exclude table_sizes
```

Filter by category:

```bash
pgdoctor --connection "postgresql://..." --categories storage,settings
```

## Available Checks

### 1. PostgreSQL Version Check
- **ID**: `pg_version`
- **Category**: `settings`
- **Description**: Checks the PostgreSQL version and warns about unsupported versions

**Validations**:
- CRITICAL: Version < 10 (end-of-life)
- WARN: Version < 12 (approaching end-of-life)
- OK: Version >= 12

[Full documentation](src/checks/version/README.md)

### 2. Table Sizes Check
- **ID**: `table_sizes`
- **Category**: `storage`
- **Description**: Analyzes all tables in the database and their sizes

**Validations**:
- Reports total database size
- WARN: Tables > 10 GB
- CRITICAL: Tables > 50 GB

[Full documentation](src/checks/table_sizes/README.md)

### 3. Vacuum Settings Check
- **ID**: `vacuum_settings`
- **Category**: `performance`
- **Description**: Validates PostgreSQL vacuum and memory configuration settings

**Validations**:
- Autovacuum scale factors (analyze and vacuum)
- Autovacuum max workers
- Maintenance work memory
- Vacuum cost settings (delay and limit)
- Work memory

[Full documentation](src/checks/vacuum_settings/README.md)

## Architecture

The codebase is organized for extensibility and maintainability. Each check is self-contained in its own directory with all related files:

```
src/
├── main.rs              # Entry point and check orchestration
├── cli.rs               # Command-line argument parsing
├── db.rs                # Database connection handling
├── output.rs            # Result formatting and display
└── checks/
    ├── mod.rs           # Check trait and common types
    ├── version/
    │   ├── mod.rs       # Module exports
    │   ├── check.rs     # Implementation
    │   ├── query.sql    # SQL query
    │   ├── tests.rs     # Unit tests
    │   └── README.md    # Check documentation
    ├── table_sizes/
    │   ├── mod.rs
    │   ├── check.rs
    │   ├── query.sql
    │   ├── tests.rs
    │   └── README.md
    └── vacuum_settings/
        ├── mod.rs
        ├── check.rs
        ├── query.sql
        ├── tests.rs
        └── README.md
```

### Check Structure

Each check follows a consistent structure:

1. **check.rs** - Implementation of the check logic and validations
2. **query.sql** - Single SQL query that extracts data from PostgreSQL
3. **tests.rs** - Unit tests for the check logic
4. **README.md** - Detailed documentation, troubleshooting guides, and references

This structure ensures:
- **Isolation**: Each check is self-contained and easy to understand
- **Testability**: Clear separation between SQL queries and validation logic
- **Documentation**: Every check has comprehensive documentation
- **Maintainability**: Easy to add, modify, or remove checks
- **Collaboration**: Multiple developers can work on different checks without conflicts

### Adding New Checks

To add a new check:

1. Create a new directory in `src/checks/` (e.g., `my_check/`)

2. Create `query.sql` with your SQL query:
```sql
SELECT column1, column2 FROM pg_table WHERE condition;
```

3. Create `check.rs` with the implementation:
```rust
use crate::checks::{Check, CheckCategory, CheckResult, CheckStatus, ValidationResult};
use anyhow::{Context, Result};
use async_trait::async_trait;
use tokio_postgres::Client;

pub struct MyCheck;

impl MyCheck {
    pub fn new() -> Self {
        Self
    }

    fn validate_data(&self, data: Vec<String>) -> Vec<ValidationResult> {
        // Your validation logic here
        vec![ValidationResult {
            name: "my_validation".to_string(),
            status: CheckStatus::Ok,
            message: "Everything looks good!".to_string(),
        }]
    }
}

#[async_trait]
impl Check for MyCheck {
    fn id(&self) -> &str {
        "my_check"
    }

    fn name(&self) -> &str {
        "My Check"
    }

    fn category(&self) -> CheckCategory {
        CheckCategory::Performance
    }

    async fn run(&self, client: &Client) -> Result<CheckResult> {
        let query = include_str!("query.sql");

        let rows = client.query(query, &[]).await
            .context("Failed to query data")?;

        // Process rows and validate
        let data: Vec<String> = rows.iter().map(|row| row.get(0)).collect();
        let validations = self.validate_data(data);

        Ok(CheckResult {
            check_id: self.id().to_string(),
            check_name: self.name().to_string(),
            category: self.category(),
            validations,
        })
    }
}
```

4. Create `tests.rs` with unit tests:
```rust
#[cfg(test)]
mod tests {
    use super::super::check::MyCheck;
    use crate::checks::CheckStatus;

    #[test]
    fn test_validate_data() {
        let check = MyCheck::new();
        let data = vec!["test".to_string()];
        let validations = check.validate_data(data);

        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Ok);
    }
}
```

5. Create `mod.rs`:
```rust
mod check;
#[cfg(test)]
mod tests;

pub use check::MyCheck;
```

6. Create `README.md` with documentation (see existing checks for examples)

7. Add the module to `src/checks/mod.rs`:
```rust
pub mod my_check;
```

8. Register the check in `src/main.rs`:
```rust
let all_checks: Vec<Box<dyn Check>> = vec![
    Box::new(VersionCheck::new()),
    Box::new(TableSizesCheck::new()),
    Box::new(VacuumSettingsCheck::new()),
    Box::new(MyCheck::new()),  // Add your check here
];
```

## Testing

Each check receives an open database connection, making it easy to test with different database states:

```rust
#[tokio::test]
async fn test_my_check() {
    let client = connect_to_test_db().await.unwrap();
    let check = MyCheck::new();
    let result = check.run(&client).await.unwrap();

    assert_eq!(result.overall_status(), CheckStatus::Ok);
}
```

## Exit Codes

- `0`: All checks passed (OK)
- `1`: At least one check returned WARN
- `2`: At least one check returned CRITICAL

## License

MIT

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.
