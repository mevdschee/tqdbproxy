# Phase 5: Replica Support

## Goal

Enable routing of cacheable SELECT queries to read replicas, allowing horizontal scaling and reducing primary database load.

## Deliverables

### 5.1 Replica Configuration
- Configure multiple replicas per database in config file:
  ```ini
  [mysql]
  primary = mysql://primary:3306/db
  replica1 = mysql://replica1:3306/db
  replica2 = mysql://replica2:3306/db
  ```
- Health checks for replica availability
- Automatic failover when replica unavailable

### 5.2 Query Routing Logic
- SELECT queries with TTL > 0 → eligible for replica
- INSERT/UPDATE/DELETE → always to primary
- SELECT with TTL = 0 → primary (fresh data required)
- Configurable: force specific queries to primary

### 5.3 Load Balancing
- Round-robin across healthy replicas
- Optional: weighted distribution
- Optional: least-connections algorithm
- Sticky sessions (optional, per connection)

### 5.4 Replica Lag Awareness (Optional)
- Monitor replication lag per replica
- Skip replicas with lag > configurable threshold
- Metric: `tqdbproxy_replica_lag_seconds`

## Success Criteria

- [ ] SELECT queries with TTL > 0 route to replicas
- [ ] Write queries always go to primary
- [ ] Failed replica automatically removed from rotation
- [ ] Metrics show query distribution across replicas

## Estimated Effort

1-2 weeks
