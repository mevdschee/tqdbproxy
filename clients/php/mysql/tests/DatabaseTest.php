<?php

namespace TQDBProxy\MySQL\Tests;

use PHPUnit\Framework\TestCase;
use TQDBProxy\MySQL\Database;
use PDO;

class DatabaseTest extends TestCase
{
    private PDO $mockPdo;
    private Database $db;

    protected function setUp(): void
    {
        // Create an in-memory SQLite database for testing
        $this->mockPdo = new PDO('sqlite::memory:');
        $this->mockPdo->setAttribute(PDO::ATTR_ERRMODE, PDO::ERRMODE_EXCEPTION);

        // Create a test table
        $this->mockPdo->exec('CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)');
        $this->mockPdo->exec("INSERT INTO users (id, name) VALUES (1, 'Alice')");
        $this->mockPdo->exec("INSERT INTO users (id, name) VALUES (2, 'Bob')");

        $this->db = Database::wrap($this->mockPdo);
    }

    public function testConstructor(): void
    {
        $db = new Database('sqlite::memory:');
        $this->assertInstanceOf(Database::class, $db);
    }

    public function testWrap(): void
    {
        $pdo = new PDO('sqlite::memory:');
        $db = Database::wrap($pdo);

        $this->assertInstanceOf(Database::class, $db);
        $this->assertSame($pdo, $db->getPDO());
    }

    public function testQuery(): void
    {
        $stmt = $this->db->query('SELECT * FROM users');
        $results = $stmt->fetchAll(PDO::FETCH_ASSOC);

        $this->assertCount(2, $results);
        $this->assertEquals('Alice', $results[0]['name']);
    }

    public function testQueryWithTTL(): void
    {
        // Execute query with TTL
        $stmt = $this->db->queryWithTTL(60, 'SELECT * FROM users WHERE id = ?', [1]);
        $result = $stmt->fetch(PDO::FETCH_ASSOC);

        $this->assertEquals('Alice', $result['name']);
    }

    public function testQueryWithTTLInjectsHint(): void
    {
        // We can't easily test the exact query sent to SQLite, but we can verify
        // that the query executes successfully with the hint prepended
        $stmt = $this->db->queryWithTTL(120, 'SELECT * FROM users WHERE id = ?', [2]);
        $result = $stmt->fetch(PDO::FETCH_ASSOC);

        $this->assertEquals('Bob', $result['name']);
    }

    public function testPrepare(): void
    {
        $stmt = $this->db->prepare('SELECT * FROM users WHERE id = ?');
        $stmt->execute([1]);
        $result = $stmt->fetch(PDO::FETCH_ASSOC);

        $this->assertEquals('Alice', $result['name']);
    }

    public function testGetPDO(): void
    {
        $pdo = $this->db->getPDO();
        $this->assertInstanceOf(PDO::class, $pdo);
        $this->assertSame($this->mockPdo, $pdo);
    }

    public function testQueryWithTTLCapturesCallerMetadata(): void
    {
        // This test verifies that queryWithTTL doesn't throw an error
        // when capturing caller metadata
        $stmt = $this->db->queryWithTTL(30, 'SELECT * FROM users');
        $results = $stmt->fetchAll(PDO::FETCH_ASSOC);

        $this->assertCount(2, $results);
    }
}
