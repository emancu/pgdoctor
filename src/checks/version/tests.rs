#[cfg(test)]
mod tests {
    use super::super::check::VersionCheck;
    use crate::checks::CheckStatus;

    #[test]
    fn test_parse_version() {
        let check = VersionCheck::new();

        assert_eq!(
            check.parse_version("PostgreSQL 15.3 on x86_64-pc-linux-gnu"),
            Some(15)
        );
        assert_eq!(
            check.parse_version("PostgreSQL 14.0 (Ubuntu 14.0-1.pgdg20.04+1)"),
            Some(14)
        );
        assert_eq!(check.parse_version("PostgreSQL 9.6.24"), Some(9));
        assert_eq!(check.parse_version("Invalid version string"), None);
    }

    #[test]
    fn test_validate_version_critical() {
        let check = VersionCheck::new();
        let validations = check.validate_version("PostgreSQL 9.6.24".to_string());

        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Critical);
        assert!(validations[0].message.contains("end-of-life"));
    }

    #[test]
    fn test_validate_version_warn() {
        let check = VersionCheck::new();
        let validations = check.validate_version("PostgreSQL 11.5".to_string());

        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Warn);
        assert!(validations[0].message.contains("approaching end-of-life"));
    }

    #[test]
    fn test_validate_version_ok() {
        let check = VersionCheck::new();
        let validations = check.validate_version("PostgreSQL 15.3".to_string());

        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Ok);
        assert!(validations[0].message.contains("supported"));
    }

    #[test]
    fn test_validate_version_invalid() {
        let check = VersionCheck::new();
        let validations = check.validate_version("Invalid version".to_string());

        assert_eq!(validations.len(), 1);
        assert_eq!(validations[0].status, CheckStatus::Warn);
        assert!(validations[0].message.contains("Could not parse"));
    }
}
