use clap::{Args, Parser, Subcommand};

#[derive(Parser, Debug)]
#[command(name = "pgdoctor")]
#[command(author = "Your Team")]
#[command(version = "0.1.0")]
#[command(about = "PostgreSQL database health checker", long_about = None)]
pub struct Cli {
    /// PostgreSQL connection string (e.g., "postgresql://user:password@localhost/dbname")
    #[arg(short, long, global = true)]
    pub connection: String,

    #[command(subcommand)]
    pub command: Commands,
}

#[derive(Subcommand, Debug)]
pub enum Commands {
    /// Run configured database checks
    Run(RunArgs),
    /// Perform a detailed table bloat analysis
    #[command(name = "check-bloat")]
    CheckBloat,
}

#[derive(Args, Debug)]
pub struct RunArgs {
    /// Include only these check IDs (comma-separated, e.g., "pg_version,table_sizes")
    #[arg(long, value_delimiter = ',')]
    pub include: Option<Vec<String>>,

    /// Exclude these check IDs (comma-separated, e.g., "pg_version,table_sizes")
    #[arg(long, value_delimiter = ',')]
    pub exclude: Option<Vec<String>>,

    /// Include only checks from these categories (comma-separated: performance,storage,indexes,settings,architecture)
    #[arg(long, value_delimiter = ',')]
    pub categories: Option<Vec<String>>,
}

impl RunArgs {
    pub fn should_run_check(&self, check_id: &str, category: &str) -> bool {
        // If include is specified, only run checks that are included
        if let Some(include) = &self.include {
            if !include.contains(&check_id.to_string()) {
                return false;
            }
        }

        // If exclude is specified, don't run excluded checks
        if let Some(exclude) = &self.exclude {
            if exclude.contains(&check_id.to_string()) {
                return false;
            }
        }

        // If categories are specified, only run checks from those categories
        if let Some(categories) = &self.categories {
            if !categories.contains(&category.to_string()) {
                return false;
            }
        }

        true
    }
}