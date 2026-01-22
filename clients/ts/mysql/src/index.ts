import mysql, { Connection, RowDataPacket, OkPacket, ResultSetHeader, FieldPacket } from 'mysql2/promise';

/**
 * Database wrapper for TQDBProxy MySQL with TTL-aware caching
 * 
 * This class wraps mysql2/promise and provides methods to execute queries with cache TTL hints
 * that are automatically injected as SQL comments along with caller metadata.
 * 
 * Example usage:
 * 
 *     const db = new Database({
 *       host: 'localhost',
 *       port: 3307,
 *       user: 'root',
 *       password: 'password',
 *       database: 'test'
 *     });
 *     
 *     // Standard query (no caching)
 *     const [rows] = await db.query("SELECT * FROM users");
 *     
 *     // Query with 60-second cache TTL
 *     const [rows] = await db.queryWithTTL(60, "SELECT * FROM users WHERE id = ?", [1]);
 */
export class Database {
    private connection: Connection;

    private constructor(connection: Connection) {
        this.connection = connection;
    }

    /**
     * Create a new Database instance
     * 
     * @param config MySQL connection configuration
     * @returns Promise<Database>
     */
    static async create(config: mysql.ConnectionOptions): Promise<Database> {
        const connection = await mysql.createConnection(config);
        return new Database(connection);
    }

    /**
     * Wrap an existing mysql2 connection
     * 
     * @param connection Existing mysql2 connection
     * @returns Database instance
     */
    static wrap(connection: Connection): Database {
        return new Database(connection);
    }

    /**
     * Execute a query with cache TTL hint
     * 
     * Automatically captures the caller's file and line number and injects
     * a SQL comment hint: ttl:X file:Y line:Z
     * 
     * @param ttl Cache TTL in seconds
     * @param sql SQL query
     * @param values Query parameters
     * @returns Promise with query results
     */
    async queryWithTTL<T extends RowDataPacket[][] | RowDataPacket[] | OkPacket | OkPacket[] | ResultSetHeader>(
        ttl: number,
        sql: string,
        values?: any
    ): Promise<[T, FieldPacket[]]> {
        const caller = this.getCaller();
        const hint = `/* ttl:${ttl} file:${caller.file} line:${caller.line} */`;
        const hintedQuery = `${hint} ${sql}`;

        return this.connection.query<T>(hintedQuery, values);
    }

    /**
     * Execute a standard query with caller metadata
     * 
     * @param sql SQL query
     * @param values Query parameters
     * @returns Promise with query results
     */
    async query<T extends RowDataPacket[][] | RowDataPacket[] | OkPacket | OkPacket[] | ResultSetHeader>(
        sql: string,
        values?: any
    ): Promise<[T, FieldPacket[]]> {
        const caller = this.getCaller();
        const hint = `/* file:${caller.file} line:${caller.line} */`;
        const hintedQuery = `${hint} ${sql}`;

        return this.connection.query<T>(hintedQuery, values);
    }

    /**
     * Execute a prepared statement with caller metadata
     * 
     * @param sql SQL query
     * @param values Query parameters
     * @returns Promise with query results
     */
    async execute<T extends RowDataPacket[][] | RowDataPacket[] | OkPacket | OkPacket[] | ResultSetHeader>(
        sql: string,
        values?: any
    ): Promise<[T, FieldPacket[]]> {
        const caller = this.getCaller();
        const hint = `/* file:${caller.file} line:${caller.line} */`;
        const hintedQuery = `${hint} ${sql}`;

        return this.connection.execute<T>(hintedQuery, values);
    }

    /**
     * Get the underlying mysql2 connection
     * 
     * @returns mysql2 Connection
     */
    getConnection(): Connection {
        return this.connection;
    }

    /**
     * Close the database connection
     */
    async close(): Promise<void> {
        await this.connection.end();
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
