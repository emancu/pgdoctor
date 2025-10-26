use crate::checks::{Check, CheckCategory, CheckResult, CheckStatus, ValidationResult};
use anyhow::{Context, Result};
use async_trait::async_trait;
use std::collections::HashMap;
use tokio_postgres::Client;

pub struct VacuumSettingsCheck;

impl VacuumSettingsCheck {
    pub fn new() -> Self {
        Self
    }

    pub(crate) fn parse_setting_value(&self, setting: &str, unit: Option<&str>) -> Option<i64> {
        let value = setting.parse::<i64>().ok()?;

        // Convert to base unit (bytes for memory, milliseconds for time)
        match unit {
            Some("kB") => Some(value * 1024),
            Some("MB") => Some(value * 1024 * 1024),
            Some("GB") => Some(value * 1024 * 1024 * 1024),
            Some("ms") => Some(value),
            Some("s") => Some(value * 1000),
            _ => Some(value), // No unit or dimensionless
        }
    }

    pub(crate) fn parse_float_setting(&self, setting: &str) -> Option<f64> {
        setting.parse::<f64>().ok()
    }

    pub(crate) fn check_autovacuum_scale_factors(&self, settings: &HashMap<String, (String, Option<String>)>) -> Vec<ValidationResult> {
        let mut validations = vec![];

        if let Some((analyze_factor_str, _)) = settings.get("autovacuum_analyze_scale_factor") {
            if let Some(analyze_factor) = self.parse_float_setting(analyze_factor_str) {
                if analyze_factor > 0.1 {
                    validations.push(ValidationResult {
                        name: "autovacuum_analyze_scale_factor".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "autovacuum_analyze_scale_factor is {}. Values > 0.1 may delay ANALYZE on large tables, affecting query planning.",
                            analyze_factor
                        ),
                    });
                } else {
                    validations.push(ValidationResult {
                        name: "autovacuum_analyze_scale_factor".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("autovacuum_analyze_scale_factor is {} (optimal).", analyze_factor),
                    });
                }
            }
        }

        if let Some((vacuum_factor_str, _)) = settings.get("autovacuum_vacuum_scale_factor") {
            if let Some(vacuum_factor) = self.parse_float_setting(vacuum_factor_str) {
                if vacuum_factor > 0.2 {
                    validations.push(ValidationResult {
                        name: "autovacuum_vacuum_scale_factor".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "autovacuum_vacuum_scale_factor is {}. Values > 0.2 may cause bloat in large tables.",
                            vacuum_factor
                        ),
                    });
                } else if vacuum_factor > 0.1 {
                    validations.push(ValidationResult {
                        name: "autovacuum_vacuum_scale_factor".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("autovacuum_vacuum_scale_factor is {} (acceptable).", vacuum_factor),
                    });
                } else {
                    validations.push(ValidationResult {
                        name: "autovacuum_vacuum_scale_factor".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("autovacuum_vacuum_scale_factor is {} (optimal).", vacuum_factor),
                    });
                }
            }
        }

        validations
    }

    pub(crate) fn check_autovacuum_workers(&self, settings: &HashMap<String, (String, Option<String>)>) -> Vec<ValidationResult> {
        let mut validations = vec![];

        if let Some((workers_str, unit)) = settings.get("autovacuum_max_workers") {
            if let Some(workers) = self.parse_setting_value(workers_str, unit.as_deref()) {
                if workers < 3 {
                    validations.push(ValidationResult {
                        name: "autovacuum_max_workers".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "autovacuum_max_workers is {}. Consider increasing to at least 3 for better concurrent vacuum performance.",
                            workers
                        ),
                    });
                } else if workers > 10 {
                    validations.push(ValidationResult {
                        name: "autovacuum_max_workers".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "autovacuum_max_workers is {}. Very high values may cause resource contention.",
                            workers
                        ),
                    });
                } else {
                    validations.push(ValidationResult {
                        name: "autovacuum_max_workers".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("autovacuum_max_workers is {} (optimal).", workers),
                    });
                }
            }
        }

        validations
    }

    pub(crate) fn check_maintenance_work_mem(&self, settings: &HashMap<String, (String, Option<String>)>) -> Vec<ValidationResult> {
        let mut validations = vec![];

        if let Some((mem_str, unit)) = settings.get("maintenance_work_mem") {
            if let Some(mem_bytes) = self.parse_setting_value(mem_str, unit.as_deref()) {
                let mem_mb = mem_bytes / (1024 * 1024);

                if mem_mb < 64 {
                    validations.push(ValidationResult {
                        name: "maintenance_work_mem".to_string(),
                        status: CheckStatus::Critical,
                        message: format!(
                            "maintenance_work_mem is {} MB. This is too low and will significantly slow down VACUUM and index creation. Increase to at least 256 MB.",
                            mem_mb
                        ),
                    });
                } else if mem_mb < 256 {
                    validations.push(ValidationResult {
                        name: "maintenance_work_mem".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "maintenance_work_mem is {} MB. Consider increasing to at least 256 MB for better maintenance performance.",
                            mem_mb
                        ),
                    });
                } else if mem_mb > 2048 {
                    validations.push(ValidationResult {
                        name: "maintenance_work_mem".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "maintenance_work_mem is {} MB. Very high values (>2GB) may not provide additional benefits.",
                            mem_mb
                        ),
                    });
                } else {
                    validations.push(ValidationResult {
                        name: "maintenance_work_mem".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("maintenance_work_mem is {} MB (optimal).", mem_mb),
                    });
                }
            }
        }

        validations
    }

    pub(crate) fn check_vacuum_cost_settings(&self, settings: &HashMap<String, (String, Option<String>)>) -> Vec<ValidationResult> {
        let mut validations = vec![];

        if let Some((delay_str, unit)) = settings.get("vacuum_cost_delay") {
            if let Some(delay_ms) = self.parse_setting_value(delay_str, unit.as_deref()) {
                if delay_ms > 10 {
                    validations.push(ValidationResult {
                        name: "vacuum_cost_delay".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "vacuum_cost_delay is {} ms. High values slow down vacuum. Consider reducing if vacuum is not keeping up.",
                            delay_ms
                        ),
                    });
                } else {
                    validations.push(ValidationResult {
                        name: "vacuum_cost_delay".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("vacuum_cost_delay is {} ms (acceptable).", delay_ms),
                    });
                }
            }
        }

        if let Some((limit_str, unit)) = settings.get("vacuum_cost_limit") {
            if let Some(limit) = self.parse_setting_value(limit_str, unit.as_deref()) {
                if limit < 200 {
                    validations.push(ValidationResult {
                        name: "vacuum_cost_limit".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "vacuum_cost_limit is {}. Low values throttle vacuum too much. Consider increasing to at least 200.",
                            limit
                        ),
                    });
                } else {
                    validations.push(ValidationResult {
                        name: "vacuum_cost_limit".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("vacuum_cost_limit is {} (acceptable).", limit),
                    });
                }
            }
        }

        validations
    }

    pub(crate) fn check_work_mem(&self, settings: &HashMap<String, (String, Option<String>)>) -> Vec<ValidationResult> {
        let mut validations = vec![];

        if let Some((mem_str, unit)) = settings.get("work_mem") {
            if let Some(mem_bytes) = self.parse_setting_value(mem_str, unit.as_deref()) {
                let mem_mb = mem_bytes / (1024 * 1024);

                if mem_mb < 4 {
                    validations.push(ValidationResult {
                        name: "work_mem".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "work_mem is {} MB. This is very low and may cause excessive disk sorts. Consider increasing to at least 4 MB.",
                            mem_mb
                        ),
                    });
                } else if mem_mb > 1024 {
                    validations.push(ValidationResult {
                        name: "work_mem".to_string(),
                        status: CheckStatus::Warn,
                        message: format!(
                            "work_mem is {} MB. Very high values can cause memory issues with many concurrent connections. Monitor memory usage carefully.",
                            mem_mb
                        ),
                    });
                } else {
                    validations.push(ValidationResult {
                        name: "work_mem".to_string(),
                        status: CheckStatus::Ok,
                        message: format!("work_mem is {} MB (acceptable).", mem_mb),
                    });
                }
            }
        }

        validations
    }

    pub(crate) fn validate_settings(&self, settings: HashMap<String, (String, Option<String>)>) -> Vec<ValidationResult> {
        let mut validations = vec![];

        validations.extend(self.check_autovacuum_scale_factors(&settings));
        validations.extend(self.check_autovacuum_workers(&settings));
        validations.extend(self.check_maintenance_work_mem(&settings));
        validations.extend(self.check_vacuum_cost_settings(&settings));
        validations.extend(self.check_work_mem(&settings));

        if validations.is_empty() {
            validations.push(ValidationResult {
                name: "vacuum_settings".to_string(),
                status: CheckStatus::Ok,
                message: "All vacuum-related settings are within acceptable ranges.".to_string(),
            });
        }

        validations
    }
}

#[async_trait]
impl Check for VacuumSettingsCheck {
    fn id(&self) -> &str {
        "vacuum_settings"
    }

    fn name(&self) -> &str {
        "Vacuum Settings Check"
    }

    fn category(&self) -> CheckCategory {
        CheckCategory::Performance
    }

    async fn run(&self, client: &Client) -> Result<CheckResult> {
        let query = include_str!("query.sql");

        let rows = client
            .query(query, &[])
            .await
            .context("Failed to query vacuum settings")?;

        let mut settings = HashMap::new();
        for row in rows {
            let name: String = row.get(0);
            let setting: String = row.get(1);
            let unit: Option<String> = row.get(2);
            settings.insert(name, (setting, unit));
        }

        let validations = self.validate_settings(settings);

        Ok(CheckResult {
            check_id: self.id().to_string(),
            check_name: self.name().to_string(),
            category: self.category(),
            validations,
        })
    }
}
