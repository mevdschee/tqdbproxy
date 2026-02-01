# Configuration

TQDBProxy uses an INI configuration file with optional environment variable overrides.

## Configuration File

Default path: `config.ini`

```ini
[mariadb]
listen = :3307
socket = /var/run/tqdbproxy/mysql.sock
default = main

[mariadb.main]
primary = 127.0.0.1:3306
replicas = 127.0.0.2:3306, 127.0.0.3:3306

[mariadb.shard1]
primary = 10.0.0.1:3306
databases = billing, reporting
```

### Options

| Section       | Key       | Default         | Description                                |
|---------------|-----------|-----------------|--------------------------------------------|
| [protocol]    | listen    | :3307 / :5433   | TCP listen address                         |
| [protocol]    | socket    |                 | Optional Unix socket path                  |
| [protocol]    | default   |                 | Name of the default (catch-all) backend   |
| [protocol].id | primary   |                 | Primary database address for this shard    |
| [protocol].id | replicas  |                 | Comma-separated list of read replicas     |
| [protocol].id | databases |                 | Comma-separated list of databases for this shard |

## Database Sharding

TQDBProxy supports horizontal sharding. Queries are routed based on the database name:

1. **PostgreSQL**: Routing is determined at connection time based on the database parameter in the startup message. Note that **schema sharding is not supported** (only database-level sharding).
2. **MariaDB**: Supports transparent shifting mid-connection via `USE database` or `COM_INIT_DB` packets.

This provides a unified sharding model across both protocols where a "Database" acts as the unit of distribution.

## Environment Variables

The following environment variables are supported for overriding listen addresses:

- `TQDBPROXY_MARIADB_LISTEN` - MariaDB TCP listen address
- `TQDBPROXY_POSTGRES_LISTEN` - PostgreSQL TCP listen address

## Hot Config Reload

TQDBProxy supports hot reloading of replica configuration via SIGHUP:

```bash
# Reload configuration
kill -SIGHUP $(pidof tqdbproxy)
```

On SIGHUP, the proxy will:
1. Re-read the configuration file
2. Update the primary and replica addresses for both MariaDB and PostgreSQL
3. Preserve health status of existing replicas
4. Log the changes

**Note**: Listen addresses and socket paths cannot be changed without restart.

[Back to Index](../README.md)
