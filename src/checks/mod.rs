pub mod version;
pub mod table_sizes;
pub mod vacuum_settings;

use anyhow::Result;
use tokio_postgres::Client;
use tokio_postgres::types::ToSql;
use time::{Duration, OffsetDateTime};

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CheckStatus {
    Ok,
    Warn,
    Critical,
}

impl std::fmt::Display for CheckStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CheckStatus::Ok => write!(f, "OK"),
            CheckStatus::Warn => write!(f, "WARN"),
            CheckStatus::Critical => write!(f, "CRITICAL"),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum CheckCategory {
    Performance,
    Storage,
    Indexes,
    Settings,
    Architecture,
}

impl std::fmt::Display for CheckCategory {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CheckCategory::Performance => write!(f, "performance"),
            CheckCategory::Storage => write!(f, "storage"),
            CheckCategory::Indexes => write!(f, "indexes"),
            CheckCategory::Settings => write!(f, "settings"),
            CheckCategory::Architecture => write!(f, "architecture"),
        }
    }
}

#[derive(Debug, Clone)]
pub struct ValidationResult {
    pub name: String,
    pub status: CheckStatus,
    pub message: String,
}

#[derive(Debug, Clone)]
pub struct CheckResult {
    pub check_id: String,
    pub check_name: String,
    pub category: CheckCategory,
    pub validations: Vec<ValidationResult>,
}

impl CheckResult {
    pub fn overall_status(&self) -> CheckStatus {
        let has_critical = self.validations.iter().any(|v| v.status == CheckStatus::Critical);
        let has_warn = self.validations.iter().any(|v| v.status == CheckStatus::Warn);

        if has_critical {
            CheckStatus::Critical
        } else if has_warn {
            CheckStatus::Warn
        } else {
            CheckStatus::Ok
        }
    }
}

#[async_trait::async_trait]
pub trait Check: Send + Sync {
    fn id(&self) -> &str;
    fn name(&self) -> &str;
    fn category(&self) -> CheckCategory;
    async fn run(&self, client: &Client) -> Result<CheckResult>;
}

/// Converts a byte count into a human-readable string (e.g., "1.23 MB").
fn bytes_to_human_readable(bytes: i64) -> String {
    if bytes == 0 {
        return "0 B".to_string();
    }
    let negative = bytes < 0;
    let bytes = bytes.abs() as f64;

    let units = ["B", "KB", "MB", "GB", "TB", "PB", "EB"];
    let i = (bytes.log2() / 10.0).floor() as usize;
    let converted_value = bytes / (1024_f64.powi(i as i32));

    let sign = if negative { "-" } else { "" };
    format!("{sign}{converted_value:.2} {}", units[i.min(units.len() - 1)])
}

/// Represents detailed information about table bloat for a single table.
#[derive(Debug, Clone)]
pub struct TableBloatInfo {
    pub schema_name: String,
    pub table_name: String,
    pub table_size: i64, // in bytes
    pub bloat_size: i64, // in bytes
    pub bloat_percentage: f64,
    pub last_autovacuum: Option<OffsetDateTime>,
    pub last_autoanalyze: Option<OffsetDateTime>,
}

/// Fetches bloat data for tables in the 'public' schema from the PostgreSQL database.
///
/// This function executes a SQL query to estimate table bloat based on actual table size
/// and an estimated ideal size derived from average row data width and tuple overhead.
/// It returns a vector of `TableBloatInfo` for tables identified as having bloat.
async fn fetch_table_bloat_data(client: &Client) -> Result<Vec<TableBloatInfo>> {
    // This query estimates table bloat by comparing the actual table size with an
    // estimated ideal size. The ideal size is calculated based on the number of
    // live tuples, their estimated average data width (from pg_statistic),
    // and a typical tuple overhead (header + item_id + alignment).
    // It's important to cast reltuples (a `real`) to bigint to avoid float inaccuracies.
    let query = "
        WITH table_data AS (
            SELECT
                c.oid AS table_oid,
                n.nspname AS schema_name,
                c.relname AS table_name,
                c.reltuples AS live_tuples,
                pg_relation_size(c.oid) AS actual_table_size_bytes,
                (
                    SELECT COALESCE(SUM(s.avg_width), 0)
                    FROM pg_attribute a
                    JOIN pg_statistic s ON s.starelid = a.attrelid AND s.staattnum = a.attnum
                    WHERE a.attrelid = c.oid
                      AND a.attnum > 0
                      AND NOT a.attisdropped
                ) AS estimated_avg_row_data_width_bytes,
                stat.last_autovacuum,
                stat.last_autoanalyze
            FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            LEFT JOIN pg_stat_user_tables stat ON stat.relid = c.oid
            WHERE c.relkind = 'r' -- Only regular tables
              AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
              AND n.nspname = 'public' -- Specific to public schema
              AND c.reltuples > 0 -- Only tables with rows for average width calculation sanity
              AND pg_relation_size(c.oid) > 0 -- Only tables with actual data size
        )
        SELECT
            td.schema_name,
            td.table_name,
            td.actual_table_size_bytes,
            td.last_autovacuum,
            td.last_autoanalyze,
            -- Bloat calculation: Actual size - (estimated_rows * (estimated_avg_row_data_width + tuple_overhead))
            -- Tuple overhead is approx 23 bytes (HeapTupleHeaderData) + 4 bytes (ItemIdData) = 27 bytes.
            -- Add padding for alignment, usually 8 bytes. So, overhead is ~35 bytes.
            CASE
                WHEN td.live_tuples > 0 AND td.estimated_avg_row_data_width_bytes > 0 THEN
                    GREATEST(0, td.actual_table_size_bytes - (
                        td.live_tuples::bigint * (td.estimated_avg_row_data_width_bytes + 35)
                    ))
                ELSE 0
            END AS bloat_size_bytes,
            CASE
                WHEN td.actual_table_size_bytes > 0 AND td.live_tuples > 0 AND td.estimated_avg_row_data_width_bytes > 0 THEN
                    ROUND(
                        GREATEST(0, td.actual_table_size_bytes - (
                            td.live_tuples::bigint * (td.estimated_avg_row_data_width_bytes + 35)
                        ))::numeric / td.actual_table_size_bytes::numeric * 100,
                        2
                    )
                ELSE 0
            END AS bloat_percentage
        FROM table_data td
        WHERE td.actual_table_size_bytes > 0 AND
              CASE
                  WHEN td.live_tuples > 0 AND td.estimated_avg_row_data_width_bytes > 0 THEN
                      GREATEST(0, td.actual_table_size_bytes - (td.live_tuples::bigint * (td.estimated_avg_row_data_width_bytes + 35)))
                  ELSE 0
              END > 0 -- Only show tables with actual bloat
        ORDER BY bloat_size_bytes DESC;
    ";

    let rows = client.query(query, &[] as &[&(dyn ToSql + Sync)]).await?;
    let mut bloat_info_list = Vec::new();

    for row in rows {
        bloat_info_list.push(TableBloatInfo {
            schema_name: row.get("schema_name"),
            table_name: row.get("table_name"),
            table_size: row.get("actual_table_size_bytes"),
            bloat_size: row.get("bloat_size_bytes"),
            bloat_percentage: row.get("bloat_percentage"),
            last_autovacuum: row.get("last_autovacuum"),
            last_autoanalyze: row.get("last_autoanalyze"),
        });
    }

    Ok(bloat_info_list)
}

/// Check for tables with significant bloat that haven't been vacuumed or analyzed recently.
#[derive(Default)]
pub struct TableBloatCheck;

#[async_trait::async_trait]
impl Check for TableBloatCheck {
    fn id(&self) -> &str {
        "table_bloat"
    }

    fn name(&self) -> &str {
        "Table Bloat"
    }

    fn category(&self) -> CheckCategory {
        CheckCategory::Storage
    }

    async fn run(&self, client: &Client) -> Result<CheckResult> {
        let bloat_data = fetch_table_bloat_data(client).await?;
        let mut validations = Vec::new();
        let five_days_ago = OffsetDateTime::now_utc() - Duration::days(5);

        for info in bloat_data {
            let is_bloated = info.bloat_percentage > 60.0;
            // If last autovacuum/analyze is None, it has never run, which we consider stale.
            let is_autovacuum_stale = info.last_autovacuum.map_or(true, |t| t < five_days_ago);
            let is_autoanalyze_stale = info.last_autoanalyze.map_or(true, |t| t < five_days_ago);

            if is_bloated && (is_autovacuum_stale || is_autoanalyze_stale) {
                let table_id = format!("{}.{}", info.schema_name, info.table_name);
                let message = format!(
                    "Table '{}' has high bloat ({:.2}%, {}) and may need maintenance. Last autovacuum: {}, last autoanalyze: {}.",
                    table_id,
                    info.bloat_percentage,
                    bytes_to_human_readable(info.bloat_size),
                    info.last_autovacuum.map_or_else(|| "never".to_string(), |t| t.date().to_string()),
                    info.last_autoanalyze.map_or_else(|| "never".to_string(), |t| t.date().to_string())
                );
                validations.push(ValidationResult {
                    name: table_id,
                    status: CheckStatus::Warn,
                    message,
                });
            }
        }

        Ok(CheckResult {
            check_id: self.id().to_string(),
            check_name: self.name().to_string(),
            category: self.category(),
            validations,
        })
    }
}