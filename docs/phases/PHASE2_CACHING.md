# Phase 2: Intelligent Caching Layer

## Goal

Add query caching with TTL support using Otter cache, including SQL normalization and cache-hint extraction.

## Deliverables

### 2.1 SQL Parser & Normalizer
- Parse SQL to identify query type (SELECT, INSERT, UPDATE, DELETE)
- Normalize queries by removing parameter values
- Generate stable cache keys from normalized query + parameters

### 2.2 Cache-TTL Hint Extraction
- Define hint format in SQL comments: `/* ttl:60 */` or `-- ttl:60`
- Extract TTL value during query parsing
- Default TTL of 0 (no caching) when hint absent

### 2.3 Otter Cache Integration
- Integrate [Otter](https://github.com/maypok86/otter) as in-memory cache
- Configure max memory / max entries
- TTL-based expiration per entry
- Cache key: normalized query + serialized parameters

### 2.4 Cache Logic
- On SELECT with TTL > 0:
  - Check cache for existing result
  - On hit: return cached result, skip database
  - On miss: query database, store result, return
- Non-SELECT queries bypass cache
- Optional: cache invalidation on write queries

## Success Criteria

- [ ] SELECT queries with TTL hint are cached
- [ ] Subsequent identical queries return cached results
- [ ] Cache respects TTL expiration
- [ ] Non-cacheable queries pass through unchanged

## Estimated Effort

1-2 weeks
