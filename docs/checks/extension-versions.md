# PostgreSQL Extension Versions

Inventories installed PostgreSQL extensions and reports on two distinct concerns under one check.
A single query joins `pg_extension` (installed version) with `pg_available_extensions` (the version
bundled on disk) and reads the server version, feeding both findings from one catalog read:

- **Supported Versions** — flags extensions whose installed version is no longer supported upstream,
  based on an embedded version-floor policy.
- **Pending Updates** — flags extensions whose installed version trails the version bundled on
  disk, i.e. an `ALTER EXTENSION ... UPDATE` was never run after the files were upgraded.

## Why This Matters

Running outdated extensions carries real operational risk:

- **Security and bug fixes missed**: Older extension releases stop receiving upstream patches, so
  known CVEs and data-corruption bugs go unfixed.
- **Blocks major-version upgrades**: A major PostgreSQL upgrade often requires a minimum extension
  version; stale extensions force last-minute scrambles during the upgrade window.
- **Partitioning extension is EOL upstream**: `pg_partman` 5.0 was a major rewrite onto native
  declarative partitioning, dropping the old trigger-based child-table machinery. The 4.x line no
  longer receives fixes, and partition maintenance behaviour differs enough that staying on 4.x is a
  liability.
- **Pre-5.1.0 breaks CDC on new partitions**: below 5.1.0, `run_maintenance()` doesn't inherit
  `REPLICA IDENTITY` onto new child partitions, so `FULL` (or unique-index) identity silently
  reverts to `DEFAULT` — dropping before-images from logical replication ([pg_partman
  #502](https://github.com/pgpartman/pg_partman/issues/502)). Upgrading isn't retroactive; existing
  partitions need a manual backfill.

## Validation

### Supported Versions

Each installed extension is classified against its policy entry:

- **WARN**: installed version is below the policy `WarnBelow` floor (deprecated), or the installed
  version string cannot be parsed and needs manual review.
- **WARN**: installed version is below the policy `FailBelow` floor (unsupported) — upgrade before it
  blocks an engine upgrade or misses security fixes.

Extensions with no policy entry, or already at a supported version, pass and are not listed.

The seeded policies are:

- `pg_partman` — warn below `5.1.0`, fail below `3.0`. The 4.x line is EOL upstream (deprecated),
  anything below 3.0 is unsupported, and below 5.1.0 the REPLICA IDENTITY inheritance fix is
  missing — see "Why This Matters" above.
- `postgis` — warn below `3.3`, fail below `3.0`. Releases below 3.3 are EOL upstream (deprecated);
  2.x (below 3.0) is unsupported.

Only extensions that need attention appear in the table; a clean run is just the PASS line. The
finding summary reports how many extensions are installed.

### Pending Updates

Compares each extension's installed version (`pg_extension.extversion`) against the version bundled
on disk (`pg_available_extensions.default_version`):

- **INFO**: the installed version is behind the bundled default (files upgraded, but
  `ALTER EXTENSION ... UPDATE` never run), or a version string cannot be parsed and needs manual
  review.
- Extensions where the installed version equals or exceeds the bundled default are not reported —
  installed-ahead happens with managed-provider bundle skew and is not actionable.
- Extensions with no bundled `default_version` (NULL) are skipped: managed providers may not expose
  control files for all extensions, so the ceiling is unknown.

The finding's summary reports the PostgreSQL major version the bundled defaults belong to, for
context (e.g. `behind the version bundled with PostgreSQL 17`).

**Managed-provider caveat**: managed extensions typically only advance their bundled
`default_version` alongside the engine version. A pending update flagged on such an extension is
informational — you cannot run the update until the provider publishes the newer files for your
engine release — so treat it as a signal rather than an always-immediately-actionable item.

## Configuration

The policy lives in `checks/extensionversions/policies.go`. To flag more extensions, add an
`ExtensionPolicy` entry with the exact `pg_extension.extname`, a `WarnBelow` and/or `FailBelow`
version floor, and a short `Reason`. Extensions with no policy entry are inventory-only and always
report `OK`.

## How to Fix

The findings are informational — they report state, not an instruction to run. **Do not run
`ALTER EXTENSION ... UPDATE` blindly.** Extension upgrades are frequently not mechanical: `pg_partman`
5.0 was a full rewrite onto native declarative partitioning, and PostGIS upgrades touch many
functions, types and operators. An upgrade can change behaviour or break dependent objects. Always
read the extension's official upgrade/migration notes and validate against a non-production copy
first.

- pg_partman — upgrade and migration docs: <https://github.com/pgpartman/pg_partman>
- PostGIS — see "Upgrading PostGIS": <https://postgis.net/docs/>

### Supported Versions

Once you have reviewed the migration steps, move the extension to a supported version:

```sql
ALTER EXTENSION pg_partman UPDATE TO '5.1.0';
```

On a managed provider the target version is bounded by what the provider publishes for your engine
release — you cannot jump past it without a PostgreSQL major-version upgrade.

**Critical caveat even on 5.1.0+**: the REPLICA IDENTITY fix only applies going forward. Partitions
created before you upgraded pg_partman, or before you set the parent to `FULL`, are not
retroactively fixed. You must audit and correct existing children manually.

### Pending Updates

The installed version trails the version bundled on disk. After confirming the bundled version's
migration notes, register it into the catalog:

```sql
ALTER EXTENSION pg_stat_statements UPDATE;
```

`pg_extension.extversion` reflects the version registered in the catalog, not the SQL files on disk.
Installing newer extension files (commonly during a PostgreSQL major upgrade) without running
`ALTER EXTENSION ... UPDATE` leaves `extversion` stale — the catalog still reports the old version
even though newer code is present. The **Pending Updates** finding surfaces exactly that drift by
comparing `extversion` against `pg_available_extensions.default_version`.

## Related Checks

- **`pg-version`** - Checks the PostgreSQL server version against support floors.
