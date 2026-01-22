import { Database } from '../src/index';

// Mock pg module
jest.mock('pg', () => ({
    Client: jest.fn(),
}));

describe('Database - Caller Metadata Tests', () => {
    let mockClient: any;
    let mockQuery: jest.Mock;

    beforeEach(() => {
        mockQuery = jest.fn().mockResolvedValue({ rows: [], rowCount: 0 });
        mockClient = {
            query: mockQuery,
            connect: jest.fn().mockResolvedValue(undefined),
            end: jest.fn().mockResolvedValue(undefined),
        };

        const { Client } = require('pg');
        Client.mockImplementation(() => mockClient);
    });

    afterEach(() => {
        jest.clearAllMocks();
    });

    test('queryWithTTL injects TTL and caller metadata', async () => {
        const db = Database.wrap(mockClient);

        await db.queryWithTTL(60, 'SELECT * FROM users');

        expect(mockQuery).toHaveBeenCalledTimes(1);
        const capturedQuery = mockQuery.mock.calls[0][0];

        // Verify TTL hint
        expect(capturedQuery).toContain('/* ttl:60');
        // Verify caller metadata
        expect(capturedQuery).toContain('file:');
        expect(capturedQuery).toContain('line:');
        expect(capturedQuery).toContain('index.test.ts');
        // Verify original query
        expect(capturedQuery).toContain('SELECT * FROM users');
    });

    test('query injects caller metadata', async () => {
        const db = Database.wrap(mockClient);

        await db.query('SELECT * FROM users');

        expect(mockQuery).toHaveBeenCalledTimes(1);
        const capturedQuery = mockQuery.mock.calls[0][0];

        // Verify caller metadata (no TTL)
        expect(capturedQuery).toContain('/* file:');
        expect(capturedQuery).toContain('line:');
        expect(capturedQuery).toContain('index.test.ts');
        // Verify original query
        expect(capturedQuery).toContain('SELECT * FROM users');
        // Should NOT contain TTL
        expect(capturedQuery).not.toContain('ttl:');
    });

    test('queryWithTTL with parameters injects metadata correctly', async () => {
        const db = Database.wrap(mockClient);

        await db.queryWithTTL(120, 'SELECT * FROM users WHERE id = $1', [42]);

        expect(mockQuery).toHaveBeenCalledTimes(1);
        const capturedQuery = mockQuery.mock.calls[0][0];
        const capturedParams = mockQuery.mock.calls[0][1];

        // Verify TTL and metadata
        expect(capturedQuery).toContain('/* ttl:120');
        expect(capturedQuery).toContain('file:');
        expect(capturedQuery).toContain('line:');
        // Verify parameters are passed through
        expect(capturedParams).toEqual([42]);
    });
});
