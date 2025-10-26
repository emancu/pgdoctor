use crate::checks::{Check, CheckCategory, CheckResult, CheckStatus, ValidationResult};
use anyhow::{Context, Result};
use async_trait::async_trait;
use tokio_postgres::Client;

pub struct VersionCheck;

impl VersionCheck {
    pub fn new() -> Self {
        Self
    }

    pub(crate) fn parse_version(&self, version_string: &str) -> Option<i32> {
        // PostgreSQL version string looks like: "PostgreSQL 15.3 on ..."
        // Extract the major version number
        version_string
            .split_whitespace()
            .nth(1)
            .and_then(|v| v.split('.').next())
            .and_then(|v| v.parse::<i32>().ok())
    }

    pub(crate) fn validate_version(&self, version_string: String) -> Vec<ValidationResult> {
        let mut validations = vec![];

        let major_version = self.parse_version(&version_string);

        if let Some(version) = major_version {
            if version < 10 {
                validations.push(ValidationResult {
                    name: "version_check".to_string(),
                    status: CheckStatus::Critical,
                    message: format!(
                        "PostgreSQL version {} is end-of-life and unsupported. Please upgrade immediately.",
                        version
                    ),
                });
            } else if version < 12 {
                validations.push(ValidationResult {
                    name: "version_check".to_string(),
                    status: CheckStatus::Warn,
                    message: format!(
                        "PostgreSQL version {} is approaching end-of-life. Consider upgrading.",
                        version
                    ),
                });
            } else {
                validations.push(ValidationResult {
                    name: "version_check".to_string(),
                    status: CheckStatus::Ok,
                    message: format!("PostgreSQL version {} is supported.", version),
                });
            }
        } else {
            validations.push(ValidationResult {
                name: "version_check".to_string(),
                status: CheckStatus::Warn,
                message: format!("Could not parse version from: {}", version_string),
            });
        }

        validations
    }
}

#[async_trait]
impl Check for VersionCheck {
    fn id(&self) -> &str {
        "pg_version"
    }

    fn name(&self) -> &str {
        "PostgreSQL Version Check"
    }

    fn category(&self) -> CheckCategory {
        CheckCategory::Settings
    }

    async fn run(&self, client: &Client) -> Result<CheckResult> {
        let query = include_str!("query.sql");

        let row = client
            .query_one(query, &[])
            .await
            .context("Failed to query PostgreSQL version")?;

        let version_string: String = row.get(0);
        let validations = self.validate_version(version_string);

        Ok(CheckResult {
            check_id: self.id().to_string(),
            check_name: self.name().to_string(),
            category: self.category(),
            validations,
        })
    }
}
