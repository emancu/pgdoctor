#[cfg(test)]
mod tests {
    use super::super::check::TableSizesCheck;
    use crate::checks::CheckStatus;

    #[test]
    fn test_format_bytes() {
        let check = TableSizesCheck::new();

        assert_eq!(check.format_bytes(500), "500 bytes");
        assert_eq!(check.format_bytes(1024), "1.00 KB");
        assert_eq!(check.format_bytes(1024 * 1024), "1.00 MB");
        assert_eq!(check.format_bytes(1024 * 1024 * 1024), "1.00 GB");
        assert_eq!(check.format_bytes(1024i64 * 1024 * 1024 * 1024), "1.00 TB");
        assert_eq!(check.format_bytes(5 * 1024 * 1024 * 1024), "5.00 GB");
    }

    #[test]
    fn test_validate_tables_empty() {
        let check = TableSizesCheck::new();
        let tables = vec![];
        let validations = check.validate_tables(tables);

        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Ok);
        assert!(validations[0].message.contains("No tables found"));
    }

    #[test]
    fn test_validate_tables_small() {
        let check = TableSizesCheck::new();
        let tables = vec![
            ("public".to_string(), "users".to_string(), 1024 * 1024), // 1 MB
            ("public".to_string(), "posts".to_string(), 5 * 1024 * 1024), // 5 MB
        ];
        let validations = check.validate_tables(tables);

        // Should have total_size + large_tables validations
        assert_eq!(validations.len(), 2);
        assert_eq!(validations[0].name, "total_size");
        assert_eq!(validations[0].status, CheckStatus::Ok);
        assert_eq!(validations[1].name, "large_tables");
        assert_eq!(validations[1].status, CheckStatus::Ok);
    }

    #[test]
    fn test_validate_tables_warn_threshold() {
        let check = TableSizesCheck::new();
        let tables = vec![
            ("public".to_string(), "large_table".to_string(), 15 * 1024 * 1024 * 1024), // 15 GB
        ];
        let validations = check.validate_tables(tables);

        // Should have total_size + table_size validation
        assert_eq!(validations.len(), 2);
        assert_eq!(validations[0].name, "total_size");
        assert!(validations[1].name.starts_with("table_size_"));
        assert_eq!(validations[1].status, CheckStatus::Warn);
        assert!(validations[1].message.contains("large"));
    }

    #[test]
    fn test_validate_tables_critical_threshold() {
        let check = TableSizesCheck::new();
        let tables = vec![
            ("public".to_string(), "huge_table".to_string(), 60 * 1024 * 1024 * 1024), // 60 GB
        ];
        let validations = check.validate_tables(tables);

        // Should have total_size + table_size validation
        assert_eq!(validations.len(), 2);
        assert_eq!(validations[0].name, "total_size");
        assert!(validations[1].name.starts_with("table_size_"));
        assert_eq!(validations[1].status, CheckStatus::Critical);
        assert!(validations[1].message.contains("very large"));
    }

    #[test]
    fn test_validate_tables_mixed() {
        let check = TableSizesCheck::new();
        let tables = vec![
            ("public".to_string(), "small".to_string(), 1024 * 1024), // 1 MB
            ("public".to_string(), "medium".to_string(), 15 * 1024 * 1024 * 1024), // 15 GB (warn)
            ("public".to_string(), "large".to_string(), 60 * 1024 * 1024 * 1024), // 60 GB (critical)
        ];
        let validations = check.validate_tables(tables);

        // Should have total_size + 2 table_size validations
        assert_eq!(validations.len(), 3);
        assert_eq!(validations[0].name, "total_size");

        // Check that we have both warn and critical
        let statuses: Vec<_> = validations.iter().map(|v| &v.status).collect();
        assert!(statuses.contains(&&CheckStatus::Warn));
        assert!(statuses.contains(&&CheckStatus::Critical));
    }
}
