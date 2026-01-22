<?php

namespace TQDBProxy\MySQL;

use PDO;
use PDOStatement;

/**
 * Database wrapper for TQDBProxy MySQL with TTL-aware caching
 * 
 * This class wraps PDO and provides methods to execute queries with cache TTL hints
 * that are automatically injected as SQL comments along with caller metadata.
 * 
 * Example usage:
 * 
 *     $db = new Database('mysql:host=localhost;port=3307;dbname=test', 'user', 'pass');
 *     
 *     // Standard query (no caching)
 *     $stmt = $db->query("SELECT * FROM users");
 *     
 *     // Query with 60-second cache TTL
 *     $stmt = $db->queryWithTTL(60, "SELECT * FROM users WHERE id = ?", [1]);
 */
class Database
{
    private PDO $pdo;

    /**
     * Create a new Database instance
     * 
     * @param string $dsn Data Source Name
     * @param string|null $username Database username
     * @param string|null $password Database password
     * @param array|null $options PDO options
     */
    public function __construct(
        string $dsn,
        ?string $username = null,
        ?string $password = null,
        ?array $options = null
    ) {
        $this->pdo = new PDO($dsn, $username, $password, $options);
    }

    /**
     * Wrap an existing PDO instance
     * 
     * @param PDO $pdo Existing PDO instance
     * @return self
     */
    public static function wrap(PDO $pdo): self
    {
        $instance = new self('mysql:host=localhost', null, null);
        $instance->pdo = $pdo;
        return $instance;
    }

    /**
     * Execute a query with cache TTL hint
     * 
     * Automatically captures the caller's file and line number and injects
     * a SQL comment hint: ttl:X file:Y line:Z
     * 
     * @param int $ttl Cache TTL in seconds
     * @param string $query SQL query
     * @param array $params Query parameters
     * @return PDOStatement
     */
    public function queryWithTTL(int $ttl, string $query, array $params = []): PDOStatement
    {
        $caller = $this->getCaller();
        $hint = sprintf('/* ttl:%d file:%s line:%d */', $ttl, $caller['file'], $caller['line']);
        $hintedQuery = $hint . ' ' . $query;

        $stmt = $this->pdo->prepare($hintedQuery);
        $stmt->execute($params);
        return $stmt;
    }

    /**
     * Execute a standard query (pass-through to PDO)
     * 
     * @param string $query SQL query
     * @return PDOStatement
     */
    public function query(string $query): PDOStatement
    {
        return $this->pdo->query($query);
    }

    /**
     * Get the underlying PDO instance
     * 
     * @return PDO
     */
    public function getPDO(): PDO
    {
        return $this->pdo;
    }

    /**
     * Prepare a statement (pass-through to PDO)
     * 
     * @param string $query SQL query
     * @return PDOStatement|false
     */
    public function prepare(string $query): PDOStatement|false
    {
        return $this->pdo->prepare($query);
    }

    /**
     * Get caller information from backtrace
     * 
     * @return array{file: string, line: int}
     */
    private function getCaller(): array
    {
        $trace = debug_backtrace(DEBUG_BACKTRACE_IGNORE_ARGS, 3);
        // Skip: [0] = getCaller, [1] = queryWithTTL, [2] = actual caller
        $caller = $trace[2] ?? ['file' => 'unknown', 'line' => 0];

        return [
            'file' => basename($caller['file'] ?? 'unknown'),
            'line' => $caller['line'] ?? 0
        ];
    }
}
