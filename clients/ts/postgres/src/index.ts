import { Client, ClientConfig, QueryResult, QueryResultRow } from 'pg';

/**
 * Database wrapper for TQDBProxy PostgreSQL with TTL-aware caching
 * 
 * This class wraps pg (node-postgres) and provides methods to execute queries with cache TTL hints
 * that are automatically injected as SQL comments along with caller metadata.
 * 
 * Example usage:
 * 
 *     const db = new Database({
 *       host: 'localhost',
 *       port: 5433,
 *       user: 'postgres',
 *       password: 'password',
 *       database: 'test'
 *     });
 *     
 *     await db.connect();
 *     
 *     // Standard query (no caching)
 *     const result = await db.query("SELECT * FROM users");
 *     
 *     // Query with 60-second cache TTL
 *     const result = await db.queryWithTTL(60, "SELECT * FROM users WHERE id = $1", [1]);
 */
export class Database {
    private client: Client;

    constructor(config: ClientConfig) {
        this.client = new Client(config);
    }

    /**
     * Wrap an existing pg client
     * 
     * @param client Existing pg client
     * @returns Database instance
     */
    static wrap(client: Client): Database {
        const instance = new Database({});
        instance.client = client;
        return instance;
    }

    /**
     * Connect to the database
     */
    async connect(): Promise<void> {
        await this.client.connect();
    }

    /**
     * Execute a query with cache TTL hint
     * 
     * Automatically captures the caller's file and line number and injects
     * a SQL comment hint: ttl:X file:Y line:Z
     * 
     * @param ttl Cache TTL in seconds
     * @param text SQL query
     * @param values Query parameters
     * @returns Promise with query result
     */
    async queryWithTTL<R extends QueryResultRow = any>(
        ttl: number,
        text: string,
        values?: any[]
    ): Promise<QueryResult<R>> {
        const caller = this.getCaller();
        const hint = `/* ttl:${ttl} file:${caller.file} line:${caller.line} */`;
        const hintedQuery = `${hint} ${text}`;

        return this.client.query<R>(hintedQuery, values);
    }

    /**
     * Execute a standard query with caller metadata
     * 
     * @param text SQL query
     * @param values Query parameters
     * @returns Promise with query result
     */
    async query<R extends QueryResultRow = any>(
        text: string,
        values?: any[]
    ): Promise<QueryResult<R>> {
        const caller = this.getCaller();
        const hint = `/* file:${caller.file} line:${caller.line} */`;
        const hintedQuery = `${hint} ${text}`;

        return this.client.query<R>(hintedQuery, values);
    }

    /**
     * Get the underlying pg client
     * 
     * @returns pg Client
     */
    getClient(): Client {
        return this.client;
    }

    /**
     * Close the database connection
     */
    async end(): Promise<void> {
        await this.client.end();
    }

    /**
     * Get caller information from stack trace
     * 
     * @returns Caller file and line number
     */
    private getCaller(): { file: string; line: number } {
        const err = new Error();
        const stack = err.stack || '';
        const lines = stack.split('\n');

        // Skip: Error, getCaller, queryWithTTL, actual caller
        const callerLine = lines[3] || '';

        // Parse stack trace line (format: "    at Object.<anonymous> (/path/to/file.ts:42:13)")
        const match = callerLine.match(/\((.+):(\d+):\d+\)/) || callerLine.match(/at (.+):(\d+):\d+/);

        if (match) {
            const fullPath = match[1];
            const file = fullPath.split('/').pop() || 'unknown';
            const line = parseInt(match[2], 10);
            return { file, line };
        }

        return { file: 'unknown', line: 0 };
    }
}
