package mariadb

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/metrics"
	"github.com/mevdschee/tqdbproxy/parser"
	"github.com/mevdschee/tqdbproxy/replica"
)

const (
	comQuery       = 0x03
	comQuit        = 0x01
	comInitDB      = 0x02
	comFieldList   = 0x04
	comPing        = 0x0e
	comStmtPrepare = 0x16
	comStmtExecute = 0x17
)

// Proxy implements a MariaDB server that forwards to backend
type Proxy struct {
	listen      string
	replicaPool *replica.Pool
	cache       *cache.Cache
	db          *sql.DB
	connID      uint32
}

// New creates a new MariaDB proxy
func New(listen string, pool *replica.Pool, c *cache.Cache) *Proxy {
	return &Proxy{
		listen:      listen,
		replicaPool: pool,
		cache:       c,
		connID:      1000,
	}
}

// Start begins accepting MariaDB connections
func (p *Proxy) Start() error {
	// Connect to backend MariaDB (using tqdbproxy credentials for testing)
	dsn := fmt.Sprintf("tqdbproxy:tqdbproxy@tcp(%s)/", p.replicaPool.GetPrimary())
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to backend: %v", err)
	}
	p.db = db

	listener, err := net.Listen("tcp", p.listen)
	if err != nil {
		return err
	}
	log.Printf("[MariaDB] Listening on %s, forwarding to %s", p.listen, p.replicaPool.GetPrimary())

	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				log.Printf("[MariaDB] Accept error: %v", err)
				continue
			}
			p.connID++
			go p.handleConnection(client, p.connID)
		}
	}()

	return nil
}

func (p *Proxy) handleConnection(client net.Conn, connID uint32) {
	defer client.Close()

	// Create dedicated backend connection for this client
	dsn := fmt.Sprintf("tqdbproxy:tqdbproxy@tcp(%s)/", p.replicaPool.GetPrimary())
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Printf("[MariaDB] Failed to connect to backend for conn %d: %v", connID, err)
		return
	}
	defer db.Close()

	conn := &clientConn{
		conn:       client,
		proxy:      p,
		connID:     connID,
		capability: 0,
		status:     SERVER_STATUS_AUTOCOMMIT,
		sequence:   0,
		backendDB:  db, // Dedicated backend connection
	}

	// Perform handshake
	if err := conn.handshake(); err != nil {
		log.Printf("[MariaDB] Handshake error (conn %d): %v", connID, err)
		return
	}

	// Handle commands
	conn.run()
}

type clientConn struct {
	conn       net.Conn
	proxy      *Proxy
	connID     uint32
	capability uint32
	status     uint16
	sequence   byte
	salt       []byte
	db         string
	backendDB  *sql.DB // Dedicated backend connection

	// Last query metadata for SHOW TQDB STATUS
	lastQueryBackend  string
	lastQueryCacheHit bool
}

func (c *clientConn) handshake() error {
	// Generate salt
	salt, err := GenerateSalt()
	if err != nil {
		return err
	}
	c.salt = salt

	// Send server greeting
	if err := c.writeServerGreeting(); err != nil {
		return err
	}

	// Read client auth response
	if err := c.readClientAuth(); err != nil {
		return err
	}

	// Send OK packet
	c.sequence++
	okPacket := WriteOKPacket(0, 0, c.status, c.capability)
	okPacket[3] = c.sequence
	if _, err := c.conn.Write(okPacket); err != nil {
		return err
	}

	// Don't reset here - let run() reset when it reads the next command
	return nil
}

func (c *clientConn) writeServerGreeting() error {
	data := make([]byte, 4, 128)

	// Protocol version
	data = append(data, 10)

	// Server version
	data = append(data, ServerVersion...)
	data = append(data, 0)

	// Connection ID
	data = append(data, byte(c.connID), byte(c.connID>>8), byte(c.connID>>16), byte(c.connID>>24))

	// Auth plugin data part 1 (8 bytes)
	data = append(data, c.salt[0:8]...)

	// Filler
	data = append(data, 0)

	// Capability flags lower 2 bytes
	capLower := uint16(DEFAULT_CAPABILITY & 0xFFFF)
	data = append(data, byte(capLower), byte(capLower>>8))

	// Character set (utf8mb4_general_ci = 45, or utf8_general_ci = 33)
	data = append(data, 33)

	// Status flags
	data = append(data, byte(c.status), byte(c.status>>8))

	// Capability flags upper 2 bytes
	capUpper := uint16((DEFAULT_CAPABILITY >> 16) & 0xFFFF)
	data = append(data, byte(capUpper), byte(capUpper>>8))

	// Auth plugin data length
	data = append(data, 21)

	// Reserved (10 bytes)
	data = append(data, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)

	// Auth plugin data part 2 (12 bytes + null terminator)
	data = append(data, c.salt[8:20]...)
	data = append(data, 0)

	// Set packet length and sequence
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(data)-4))
	data[3] = c.sequence
	c.sequence++

	_, err := c.conn.Write(data)
	return err
}

func (c *clientConn) readClientAuth() error {
	packet, err := c.readPacket()
	if err != nil {
		return err
	}

	pos := 0

	// Capability flags
	c.capability = binary.LittleEndian.Uint32(packet[pos : pos+4])
	pos += 4

	// Max packet size
	pos += 4

	// Character set
	pos++

	// Reserved (23 bytes)
	pos += 23

	// Username (null-terminated)
	user := string(packet[pos : pos+bytes.IndexByte(packet[pos:], 0)])
	pos += len(user) + 1

	// Auth response length
	authLen := int(packet[pos])
	pos++

	// Auth response
	auth := packet[pos : pos+authLen]
	pos += authLen

	// Database name (if CLIENT_CONNECT_WITH_DB)
	if c.capability&CLIENT_CONNECT_WITH_DB > 0 && pos < len(packet) {
		c.db = string(packet[pos : pos+bytes.IndexByte(packet[pos:], 0)])
		// Execute USE database on backend if database was specified
		if c.db != "" {
			_, err := c.backendDB.Exec(fmt.Sprintf("USE `%s`", c.db))
			if err != nil {
				return err
			}
		}
	}

	// For now, accept any authentication (no password check)
	// In production, you would verify: CalcPassword(c.salt, []byte(password)) == auth
	_ = user
	_ = auth

	return nil
}

func (c *clientConn) run() {
	for {
		packet, err := c.readPacket()
		if err != nil {
			if err != io.EOF {
				log.Printf("[MariaDB] Read error (conn %d): %v", c.connID, err)
			}
			return
		}

		if len(packet) < 1 {
			continue
		}

		cmd := packet[0]
		data := packet[1:]

		if err := c.dispatch(cmd, data); err != nil {
			if err != io.EOF {
				log.Printf("[MariaDB] Command error (conn %d): %v", c.connID, err)
			}
			c.writeError(err)
		}

		c.sequence = 0
	}
}

func (c *clientConn) dispatch(cmd byte, data []byte) error {
	switch cmd {
	case comQuit:
		return io.EOF
	case comInitDB:
		dbName := string(data)
		c.db = dbName
		// Execute USE database on backend
		_, err := c.backendDB.Exec(fmt.Sprintf("USE `%s`", dbName))
		if err != nil {
			return err
		}
		return c.writeOK()
	case comFieldList:
		// COM_FIELD_LIST is deprecated and used for table completion
		// Just return EOF to indicate no fields (client will fall back to other methods)
		return c.writeEOF()
	case comPing:
		return c.writeOK()
	case comQuery:
		return c.handleQuery(string(data))
	case comStmtPrepare:
		return c.handlePrepare(string(data))
	case comStmtExecute:
		return c.handleExecute(data)
	default:
		return fmt.Errorf("command %d not supported", cmd)
	}
}

func (c *clientConn) handleBegin() error {
	_, err := c.backendDB.Exec("BEGIN")
	if err != nil {
		return err
	}
	c.status |= SERVER_STATUS_IN_TRANS
	return c.writeOKWithInfo("")
}

func (c *clientConn) handleCommit() error {
	_, err := c.backendDB.Exec("COMMIT")
	if err != nil {
		return err
	}
	c.status &= ^uint16(SERVER_STATUS_IN_TRANS)
	return c.writeOKWithInfo("")
}

func (c *clientConn) handleRollback() error {
	_, err := c.backendDB.Exec("ROLLBACK")
	if err != nil {
		return err
	}
	c.status &= ^uint16(SERVER_STATUS_IN_TRANS)
	return c.writeOKWithInfo("")
}

func (c *clientConn) handleQuery(query string) error {
	start := time.Now()
	parsed := parser.Parse(query)

	// Debug: log the query and parsed metadata (commented out for performance)
	// log.Printf("[MariaDB] Raw query from client: %q", query)
	// log.Printf("[MariaDB] Parsed - File: %q, Line: %d, TTL: %d, Clean query: %q", parsed.File, parsed.Line, parsed.TTL, parsed.Query)

	file := parsed.File
	if file == "" {
		file = "unknown"
	}
	line := "0"
	if parsed.Line > 0 {
		line = strconv.Itoa(parsed.Line)
	}
	queryType := queryTypeLabel(parsed.Type)

	// Check for transaction commands
	queryUpper := strings.ToUpper(strings.TrimSpace(parsed.Query))
	if queryUpper == "BEGIN" || queryUpper == "START TRANSACTION" {
		return c.handleBegin()
	}
	if queryUpper == "COMMIT" {
		return c.handleCommit()
	}
	if queryUpper == "ROLLBACK" {
		return c.handleRollback()
	}

	// Check for custom SHOW TQDB STATUS command
	if queryUpper == "SHOW TQDB STATUS" {
		return c.handleShowTQDBStatus()
	}

	// Check cache
	if parsed.IsCacheable() {
		if cached, ok := c.proxy.cache.Get(parsed.Query); ok {
			metrics.CacheHits.WithLabelValues(file, line).Inc()
			metrics.QueryTotal.WithLabelValues(file, line, queryType, "true").Inc()
			metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())

			// Track metadata for SHOW TQDB STATUS
			c.lastQueryBackend = "cache"
			c.lastQueryCacheHit = true

			return c.writeRaw(cached)
		}
		metrics.CacheMisses.WithLabelValues(file, line).Inc()
	}

	// For non-SELECT queries (INSERT, UPDATE, DELETE), use Exec instead of Query
	if parsed.Type != parser.QuerySelect {
		result, err := c.backendDB.Exec(parsed.Query)
		if err != nil {
			return err
		}

		affectedRows, _ := result.RowsAffected()
		lastInsertId, _ := result.LastInsertId()

		metrics.DatabaseQueries.WithLabelValues("primary").Inc()
		metrics.QueryTotal.WithLabelValues(file, line, queryType, "false").Inc()
		metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())

		// Track metadata for SHOW TQDB STATUS
		c.lastQueryBackend = "primary"
		c.lastQueryCacheHit = false

		// Send OK packet with affected rows
		c.sequence++
		okPacket := WriteOKPacket(uint64(affectedRows), uint64(lastInsertId), c.status, c.capability)
		okPacket[3] = c.sequence
		_, err = c.conn.Write(okPacket)
		return err
	}

	// Execute query on backend (use cleaned query without metadata)
	rows, err := c.backendDB.Query(parsed.Query)
	if err != nil {
		return err
	}
	defer rows.Close()

	metrics.DatabaseQueries.WithLabelValues("primary").Inc()
	metrics.QueryTotal.WithLabelValues(file, line, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())

	// Track metadata for SHOW TQDB STATUS
	c.lastQueryBackend = "primary"
	c.lastQueryCacheHit = false

	// Build result set
	result, err := c.buildResultSet(rows)
	if err != nil {
		return err
	}

	// Cache if cacheable
	if parsed.IsCacheable() {
		c.proxy.cache.Set(parsed.Query, result, time.Duration(parsed.TTL)*time.Second)
	}

	return c.writeRaw(result)
}

func (c *clientConn) handlePrepare(query string) error {
	// For now, just return an error - prepared statements need more work
	return fmt.Errorf("prepared statements not yet supported in server mode")
}

func (c *clientConn) handleExecute(data []byte) error {
	return fmt.Errorf("prepared statements not yet supported in server mode")
}

func (c *clientConn) buildResultSet(rows *sql.Rows) ([]byte, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var result []byte

	// Column count packet
	colCount := len(columns)
	packet := make([]byte, 4)
	packet = append(packet, PutLengthEncodedInt(uint64(colCount))...)
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
	c.sequence++
	packet[3] = c.sequence
	result = append(result, packet...)

	// Column definition packets (simplified - just send column names)
	for _, col := range columns {
		packet = make([]byte, 4)
		packet = append(packet, 0x03, 'd', 'e', 'f') // catalog
		packet = append(packet, 0)                   // database
		packet = append(packet, 0)                   // table
		packet = append(packet, 0)                   // org_table
		packet = append(packet, PutLengthEncodedInt(uint64(len(col)))...)
		packet = append(packet, []byte(col)...)
		packet = append(packet, 0)                      // org_name
		packet = append(packet, 0x0c)                   // length of fixed fields
		packet = append(packet, 0x21, 0x00)             // character set
		packet = append(packet, 0xff, 0xff, 0xff, 0xff) // column length
		packet = append(packet, 0xfd)                   // type: VAR_STRING
		packet = append(packet, 0x00, 0x00)             // flags
		packet = append(packet, 0x00)                   // decimals
		packet = append(packet, 0x00, 0x00)             // filler

		binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
		c.sequence++
		packet[3] = c.sequence
		result = append(result, packet...)
	}

	// EOF packet after columns
	c.sequence++
	eofPacket := WriteEOFPacket(c.status, c.capability)
	eofPacket[3] = c.sequence
	result = append(result, eofPacket...)

	// Row data packets
	values := make([]interface{}, colCount)
	valuePtrs := make([]interface{}, colCount)
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}

		packet = make([]byte, 4)
		for _, val := range values {
			if val == nil {
				packet = append(packet, 0xfb) // NULL
			} else {
				var str string
				// Convert byte arrays to strings
				switch v := val.(type) {
				case []byte:
					str = string(v)
				default:
					str = fmt.Sprintf("%v", v)
				}
				packet = append(packet, PutLengthEncodedInt(uint64(len(str)))...)
				packet = append(packet, []byte(str)...)
			}
		}

		binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
		c.sequence++
		packet[3] = c.sequence
		result = append(result, packet...)
	}

	// EOF packet after rows
	c.sequence++
	eofPacket = WriteEOFPacket(c.status, c.capability)
	eofPacket[3] = c.sequence
	result = append(result, eofPacket...)

	return result, nil
}

func (c *clientConn) readPacket() ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(c.conn, header); err != nil {
		return nil, err
	}

	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	// Read the client's sequence number and use it as base for our response
	clientSeq := header[3]
	c.sequence = clientSeq

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.conn, payload); err != nil {
		return nil, err
	}

	return payload, nil
}

func (c *clientConn) writeOK() error {
	return c.writeOKWithInfo("")
}

func (c *clientConn) writeOKWithInfo(info string) error {
	c.sequence++
	packet := WriteOKPacket(0, 0, c.status, c.capability)
	packet[3] = c.sequence
	_, err := c.conn.Write(packet)
	return err
}

func (c *clientConn) writeError(e error) error {
	c.sequence++
	packet := WriteErrorPacket(1105, "HY000", e.Error(), c.capability)
	packet[3] = c.sequence
	_, err := c.conn.Write(packet)
	return err
}

func (c *clientConn) writeEOF() error {
	c.sequence++
	packet := WriteEOFPacket(c.status, c.capability)
	packet[3] = c.sequence
	_, err := c.conn.Write(packet)
	return err
}

func (c *clientConn) writeRaw(data []byte) error {
	_, err := c.conn.Write(data)
	return err
}

func queryTypeLabel(t parser.QueryType) string {
	switch t {
	case parser.QuerySelect:
		return "select"
	case parser.QueryInsert:
		return "insert"
	case parser.QueryUpdate:
		return "update"
	case parser.QueryDelete:
		return "delete"
	default:
		return "unknown"
	}
}

func (c *clientConn) handleShowTQDBStatus() error {
	// Build a simple result set following kingshard's approach
	var result []byte

	// Prepare data
	backend := c.lastQueryBackend
	if backend == "" {
		backend = "none"
	}
	cacheHit := "0"
	if c.lastQueryCacheHit {
		cacheHit = "1"
	}

	// Column count packet (2 columns)
	c.sequence++
	packet := make([]byte, 4)
	packet = append(packet, PutLengthEncodedInt(2)...)
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
	packet[3] = c.sequence
	result = append(result, packet...)

	// Column 1: Variable_name (use kingshard's Field.Dump() pattern)
	c.sequence++
	packet = make([]byte, 4)
	packet = append(packet, PutLengthEncodedString([]byte("def"))...)           // catalog
	packet = append(packet, PutLengthEncodedString([]byte(""))...)              // schema
	packet = append(packet, PutLengthEncodedString([]byte(""))...)              // table
	packet = append(packet, PutLengthEncodedString([]byte(""))...)              // org_table
	packet = append(packet, PutLengthEncodedString([]byte("Variable_name"))...) // name
	packet = append(packet, PutLengthEncodedString([]byte(""))...)              // org_name
	packet = append(packet, 0x0c)                                               // filler
	packet = append(packet, 0x21, 0x00)                                         // charset (utf8)
	packet = append(packet, 0x00, 0x01, 0x00, 0x00)                             // column length (256)
	packet = append(packet, 0xfd)                                               // type (VAR_STRING)
	packet = append(packet, 0x00, 0x00)                                         // flags
	packet = append(packet, 0x00)                                               // decimals
	packet = append(packet, 0x00, 0x00)                                         // filler
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
	packet[3] = c.sequence
	result = append(result, packet...)

	// Column 2: Value
	c.sequence++
	packet = make([]byte, 4)
	packet = append(packet, PutLengthEncodedString([]byte("def"))...)   // catalog
	packet = append(packet, PutLengthEncodedString([]byte(""))...)      // schema
	packet = append(packet, PutLengthEncodedString([]byte(""))...)      // table
	packet = append(packet, PutLengthEncodedString([]byte(""))...)      // org_table
	packet = append(packet, PutLengthEncodedString([]byte("Value"))...) // name
	packet = append(packet, PutLengthEncodedString([]byte(""))...)      // org_name
	packet = append(packet, 0x0c)                                       // filler
	packet = append(packet, 0x21, 0x00)                                 // charset (utf8)
	packet = append(packet, 0x00, 0x01, 0x00, 0x00)                     // column length (256)
	packet = append(packet, 0xfd)                                       // type (VAR_STRING)
	packet = append(packet, 0x00, 0x00)                                 // flags
	packet = append(packet, 0x00)                                       // decimals
	packet = append(packet, 0x00, 0x00)                                 // filler
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
	packet[3] = c.sequence
	result = append(result, packet...)

	// EOF after columns
	c.sequence++
	eofPacket := WriteEOFPacket(c.status, c.capability)
	eofPacket[3] = c.sequence
	result = append(result, eofPacket...)

	// Row 1: Backend
	c.sequence++
	packet = make([]byte, 4)
	packet = append(packet, PutLengthEncodedString([]byte("Backend"))...)
	packet = append(packet, PutLengthEncodedString([]byte(backend))...)
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
	packet[3] = c.sequence
	result = append(result, packet...)

	// Row 2: Cache_hit
	c.sequence++
	packet = make([]byte, 4)
	packet = append(packet, PutLengthEncodedString([]byte("Cache_hit"))...)
	packet = append(packet, PutLengthEncodedString([]byte(cacheHit))...)
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(packet)-4))
	packet[3] = c.sequence
	result = append(result, packet...)

	// EOF after rows
	c.sequence++
	eofPacket = WriteEOFPacket(c.status, c.capability)
	eofPacket[3] = c.sequence
	result = append(result, eofPacket...)

	return c.writeRaw(result)
}
