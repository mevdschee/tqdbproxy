# SQL Parser Component

The `parser` component is a lightweight SQL analyzer used to extract metadata and hints from SQL queries.

## Functionality

- **Hint Extraction**: Uses regular expressions to find and parse comments in the format `/* ttl:60 file:user.go line:42 */`.
  - `ttl`: Cache duration in seconds.
  - `file`: Source file that issued the query.
  - `line`: Line number in the source file.
- **Query Type Detection**: Identifies whether a query is a `SELECT`, `INSERT`, `UPDATE`, or `DELETE` statement.
- **Cacheability Check**: Determines if a query is eligible for caching (must be a `SELECT` query with a `ttl` > 0).

## Implementation

The parser uses optimized regular expressions to extract information without the overhead of a full SQL grammar parser, ensuring minimal latency in the proxy hot path.

[Back to Index](../../README.md)
