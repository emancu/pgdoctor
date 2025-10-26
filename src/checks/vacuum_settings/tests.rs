#[cfg(test)]
mod tests {
    use super::super::check::VacuumSettingsCheck;
    use crate::checks::CheckStatus;
    use std::collections::HashMap;

    #[test]
    fn test_parse_setting_value() {
        let check = VacuumSettingsCheck::new();

        assert_eq!(check.parse_setting_value("64", Some("kB")), Some(64 * 1024));
        assert_eq!(check.parse_setting_value("1", Some("MB")), Some(1024 * 1024));
        assert_eq!(check.parse_setting_value("1", Some("GB")), Some(1024 * 1024 * 1024));
        assert_eq!(check.parse_setting_value("100", Some("ms")), Some(100));
        assert_eq!(check.parse_setting_value("5", Some("s")), Some(5000));
        assert_eq!(check.parse_setting_value("42", None), Some(42));
    }

    #[test]
    fn test_parse_float_setting() {
        let check = VacuumSettingsCheck::new();

        assert_eq!(check.parse_float_setting("0.1"), Some(0.1));
        assert_eq!(check.parse_float_setting("0.05"), Some(0.05));
        assert_eq!(check.parse_float_setting("1.5"), Some(1.5));
        assert_eq!(check.parse_float_setting("invalid"), None);
    }

    #[test]
    fn test_check_autovacuum_scale_factors_optimal() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "autovacuum_analyze_scale_factor".to_string(),
            ("0.05".to_string(), None),
        );
        settings.insert(
            "autovacuum_vacuum_scale_factor".to_string(),
            ("0.1".to_string(), None),
        );

        let validations = check.check_autovacuum_scale_factors(&settings);
        assert_eq!(validations.len(), 2);
        assert!(validations.iter().all(|v| v.status == CheckStatus::Ok));
    }

    #[test]
    fn test_check_autovacuum_scale_factors_warn() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "autovacuum_analyze_scale_factor".to_string(),
            ("0.2".to_string(), None),
        );
        settings.insert(
            "autovacuum_vacuum_scale_factor".to_string(),
            ("0.3".to_string(), None),
        );

        let validations = check.check_autovacuum_scale_factors(&settings);
        assert_eq!(validations.len(), 2);
        assert!(validations.iter().all(|v| v.status == CheckStatus::Warn));
    }

    #[test]
    fn test_check_autovacuum_workers_low() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert("autovacuum_max_workers".to_string(), ("2".to_string(), None));

        let validations = check.check_autovacuum_workers(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Warn);
        assert!(validations[0].message.contains("increasing to at least 3"));
    }

    #[test]
    fn test_check_autovacuum_workers_optimal() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert("autovacuum_max_workers".to_string(), ("5".to_string(), None));

        let validations = check.check_autovacuum_workers(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Ok);
    }

    #[test]
    fn test_check_autovacuum_workers_high() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert("autovacuum_max_workers".to_string(), ("15".to_string(), None));

        let validations = check.check_autovacuum_workers(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Warn);
        assert!(validations[0].message.contains("resource contention"));
    }

    #[test]
    fn test_check_maintenance_work_mem_critical() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "maintenance_work_mem".to_string(),
            ("32768".to_string(), Some("kB".to_string())),
        ); // 32 MB

        let validations = check.check_maintenance_work_mem(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Critical);
        assert!(validations[0].message.contains("too low"));
    }

    #[test]
    fn test_check_maintenance_work_mem_warn() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "maintenance_work_mem".to_string(),
            ("131072".to_string(), Some("kB".to_string())),
        ); // 128 MB

        let validations = check.check_maintenance_work_mem(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Warn);
    }

    #[test]
    fn test_check_maintenance_work_mem_optimal() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "maintenance_work_mem".to_string(),
            ("524288".to_string(), Some("kB".to_string())),
        ); // 512 MB

        let validations = check.check_maintenance_work_mem(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Ok);
    }

    #[test]
    fn test_check_vacuum_cost_settings() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "vacuum_cost_delay".to_string(),
            ("2".to_string(), Some("ms".to_string())),
        );
        settings.insert("vacuum_cost_limit".to_string(), ("200".to_string(), None));

        let validations = check.check_vacuum_cost_settings(&settings);
        assert_eq!(validations.len(), 2);
        assert!(validations.iter().all(|v| v.status == CheckStatus::Ok));
    }

    #[test]
    fn test_check_work_mem_low() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "work_mem".to_string(),
            ("2048".to_string(), Some("kB".to_string())),
        ); // 2 MB

        let validations = check.check_work_mem(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Warn);
        assert!(validations[0].message.contains("very low"));
    }

    #[test]
    fn test_check_work_mem_optimal() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "work_mem".to_string(),
            ("16384".to_string(), Some("kB".to_string())),
        ); // 16 MB

        let validations = check.check_work_mem(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Ok);
    }

    #[test]
    fn test_check_work_mem_high() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();
        settings.insert(
            "work_mem".to_string(),
            ("2097152".to_string(), Some("kB".to_string())),
        ); // 2 GB

        let validations = check.check_work_mem(&settings);
        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Warn);
        assert!(validations[0].message.contains("memory issues"));
    }

    #[test]
    fn test_validate_settings_comprehensive() {
        let check = VacuumSettingsCheck::new();
        let mut settings = HashMap::new();

        // Add all settings
        settings.insert(
            "autovacuum_analyze_scale_factor".to_string(),
            ("0.1".to_string(), None),
        );
        settings.insert(
            "autovacuum_vacuum_scale_factor".to_string(),
            ("0.2".to_string(), None),
        );
        settings.insert("autovacuum_max_workers".to_string(), ("5".to_string(), None));
        settings.insert(
            "maintenance_work_mem".to_string(),
            ("524288".to_string(), Some("kB".to_string())),
        );
        settings.insert(
            "vacuum_cost_delay".to_string(),
            ("2".to_string(), Some("ms".to_string())),
        );
        settings.insert("vacuum_cost_limit".to_string(), ("200".to_string(), None));
        settings.insert(
            "work_mem".to_string(),
            ("16384".to_string(), Some("kB".to_string())),
        );

        let validations = check.validate_settings(settings);

        // Should have multiple validations, all OK
        assert!(validations.len() > 0);
        assert!(validations.iter().all(|v| v.status == CheckStatus::Ok));
    }
}
