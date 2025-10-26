use anyhow::{Context, Result};
use native_tls::TlsConnector;
use postgres_native_tls::MakeTlsConnector;
use tokio_postgres::Client;

pub async fn connect(connection_string: &str) -> Result<Client> {
    let connector = TlsConnector::builder()
        .danger_accept_invalid_certs(true)
        .build()
        .context("Failed to build TLS connector")?;

    let connector = MakeTlsConnector::new(connector);

    let (client, connection) = tokio_postgres::connect(connection_string, connector)
        .await
        .context("Failed to connect to PostgreSQL")?;

    tokio::spawn(async move {
        if let Err(e) = connection.await {
            eprintln!("Connection error: {}", e);
        }
    });

    Ok(client)
}
