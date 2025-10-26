use crate::checks::{CheckResult, CheckStatus};

pub fn print_results(results: Vec<CheckResult>) {
    println!("\n╔══════════════════════════════════════════════════════════════════════════════╗");
    println!("║                           PostgreSQL Doctor Report                           ║");
    println!("╚══════════════════════════════════════════════════════════════════════════════╝\n");

    let mut overall_status = CheckStatus::Ok;

    for result in &results {
        let check_status = result.overall_status();

        // Update overall status
        if check_status == CheckStatus::Critical {
            overall_status = CheckStatus::Critical;
        } else if check_status == CheckStatus::Warn && overall_status != CheckStatus::Critical {
            overall_status = CheckStatus::Warn;
        }

        let status_icon = match check_status {
            CheckStatus::Ok => "✓",
            CheckStatus::Warn => "⚠",
            CheckStatus::Critical => "✗",
        };

        println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");
        println!(
            "{} [{}] {} (Category: {})",
            status_icon,
            check_status,
            result.check_name,
            result.category
        );
        println!("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━");

        for validation in &result.validations {
            let validation_icon = match validation.status {
                CheckStatus::Ok => "  ✓",
                CheckStatus::Warn => "  ⚠",
                CheckStatus::Critical => "  ✗",
            };
            println!("{} [{}] {}", validation_icon, validation.status, validation.message);
        }
        println!();
    }

    println!("════════════════════════════════════════════════════════════════════════════════");
    let summary_icon = match overall_status {
        CheckStatus::Ok => "✓",
        CheckStatus::Warn => "⚠",
        CheckStatus::Critical => "✗",
    };
    println!(
        "{} Overall Status: {}",
        summary_icon,
        overall_status
    );
    println!("════════════════════════════════════════════════════════════════════════════════\n");

    let exit_code = match overall_status {
        CheckStatus::Ok => 0,
        CheckStatus::Warn => 1,
        CheckStatus::Critical => 2,
    };

    std::process::exit(exit_code);
}
