# TQDBProxy - Project Brief

## Overview

TQDBProxy is a unified data layer that sits between applications and their databases, providing a single intelligent gateway for **MySQL** and **PostgreSQL**.

## Core Value Proposition

Instead of every service managing its own caching, metrics, and query instrumentation, a dedicated Go written proxy handles these concerns centrally.

## Key Features

### Protocol Support
- Native understanding of both MySQL and PostgreSQL wire protocols
- Supports simple and prepared statements

### Intelligent Caching
- Intercepts queries and extracts optional cache-TTL and file and line number hints
- Normalizes SQL for cache key generation
- Serves results from fast Otter cache when available (in-memory, with ttl, cache key is: query + params)
- Falls back to underlying database when needed

### Replica Support
- Select queries with TTL > 0 (stale responses accepted) can be served from replicas
- Enables horizontal scaling of the database layer
- Reduces load on the primary database

### Observability
- Metrics are collected per:
  - file and line number
  - replica (primary, replica1, replica2, ...)
  - query type (insert, update, delete, select)
- Rich execution metrics per file and line number:
  - Query (without params)
  - Total query latency
  - Cache hit/miss ratio
  - Primary (vs. replica) ratio
  - Total queries
  - Cache queries
- Prometheus-compatible metrics endpoint
- Deep visibility into database behavior without modifying application logic

## Client Libraries

Six client libraries for seamless adoption:

| Language   | MySQL | PostgreSQL |
|------------|:-----:|:----------:|
| Go         | ✓     | ✓          |
| PHP        | ✓     | ✓          |
| TypeScript | ✓     | ✓          |

Each library:
- Wraps the existing native driver
- Preserves the familiar interface
- Adds optional cache-TTL parameter
- Automatically attaches caller metadata

## Benefits

- **Zero-friction adoption**: Drop-in replacement for existing drivers
- **Centralized caching**: Reduces database load across all services
- **Unified observability**: Consistent instrumentation across the entire stack
- **Language-agnostic**: Same behavior and metrics regardless of client language
- **Measurable & optimizable**: Makes database access transparent and tunable
