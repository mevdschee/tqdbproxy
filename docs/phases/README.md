# Project Phases

This directory contains detailed documentation for each development phase of TQDBProxy.

## Phases Index

1. **[Phase 1: Core Proxy Infrastructure](PHASE1_CORE_PROXY.md)**
   - Goal: Build the foundational proxy server that can accept database connections and forward them to MySQL and PostgreSQL backends.
2. **[Phase 2: Intelligent Caching Layer](PHASE2_CACHING.md)**
   - Goal: Add query caching with TTL support using Otter cache, including SQL normalization and cache-hint extraction.
3. **[Phase 3: Observability & Metrics](PHASE3_OBSERVABILITY.md)**
   - Goal: Implement comprehensive metrics collection with Prometheus-compatible endpoint.
4. **[Phase 4: Client Libraries](PHASE4_CLIENT_LIBRARIES.md)**
   - Goal: Create client libraries that wrap native drivers and inject cache-TTL and caller metadata hints.
5. **[Phase 5: Replica Support](PHASE5_REPLICA_SUPPORT.md)**
   - Goal: Enable routing of cacheable SELECT queries to read replicas.

[Back to Documentation Home](../README.md)
