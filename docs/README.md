# TQDBProxy Documentation

Welcome to the TQDBProxy documentation. TQDBProxy is a unified data layer that
sits between applications and their databases, providing a single intelligent
gateway for **MariaDB** and **PostgreSQL**.

## Index

- [Configuration](configuration/README.md)
- System Components
  - [Cache](components/cache/README.md)
  - [Metrics](components/metrics/README.md)
  - [MariaDB Protocol](components/mariadb/README.md)
  - [PostgreSQL Protocol](components/postgres/README.md)
  - [SQL Parser](components/parser/README.md)
  - [Replica Management](components/replica/README.md)
  - [Write Batching](components/writebatch/README.md)
- [Client Libraries](clients/README.md)
- Special Topics
  - [Batch Hint Quick Start](BATCH_HINT_QUICKSTART.md)
  - [Batch Hint Implementation Analysis](BATCH_HINT_ANALYSIS.md)
  - [Production Readiness](PRODUCTION_READINESS.md)

---

## System Components

TQDBProxy is composed of several modular components:

- **[Cache](components/cache/README.md)**: In-memory caching with thundering
  herd protection using TQMemory.
- **[Metrics](components/metrics/README.md)**: Collects and exposes
  Prometheus-compatible metrics.
- **[MariaDB](components/mariadb/README.md)**: Handles the MariaDB-specific wire
  protocol and query interception.
- **[PostgreSQL](components/postgres/README.md)**: Handles the
  PostgreSQL-specific wire protocol and query interception.
- **[Parser](components/parser/README.md)**: Extracts metadata and hints from
  SQL queries.
- **[Replica](components/replica/README.md)**: Manages database connection pools
  and health checks.
- **[Write Batching](components/writebatch/README.md)**: Batches write
  operations for improved throughput using hint-based grouping.

---

## Client Libraries

Currently, TQDBProxy provides client libraries for:

- **[Go](clients/README.md)**
- **[PHP](clients/README.md)**
- **[TypeScript](clients/README.md)**

See the [Clients Documentation](clients/README.md) for usage instructions.
