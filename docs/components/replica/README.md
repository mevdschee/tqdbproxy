# Replica Component

The `replica` component manages multiple backend pools, each containing a primary database and optional read replicas. It provides the core sharding and routing logic for the proxy.

## Features

- **Multi-Pool Management**: Supports multiple named backend pools as defined in the hierarchical configuration.
- **Database Sharding**: Maps specific databases to different backend pools for horizontal scaling.
- **Load Balancing**: Implements a Round-Robin strategy within each pool to distribute read queries across healthy replicas.
- **Health Checks**: Periodically verifies the availability of all primary and replica backends using TCP or Unix connection probes.
- **Automatic Failover**: Transparently falls back to the primary database within a pool if no healthy replicas are available.

## Routing Logic

- **Shard Routing**: Determines the correct backend pool based on the database name (at connection time for PostgreSQL, or dynamically for MariaDB).
- **Primary**: All write operations (INSERT, UPDATE, DELETE) and non-cacheable SELECTs are routed to the pool's primary.
- **Replicas**: Cacheable SELECT queries (those with a `ttl > 0` hint) are distributed across healthy replicas in the pool.

[Back to Index](../../README.md)
