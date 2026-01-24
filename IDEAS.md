# Ideas

## ACID-Compliant Dual Writes

The proxy could support writing to two databases simultaneously with full ACID guarantees using XA (distributed) transactions. When dual-write mode is enabled, every write operation would be executed against both the primary and secondary database in parallel, using two-phase commit to ensure atomicity. In the prepare phase, both databases confirm they can commit. Only if both succeed does the proxy issue the final commit to both; if either fails, both roll back. This guarantees that confirmed writes exist in both databases and failed writes exist in neither.

Since writes execute in parallel, latency equals the slower of the two databases rather than the sum. The main trade-off is availability: if either database becomes unreachable, all writes block. This feature enables zero-downtime database migrations, geographic redundancy, and live replica promotion. Configuration would simply add a `secondary` address and `dual_write = true` flag.

## Sharding over multiple primaries

The proxy could have a configurable list of databases per primary (and support multiple primaries), a pattern often seen at more expensive SaaS providers, where each customer gets their own database and there is also one shared database for all customers. It should be transparent to the application, and the proxy should be able to route queries to the appropriate primary based on the selected database.
