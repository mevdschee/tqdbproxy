# TQDBProxy Documentation

Welcome to the TQDBProxy documentation. TQDBProxy is a unified data layer that sits between applications and their databases, providing a single intelligent gateway for **MySQL** and **PostgreSQL**.

## Index

- [System Components](components/README.md)
  - [Cache](components/cache/README.md)
  - [Metrics](components/metrics/README.md)
  - [MySQL Protocol](components/mysql/README.md)
  - [SQL Parser](components/parser/README.md)
  - [Generic Proxy](components/proxy/README.md)
  - [Replica Management](components/replica/README.md)
- [Client Libraries](clients/README.md)

---

## System Components

TQDBProxy is composed of several modular components:

- **[Cache](components/cache/README.md)**: Manages in-memory storage using the Otter library.
- **[Metrics](components/metrics/README.md)**: Collects and exposes Prometheus-compatible metrics.
- **[MySQL](components/mysql/README.md)**: Handles the MySQL-specific wire protocol and query interception.
- **[Parser](components/parser/README.md)**: Extracts metadata and hints from SQL queries.
- **[Proxy](components/proxy/README.md)**: Provides a generic TCP proxy implementation.
- **[Replica](components/replica/README.md)**: Manages database connection pools and health checks.

---

## Client Libraries

Currently, TQDBProxy provides client libraries for:
- **[Go](clients/README.md)**
- **[PHP](clients/README.md)**
- **[TypeScript](clients/README.md)**

See the [Clients Documentation](clients/README.md) for usage instructions.
