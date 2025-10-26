pub mod version;
pub mod table_sizes;
pub mod vacuum_settings;

use anyhow::Result;
use tokio_postgres::Client;

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
