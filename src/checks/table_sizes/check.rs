use crate::checks::{Check, CheckCategory, CheckResult, CheckStatus, ValidationResult};
use anyhow::{Context, Result};
use async_trait::async_trait;
use tokio_postgres::Client;

pub struct TableSizesCheck;

impl TableSizesCheck {
    pub fn new() -> Self {
        Self
    }

    pub(crate) fn format_bytes(&self, bytes: i64) -> String {
        const KB: i64 = 1024;
        const MB: i64 = KB * 1024;
        const GB: i64 = MB * 1024;
        const TB: i64 = GB * 1024;

        if bytes >= TB {
            format!("{:.2} TB", bytes as f64 / TB as f64)
        } else if bytes >= GB {
            format!("{:.2} GB", bytes as f64 / GB as f64)
        } else if bytes >= MB {
            format!("{:.2} MB", bytes as f64 / MB as f64)
        } else if bytes >= KB {
            format!("{:.2} KB", bytes as f64 / KB as f64)
        } else {
            format!("{} bytes", bytes)
        }
    }

    pub(crate) fn validate_tables(&self, tables: Vec<(String, String, i64)>) -> Vec<ValidationResult> {
        let mut validations = vec![];

        const WARN_THRESHOLD: i64 = 10 * 1024 * 1024 * 1024; // 10 GB
        const CRITICAL_THRESHOLD: i64 = 50 * 1024 * 1024 * 1024; // 50 GB

        if tables.is_empty() {
            validations.push(ValidationResult {
                name: "table_count".to_string(),
                status: CheckStatus::Ok,
                message: "No tables found in the database.".to_string(),
            });
            return validations;
        }

        let total_size: i64 = tables.iter().map(|(_, _, size)| size).sum();
        validations.push(ValidationResult {
            name: "total_size".to_string(),
            status: CheckStatus::Ok,
            message: format!(
                "Total database size: {} across {} table(s)",
                self.format_bytes(total_size),
                tables.len()
            ),
        });

        for (schema, table, size) in tables {
            if size >= CRITICAL_THRESHOLD {
                validations.push(ValidationResult {
                    name: format!("table_size_{}.{}", schema, table),
                    status: CheckStatus::Critical,
                    message: format!(
                        "Table {}.{} is very large: {}. Consider partitioning or archiving.",
                        schema,
                        table,
                        self.format_bytes(size)
                    ),
                });
            } else if size >= WARN_THRESHOLD {
                validations.push(ValidationResult {
                    name: format!("table_size_{}.{}", schema, table),
                    status: CheckStatus::Warn,
                    message: format!(
                        "Table {}.{} is large: {}. Monitor growth and consider optimization.",
                        schema,
                        table,
                        self.format_bytes(size)
                    ),
                });
            }
        }

        if validations.len() == 1 {
            validations.push(ValidationResult {
                name: "large_tables".to_string(),
                status: CheckStatus::Ok,
                message: "No unusually large tables detected.".to_string(),
            });
        }

        validations
    }
}

#[async_trait]
impl Check for TableSizesCheck {
    fn id(&self) -> &str {
        "table_sizes"
    }

    fn name(&self) -> &str {
        "Table Sizes Check"
    }

    fn category(&self) -> CheckCategory {
        CheckCategory::Storage
    }

    async fn run(&self, client: &Client) -> Result<CheckResult> {
        let query = include_str!("query.sql");

        let rows = client
            .query(query, &[])
            .await
            .context("Failed to query table sizes")?;

        let mut tables = vec![];
        for row in rows {
            let schema: String = row.get(0);
            let table: String = row.get(1);
            let size: i64 = row.get(2);
            tables.push((schema, table, size));
        }

        let validations = self.validate_tables(tables);

        Ok(CheckResult {
            check_id: self.id().to_string(),
            check_name: self.name().to_string(),
            category: self.category(),
            validations,
        })
    }
}
