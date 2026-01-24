# Configuration

TQDBProxy uses an INI configuration file with optional environment variable overrides.

## Configuration File

Default path: `config.ini`

```ini
[mariadb]
listen = :3307
socket = /var/run/tqdbproxy/mysql.sock
primary = 127.0.0.1:3306
replica1 = 127.0.0.2:3306
replica2 = 127.0.0.3:3306

[postgres]
listen = :5433
socket = /var/run/tqdbproxy/.s.PGSQL.5433
primary = 127.0.0.1:5432
replica1 = 127.0.0.2:5432
```

### Options

| Section   | Key       | Default         | Description                    |
|-----------|-----------|-----------------|--------------------------------|
| mariadb   | listen    | :3307           | TCP listen address             |
| mariadb   | socket    |                 | Optional Unix socket path      |
| mariadb   | primary   | 127.0.0.1:3306  | Primary database address       |
| mariadb   | replica1-10 |               | Read replica addresses         |
| postgres  | listen    | :5433           | TCP listen address             |
| postgres  | socket    |                 | Optional Unix socket path      |
| postgres  | primary   | 127.0.0.1:5432  | Primary database address       |
| postgres  | replica1-10 |               | Read replica addresses         |

## Environment Variables

Environment variables override config file values:

- `TQDBPROXY_MARIADB_LISTEN` - MariaDB listen address
- `TQDBPROXY_MARIADB_PRIMARY` - MariaDB primary address
- `TQDBPROXY_POSTGRES_LISTEN` - PostgreSQL listen address
- `TQDBPROXY_POSTGRES_PRIMARY` - PostgreSQL primary address

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
