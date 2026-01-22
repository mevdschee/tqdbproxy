# TQDBProxy Documentation

Welcome to the TQDBProxy documentation. TQDBProxy is a unified data layer that sits between applications and their databases, providing a single intelligent gateway for **MySQL** and **PostgreSQL**.

## Index

- [Project Brief](#project-brief)
- [Project Phases](#project-phases)
- [System Components](components/README.md)
  - [Cache](components/cache/README.md)
  - [Metrics](components/metrics/README.md)
  - [MySQL Protocol](components/mysql/README.md)
  - [SQL Parser](components/parser/README.md)
  - [Generic Proxy](components/proxy/README.md)
  - [Replica Management](components/replica/README.md)
- [Client Libraries](clients/README.md)

---

## Project Brief

### Overview
TQDBProxy acts as a dedicated proxy that handles caching, metrics, and query instrumentation centrally. It understands both MySQL and PostgreSQL wire protocols, intercepts queries, extracts cache-TTL hints, normalizes SQL, and serves results from a fast Otter cache or forwards to the underlying database.

### Key Features
- **Protocol Support**: Native understanding of MySQL and PostgreSQL (currently transparent) protocols.
- **Intelligent Caching**: Serves results from Otter cache based on SQL hints (e.g., `/* ttl:60 */`).
- **Replica Support**: Routes cacheable SELECT queries to read replicas for horizontal scaling.
- **Observability**: Rich Prometheus metrics collected per file and line number.

---

## Project Phases

TQDBProxy development is structured into five main phases:

1. **[Phase 1: Core Proxy Infrastructure](phases/PHASE1_CORE_PROXY.md)**
   - Foundational proxy server for MySQL and PostgreSQL.
   - Connection pooling and protocol handshakes.
2. **[Phase 2: Intelligent Caching Layer](phases/PHASE2_CACHING.md)**
   - SQL parsing and normalization.
   - Otter cache integration and TTL hint extraction.
3. **[Phase 3: Observability & Metrics](phases/PHASE3_OBSERVABILITY.md)**
   - Prometheus integration.
   - Caller metadata extraction (file/line hints).
4. **[Phase 4: Client Libraries](phases/PHASE4_CLIENT_LIBRARIES.md)**
   - Native driver wrappers for Go and PHP.
   - Automatic hint injection.
5. **[Phase 5: Replica Support](phases/PHASE5_REPLICA_SUPPORT.md)**
   - Read replica routing and load balancing.
   - Health checks and automatic failover.

For more details, see the [Phases Index](phases/README.md).

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

See the [Clients Documentation](clients/README.md) for usage instructions.
