# Replica Component

The `replica` component manages pools of database connections, specifically handling the distribution of queries between a primary database and multiple read replicas.

## Features

- **Pool Management**: Maintains a list of primary and replica addresses.
- **Load Balancing**: Implements a Round-Robin strategy to distribute read queries across available replicas.
- **Health Checks**: Periodically verifies the availability of replicas using TCP connection probes.
- **Automatic Failover**: Transparently falls back to the primary database if no healthy replicas are available.

## Routing Logic

- **Primary**: All write operations (INSERT, UPDATE, DELETE) and non-cacheable SELECTs.
- **Replicas**: Cacheable SELECT queries (those with a `ttl > 0` hint) can be routed to replicas to reduce primary load.

[Back to Components](../README.md)
