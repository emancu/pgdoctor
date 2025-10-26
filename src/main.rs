mod checks;
mod cli;
mod db;
mod output;

use anyhow::Result;
use checks::{
    table_sizes::TableSizesCheck, vacuum_settings::VacuumSettingsCheck, version::VersionCheck,
    Check,
};
use clap::Parser;
use cli::Cli;

#[tokio::main]
async fn main() -> Result<()> {
    let args = Cli::parse();

    println!("Connecting to PostgreSQL...");
    let client = db::connect(&args.connection).await?;
    println!("Connected successfully!\n");

    let all_checks: Vec<Box<dyn Check>> = vec![
        Box::new(VersionCheck::new()),
        Box::new(TableSizesCheck::new()),
        Box::new(VacuumSettingsCheck::new()),
    ];

    let mut results = vec![];

    for check in all_checks {
        let category = check.category().to_string();
        if args.should_run_check(check.id(), &category) {
            println!("Running check: {}", check.name());
            match check.run(&client).await {
                Ok(result) => results.push(result),
                Err(e) => {
                    eprintln!("Error running check {}: {}", check.name(), e);
                }
            }
        }
    }

    output::print_results(results);

    Ok(())
}
