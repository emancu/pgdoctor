# Security Policy

## Report a vulnerability

Do not open a public issue for a vulnerability. Use GitHub private vulnerability reporting:

1. Go to the [Security tab](https://github.com/emancu/pgdoctor/security) of github.com/emancu/pgdoctor.
2. Click **Report a vulnerability**.
3. Write the affected version, the steps to reproduce, and the effect.

## Supported versions

Only the latest minor release gets security fixes.

## Credentials

pgdoctor reads a DSN from its argument or from `PGDOCTOR_DSN`. It does not store the DSN, and its output shows only the host and the database name.
