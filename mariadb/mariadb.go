package mariadb

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/config"
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
	config config.ProxyConfig
	pools  map[string]*replica.Pool
	cache  *cache.Cache
	db     *sql.DB
	connID uint32
	mu     sync.RWMutex
}

// New creates a new MariaDB proxy
func New(pcfg config.ProxyConfig, pools map[string]*replica.Pool, c *cache.Cache) *Proxy {
	return &Proxy{
		config: pcfg,
		pools:  pools,
		cache:  c,
		connID: 1000,
	}
}

// UpdateConfig updates the proxy configuration and pools
func (p *Proxy) UpdateConfig(pcfg config.ProxyConfig, pools map[string]*replica.Pool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config = pcfg
	p.pools = pools
}

// Start begins accepting MariaDB connections
func (p *Proxy) Start() error {
	p.mu.RLock()
	listen := p.config.Listen
	socket := p.config.Socket
	defaultBackend := p.config.Default
	defaultPool := p.pools[defaultBackend]
	p.mu.RUnlock()

	if defaultPool == nil {
		return fmt.Errorf("default backend pool %q not found", defaultBackend)
	}

	// Connect to backend MariaDB (using tqdbproxy credentials for testing)
	addr := defaultPool.GetPrimary()
	dsn := fmt.Sprintf("tqdbproxy:tqdbproxy@tcp(%s)/", addr)
	if len(addr) > 5 && addr[:5] == "unix:" {
		dsn = fmt.Sprintf("tqdbproxy:tqdbproxy@unix(%s)/", addr[5:])
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to backend: %v", err)
	}
	p.db = db

	// Start TCP listener
	tcpListener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	log.Printf("[MariaDB] Listening on %s (tcp), forwarding to %v backends", listen, len(p.pools))

	go p.acceptLoop(tcpListener)

	// Start Unix socket listener if configured
	if socket != "" {
		// Remove existing socket file if present
		if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
			log.Printf("[MariaDB] Warning: could not remove existing socket: %v", err)
		}
		unixListener, err := net.Listen("unix", socket)
		if err != nil {
			return fmt.Errorf("failed to listen on unix socket: %v", err)
		}
		log.Printf("[MariaDB] Listening on %s (unix)", socket)
		go p.acceptLoop(unixListener)
	}

	return nil
}

func (p *Proxy) acceptLoop(listener net.Listener) {
	for {
		client, err := listener.Accept()
		if err != nil {
			log.Printf("[MariaDB] Accept error: %v", err)
			continue
		}
		p.connID++
		go p.handleConnection(client, p.connID)
	}
}

func (p *Proxy) handleConnection(client net.Conn, connID uint32) {
	defer client.Close()

	p.mu.RLock()
	defaultPool := p.pools[p.config.Default]
	p.mu.RUnlock()

	// Connect to default backend FIRST to get its salt
	addr := defaultPool.GetPrimary()
	network := "tcp"
	dialAddr := addr
	if len(addr) > 5 && addr[:5] == "unix:" {
		network = "unix"
		dialAddr = addr[5:]
	}

	backend, err := net.Dial(network, dialAddr)
	if err != nil {
		log.Printf("[MariaDB] Failed to connect to backend for conn %d: %v", connID, err)
		return
	}
	defer backend.Close()

	conn := &clientConn{
		conn:        client,
		backend:     backend,
		backendPool: defaultPool,
		backendAddr: dialAddr,
		backendName: "primary",
		proxy:       p,
		connID:      connID,
		capability:  0,
		status:      SERVER_STATUS_AUTOCOMMIT,
		sequence:    0,
	}

	// Perform handshake with salt forwarding
	if err := conn.handshake(); err != nil {
		log.Printf("[MariaDB] Handshake error (conn %d): %v", connID, err)
		return
	}

	// Handle commands
	conn.run()
}

type clientConn struct {
	mu          sync.Mutex
	conn        net.Conn
	backend     net.Conn // Raw TCP connection to backend
	backendPool *replica.Pool
	proxy       *Proxy
	connID      uint32
	capability  uint32
	status      uint16
	sequence    byte
	backendSeq  byte // Sequence number for backend
	salt        []byte
	db          string
	user        string // Client username
	auth        []byte // Client auth response
	rawAuthPkt  []byte // Original client auth packet to forward

	// Backend connection state
	backendAddr string
	backendName string // "primary", "replicas[0]", etc.

	// Last query metadata for SHOW TQDB STATUS
	lastQueryBackend  string
	lastQueryShard    string
	lastQueryCacheHit bool
}

func (c *clientConn) handshake() error {
	// Step 1: Read backend's greeting to get its salt
	backendGreeting, err := c.readBackendPacket()
	if err != nil {
		return fmt.Errorf("failed to read backend greeting: %v", err)
	}

	// Parse backend greeting to extract salt (for our internal use)
	if len(backendGreeting) < 44 {
		return fmt.Errorf("backend greeting too short")
	}

	// Skip protocol version (1 byte) and version string (null-terminated)
	pos := 1
	for pos < len(backendGreeting) && backendGreeting[pos] != 0 {
		pos++
	}
	pos++ // skip null terminator

	// Skip connection ID (4 bytes)
	pos += 4

	// Auth plugin data part 1 (8 bytes) = salt[0:8]
	salt1 := backendGreeting[pos : pos+8]
	pos += 8

	// Skip filler (1 byte)
	pos++

	// Skip capability flags lower (2 bytes), charset (1), status (2), cap upper (2)
	pos += 7

	// Auth plugin data length (1 byte)
	authDataLen := int(backendGreeting[pos])
	pos++

	// Skip reserved (10 bytes)
	pos += 10

	// Auth plugin data part 2 (remaining of 21 bytes)
	var salt2 []byte
	if authDataLen > 8 && pos+12 <= len(backendGreeting) {
		salt2 = backendGreeting[pos : pos+12]
	}

	// Combine salt parts (for internal use)
	c.salt = make([]byte, 20)
	copy(c.salt[0:8], salt1)
	if len(salt2) >= 12 {
		copy(c.salt[8:20], salt2)
	}

	// Step 2: Forward the backend's greeting directly to the client
	// This ensures client sees exact capabilities backend expects
	greetingPacket := make([]byte, 4+len(backendGreeting))
	binary.LittleEndian.PutUint32(greetingPacket[0:4], uint32(len(backendGreeting)))
	greetingPacket[3] = c.sequence
	copy(greetingPacket[4:], backendGreeting)
	if _, err := c.conn.Write(greetingPacket); err != nil {
		return err
	}
	c.sequence++

	// Initialize shard name for status
	c.lastQueryShard = c.proxy.config.Default

	// Step 3: Read client auth response
	if err := c.readClientAuth(); err != nil {
		return err
	}

	// Step 4: Forward client auth to backend
	if err := c.forwardClientAuth(); err != nil {
		return err
	}

	// Step 5: Read backend's auth response
	backendResponse, err := c.readBackendPacket()
	if err != nil {
		return fmt.Errorf("failed to read backend auth response: %v", err)
	}

	// Check if auth succeeded (OK packet starts with 0x00, Error with 0xFF)
	if len(backendResponse) > 0 && backendResponse[0] == 0xFF {
		// Forward error to client with proper packet header
		c.sequence++
		length := len(backendResponse)
		packet := make([]byte, 4+length)
		packet[0] = byte(length)
		packet[1] = byte(length >> 8)
		packet[2] = byte(length >> 16)
		packet[3] = c.sequence
		copy(packet[4:], backendResponse)
		c.conn.Write(packet)
		return fmt.Errorf("backend auth failed")
	}

	// Check for auth switch request (0xFE)
	if len(backendResponse) > 0 && backendResponse[0] == 0xFE {
		return fmt.Errorf("auth switch request not supported")
	}

	// Step 6: Forward OK response to client
	if len(backendResponse) > 0 && backendResponse[0] == 0x00 {
		// OK packet: 00 <lenenc_rows> <lenenc_id> <status_flags(2)>
		statusOffset := 1
		_, _, n1 := ReadLengthEncodedInt(backendResponse[statusOffset:])
		statusOffset += n1
		_, _, n2 := ReadLengthEncodedInt(backendResponse[statusOffset:])
		statusOffset += n2
		if len(backendResponse) >= statusOffset+2 {
			c.status = binary.LittleEndian.Uint16(backendResponse[statusOffset:])
		}
	}

	c.sequence++
	okPacket := make([]byte, 4+len(backendResponse))
	okPacket[0] = byte(len(backendResponse))
	okPacket[1] = byte(len(backendResponse) >> 8)
	okPacket[2] = byte(len(backendResponse) >> 16)
	okPacket[3] = c.sequence
	copy(okPacket[4:], backendResponse)
	if _, err := c.conn.Write(okPacket); err != nil {
		return err
	}

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
	}

	// Store username and auth for backend connection
	c.user = user
	c.auth = auth
	c.rawAuthPkt = packet // Store original packet for forwarding

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

func (c *clientConn) ensureBackend(db string) error {
	c.proxy.mu.RLock()
	shardName := c.proxy.config.DBMap[db]
	if shardName == "" {
		shardName = c.proxy.config.Default
	}
	targetPool := c.proxy.pools[shardName]
	c.proxy.mu.RUnlock()

	if targetPool == nil {
		return fmt.Errorf("no backend pool found for database %q", db)
	}

	c.lastQueryShard = shardName

	if targetPool == c.backendPool {
		return nil // Already on the right shard
	}

	// When switching shards, we default to the primary of that shard
	addr := targetPool.GetPrimary()
	return c.ensureBackendConn(addr, "primary", targetPool)
}

func (c *clientConn) ensureBackendConn(addr string, name string, pool *replica.Pool) error {
	if addr == c.backendAddr && c.backend != nil {
		return nil // Already on the right connection
	}

	log.Printf("[MariaDB] Switching backend for conn %d: %s -> %s (%s)", c.connID, c.backendAddr, addr, name)

	// Reconnect and re-authenticate
	network := "tcp"
	dialAddr := addr
	if len(addr) > 5 && addr[:5] == "unix:" {
		network = "unix"
		dialAddr = addr[5:]
	}

	newBackend, err := net.Dial(network, dialAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to new backend: %v", err)
	}

	// Read greeting from new backend
	header := make([]byte, 4)
	if _, err := io.ReadFull(newBackend, header); err != nil {
		newBackend.Close()
		return fmt.Errorf("failed to read from new backend: %v", err)
	}
	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	payload := make([]byte, length)
	if _, err := io.ReadFull(newBackend, payload); err != nil {
		newBackend.Close()
		return err
	}

	// Forward original auth to new backend
	c.backendSeq = header[3] + 1
	packet := make([]byte, 4+len(c.rawAuthPkt))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(c.rawAuthPkt)))
	packet[3] = c.backendSeq
	copy(packet[4:], c.rawAuthPkt)
	if _, err := newBackend.Write(packet); err != nil {
		newBackend.Close()
		return err
	}

	// Read auth response
	if _, err := io.ReadFull(newBackend, header); err != nil {
		newBackend.Close()
		return err
	}
	length = int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	payload = make([]byte, length)
	if _, err := io.ReadFull(newBackend, payload); err != nil {
		newBackend.Close()
		return err
	}

	if len(payload) > 0 && payload[0] == 0xFF {
		newBackend.Close()
		return fmt.Errorf("backend switch auth failed (likely salt mismatch)")
	}

	// Switch successful
	c.backend.Close()
	c.backend = newBackend
	c.backendPool = pool
	c.backendAddr = addr
	c.backendName = name
	c.backendSeq = header[3]

	return nil
}

func (c *clientConn) dispatch(cmd byte, data []byte) error {
	switch cmd {
	case comQuit:
		return io.EOF
	case comInitDB:
		c.mu.Lock()
		dbName := string(data)
		if err := c.ensureBackend(dbName); err != nil {
			c.mu.Unlock()
			return err
		}
		c.db = dbName
		// Execute USE database on backend
		_, err := c.execBackendQuery(fmt.Sprintf("USE `%s`", dbName))
		c.mu.Unlock()
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
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.handlePrepare(string(data))
	case comStmtExecute:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.handleExecute(data)
	default:
		return fmt.Errorf("command %d not supported", cmd)
	}
}

func (c *clientConn) handleBegin(moreResults bool) error {
	_, err := c.execBackendQuery("BEGIN")
	if err != nil {
		return err
	}
	c.status |= SERVER_STATUS_IN_TRANS
	return c.writeOKWithInfo("", moreResults)
}

func (c *clientConn) handleCommit(moreResults bool) error {
	_, err := c.execBackendQuery("COMMIT")
	if err != nil {
		return err
	}
	c.status &= ^uint16(SERVER_STATUS_IN_TRANS)
	return c.writeOKWithInfo("", moreResults)
}

func (c *clientConn) handleRollback(moreResults bool) error {
	_, err := c.execBackendQuery("ROLLBACK")
	if err != nil {
		return err
	}
	c.status &= ^uint16(SERVER_STATUS_IN_TRANS)
	return c.writeOKWithInfo("", moreResults)
}

func (c *clientConn) handleQuery(query string) error {
	start := time.Now()
	parsed := parser.Parse(query)

	// Split multi-statement queries
	statements := splitQueries(parsed.Query)
	for i, stmt := range statements {
		moreResults := (i < len(statements)-1)
		// Process each statement
		if err := c.handleSingleQuery(stmt, parsed, start, moreResults); err != nil {
			return err
		}
	}
	return nil
}

func (c *clientConn) handleSingleQuery(query string, originalParsed *parser.ParsedQuery, start time.Time, moreResults bool) error {
	// Re-parse the single statement if it's different from the original
	parsed := originalParsed
	if query != originalParsed.Query {
		parsed = parser.Parse(query)
	}

	// Lock the session for the duration of a single statement processing
	// This prevents background refreshes from interleaving with the main query stream
	c.mu.Lock()
	defer c.mu.Unlock()

	file := parsed.File
	if file == "" {
		file = "unknown"
	}
	line := originalParsed.Line // Use line from original if available
	if parsed.Line > 0 {
		line = parsed.Line
	}
	lineStr := strconv.Itoa(line)
	queryType := queryTypeLabel(parsed.Type)

	queryUpper := strings.ToUpper(strings.TrimSpace(parsed.Query))
	queryUpper = strings.TrimSuffix(queryUpper, ";")

	// Check for USE statement
	if strings.HasPrefix(queryUpper, "USE ") {
		parts := strings.Fields(parsed.Query)
		if len(parts) >= 2 {
			dbName := strings.Trim(parts[1], "`;")
			if err := c.ensureBackend(dbName); err != nil {
				return err
			}
			c.db = dbName
			return c.writeOKWithInfo("", moreResults)
		}
	}

	// Check for transaction commands
	if queryUpper == "BEGIN" || queryUpper == "START TRANSACTION" {
		return c.handleBegin(moreResults)
	}
	if queryUpper == "COMMIT" {
		return c.handleCommit(moreResults)
	}
	if queryUpper == "ROLLBACK" {
		return c.handleRollback(moreResults)
	}

	// Check for custom SHOW TQDB STATUS command
	if queryUpper == "SHOW TQDB STATUS" {
		return c.handleShowTQDBStatus(moreResults)
	}

	// Check cache with thundering herd protection
	if parsed.IsCacheable() {
		cached, flags, ok := c.proxy.cache.Get(parsed.Query)
		if ok {
			// Handle stale data with background refresh
			if flags == cache.FlagRefresh {
				// First stale access - trigger background refresh
				go c.refreshQueryInBackground(parsed.Query, parsed.TTL)
			}
			// Serve from cache (fresh or stale)
			metrics.CacheHits.WithLabelValues(file, lineStr).Inc()
			metrics.QueryTotal.WithLabelValues(file, lineStr, queryType, "true").Inc()
			metrics.QueryLatency.WithLabelValues(file, lineStr, queryType).Observe(time.Since(start).Seconds())

			backend := "cache"
			if flags != cache.FlagFresh {
				backend = "cache (stale)"
			}
			c.lastQueryBackend = backend
			c.lastQueryCacheHit = true

			return c.forwardBackendResponse(cached, moreResults)
		}
		metrics.CacheMisses.WithLabelValues(file, lineStr).Inc()

		// Cold cache: use single-flight pattern
		cached, _, ok, waited := c.proxy.cache.GetOrWait(parsed.Query)
		if waited && ok {
			// Another goroutine fetched it for us
			metrics.CacheHits.WithLabelValues(file, lineStr).Inc()
			c.lastQueryBackend = "cache"
			c.lastQueryCacheHit = true
			return c.forwardBackendResponse(cached, moreResults)
		}
		// We need to fetch from DB (either first request or waited but still miss)
	}

	// Select backend
	backendAddr := c.backendPool.GetPrimary()
	backendName := "primary"

	if parsed.IsCacheable() && (c.status&SERVER_STATUS_IN_TRANS == 0) {
		backendAddr, backendName = c.backendPool.GetReplica()
	}
	// Ensure we are connected to the right backend
	if err := c.ensureBackendConn(backendAddr, backendName, c.backendPool); err != nil {
		// Cancel inflight if we were the first request
		if parsed.IsCacheable() {
			c.proxy.cache.CancelInflight(parsed.Query)
		}
		return err
	}

	// Execute query on backend via raw socket
	response, err := c.execBackendQuery(parsed.Query)
	if err != nil {
		// Cancel inflight if we were the first request
		if parsed.IsCacheable() {
			c.proxy.cache.CancelInflight(parsed.Query)
		}
		return err
	}

	metrics.DatabaseQueries.WithLabelValues(backendName).Inc()
	metrics.QueryTotal.WithLabelValues(file, lineStr, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, lineStr, queryType).Observe(time.Since(start).Seconds())

	// Track metadata for SHOW TQDB STATUS
	c.lastQueryBackend = backendName
	c.lastQueryCacheHit = false

	// Cache if cacheable (SELECT queries) - use SetAndNotify for single-flight
	if parsed.IsCacheable() {
		c.proxy.cache.SetAndNotify(parsed.Query, response, time.Duration(parsed.TTL)*time.Second)
	}

	// Forward the response to client, adjusting sequence numbers
	return c.forwardBackendResponse(response, moreResults)
}

func (c *clientConn) handlePrepare(query string) error {
	// For now, just return an error - prepared statements need more work
	return fmt.Errorf("prepared statements not yet supported in server mode")
}

// refreshQueryInBackground refreshes a stale cache entry.
// Called when cache.FlagRefresh is returned (first stale access).
func (c *clientConn) refreshQueryInBackground(query string, ttl int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	parsed := parser.Parse(query)
	response, err := c.execBackendQuery(parsed.Query)
	if err != nil {
		log.Printf("[MariaDB] Background refresh failed for query: %v", err)
		return
	}
	// Update cache with fresh data - use the passed TTL
	c.proxy.cache.Set(parsed.Query, response, time.Duration(ttl)*time.Second)
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

func (c *clientConn) readBackendPacket() ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(c.backend, header); err != nil {
		return nil, err
	}

	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	c.backendSeq = header[3]

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.backend, payload); err != nil {
		return nil, err
	}

	return payload, nil
}

func (c *clientConn) writeBackendPacket(payload []byte) error {
	c.backendSeq++
	packet := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(payload)))
	packet[3] = c.backendSeq
	copy(packet[4:], payload)
	_, err := c.backend.Write(packet)
	return err
}

func (c *clientConn) forwardClientAuth() error {
	// Forward the original client auth packet directly to backend
	c.backendSeq++
	packet := make([]byte, 4+len(c.rawAuthPkt))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(c.rawAuthPkt)))
	packet[3] = c.backendSeq
	copy(packet[4:], c.rawAuthPkt)
	_, err := c.backend.Write(packet)
	return err
}

// execBackendQuery sends a query to backend and returns the full response
func (c *clientConn) execBackendQuery(query string) ([]byte, error) {
	// Build COM_QUERY packet
	payload := make([]byte, 1+len(query))
	payload[0] = comQuery
	copy(payload[1:], query)

	// Reset backend sequence for new command
	c.backendSeq = 255 // Will wrap to 0 on first write

	if err := c.writeBackendPacket(payload); err != nil {
		return nil, err
	}

	// Read all response packets until we get an EOF or OK or Error
	var response []byte
	eofCount := 0
	for {
		packet, err := c.readBackendPacket()
		if err != nil {
			return nil, err
		}

		// Add packet with header to response
		header := make([]byte, 4)
		binary.LittleEndian.PutUint32(header[0:4], uint32(len(packet)))
		header[3] = c.backendSeq
		response = append(response, header...)
		response = append(response, packet...)

		// Check packet type
		if len(packet) > 0 {
			switch packet[0] {
			case 0x00: // OK packet
				// OK packet: 00 <lenenc_rows> <lenenc_id> <status_flags(2)>
				if eofCount == 0 {
					statusOffset := 1
					_, _, n1 := ReadLengthEncodedInt(packet[statusOffset:])
					statusOffset += n1
					_, _, n2 := ReadLengthEncodedInt(packet[statusOffset:])
					statusOffset += n2
					if len(packet) >= statusOffset+2 {
						status := binary.LittleEndian.Uint16(packet[statusOffset:])
						if status&SERVER_MORE_RESULTS_EXISTS == 0 {
							return response, nil
						}
						// More results coming (e.g. from multi-statement call or procedure)
						eofCount = 0
						continue
					}
					return response, nil
				}
			case 0xFF: // Error packet
				return response, nil
			case 0xFE: // EOF packet
				if len(packet) < 9 {
					eofCount++
					// Result sets have 2 EOFs: after columns and after rows
					if eofCount >= 2 {
						// EOF packet: FE <warnings(2)> <status_flags(2)>
						if len(packet) >= 5 {
							status := binary.LittleEndian.Uint16(packet[3:])
							if status&SERVER_MORE_RESULTS_EXISTS == 0 {
								return response, nil
							}
							// More results coming
							eofCount = 0
							continue
						}
						return response, nil
					}
				}
			}
		}
	}
}

// forwardBackendResponse forwards a backend response to the client with adjusted sequence numbers
func (c *clientConn) forwardBackendResponse(response []byte, moreResults bool) error {
	// IMPORTANT: Do NOT mutate the input slice in place if it might be from the cache
	respCopy := make([]byte, len(response))
	copy(respCopy, response)

	// Parse and rewrite sequence numbers in the response
	pos := 0
	var lastPacketPos int
	for pos < len(respCopy) {
		if pos+4 > len(respCopy) {
			break
		}

		// Read packet length
		length := int(uint32(respCopy[pos]) | uint32(respCopy[pos+1])<<8 | uint32(respCopy[pos+2])<<16)

		// Update sequence number
		c.sequence++
		respCopy[pos+3] = c.sequence

		lastPacketPos = pos
		// Move to next packet
		pos += 4 + length
	}

	// If more results follow, set the SERVER_MORE_RESULTS_EXISTS flag in the last packet
	if moreResults && lastPacketPos < len(respCopy) {
		packet := respCopy[lastPacketPos:]
		if len(packet) > 4 {
			header := packet[4]
			if header == OK_HEADER {
				// OK packet: 00 <lenenc_rows> <lenenc_id> <status_flags(2)>
				statusOffset := 5
				_, _, n1 := ReadLengthEncodedInt(packet[statusOffset:])
				statusOffset += n1
				_, _, n2 := ReadLengthEncodedInt(packet[statusOffset:])
				statusOffset += n2
				if len(packet) >= statusOffset+2 {
					status := binary.LittleEndian.Uint16(packet[statusOffset:])
					status |= SERVER_MORE_RESULTS_EXISTS
					binary.LittleEndian.PutUint16(packet[statusOffset:], status)
				}
			} else if header == EOF_HEADER {
				// EOF packet: FE <warnings(2)> <status_flags(2)>
				if len(packet) >= 9 {
					status := binary.LittleEndian.Uint16(packet[7:9])
					status |= SERVER_MORE_RESULTS_EXISTS
					binary.LittleEndian.PutUint16(packet[7:9], status)
				}
			}
		}
	}

	_, err := c.conn.Write(respCopy)
	return err
}

func (c *clientConn) writeOK() error {
	return c.writeOKWithInfo("", false)
}

func (c *clientConn) writeOKWithInfo(info string, moreResults bool) error {
	c.sequence++
	status := c.status
	if moreResults {
		status |= SERVER_MORE_RESULTS_EXISTS
	}
	packet := WriteOKPacket(0, 0, status, c.capability)
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

func (c *clientConn) writeAuthError(user string) error {
	c.sequence++
	msg := fmt.Sprintf("Access denied for user '%s'@'localhost' (using password: YES)", user)
	packet := WriteErrorPacket(1045, "28000", msg, c.capability)
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

func (c *clientConn) handleShowTQDBStatus(moreResults bool) error {
	// Ensure we have a backend connection to execute the synthetic query
	if err := c.ensureBackend(""); err != nil {
		return err
	}

	backend := c.lastQueryBackend
	if backend == "" {
		backend = "none"
	}
	shard := c.lastQueryShard
	if shard == "" {
		shard = c.proxy.config.Default
	}

	// Build a synthetic query that returns the status as a result set
	query := fmt.Sprintf("SELECT 'Shard' AS `Variable_name`, '%s' AS `Value` UNION ALL SELECT 'Backend', '%s'", shard, backend)
	response, err := c.execBackendQuery(query)
	if err != nil {
		return err
	}

	return c.forwardBackendResponse(response, moreResults)
}
func splitQueries(query string) []string {
	var queries []string
	var current strings.Builder
	inQuote := false
	quoteChar := rune(0)
	escaped := false

	for _, r := range query {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			current.WriteRune(r)
			escaped = true
			continue
		}
		if (r == '\'' || r == '"' || r == '`') && !escaped {
			if !inQuote {
				inQuote = true
				quoteChar = r
			} else if r == quoteChar {
				inQuote = false
			}
		}
		if r == ';' && !inQuote {
			q := strings.TrimSpace(current.String())
			if q != "" {
				queries = append(queries, q)
			}
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	q := strings.TrimSpace(current.String())
	if q != "" {
		queries = append(queries, q)
	}
	return queries
}
