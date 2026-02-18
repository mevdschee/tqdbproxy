package mariadb

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/config"
	"github.com/mevdschee/tqdbproxy/metrics"
	"github.com/mevdschee/tqdbproxy/parser"
	"github.com/mevdschee/tqdbproxy/replica"
	"github.com/mevdschee/tqdbproxy/writebatch"
)

const (
	backendTimeout = 30 * time.Second
)

// Proxy implements a MariaDB server that forwards to backend
type Proxy struct {
	config     config.ProxyConfig
	pools      map[string]*replica.Pool
	cache      *cache.Cache
	db         *sql.DB
	connID     uint32 // Managed atomically
	listeners  []net.Listener
	mu         sync.RWMutex
	writeBatch *writebatch.Manager
	wbCtx      context.Context
	wbCancel   context.CancelFunc
}

// New creates a new MariaDB proxy
func New(pcfg config.ProxyConfig, pools map[string]*replica.Pool, c *cache.Cache) *Proxy {
	p := &Proxy{
		config: pcfg,
		pools:  pools,
		cache:  c,
		connID: 1000,
	}

	// Initialize write batching (actual manager created in Start after db connection)
	p.wbCtx, p.wbCancel = context.WithCancel(context.Background())
	log.Printf("[MariaDB] Write batching will be enabled (batch size: %d)",
		pcfg.WriteBatch.MaxBatchSize)

	return p
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
	dsn := fmt.Sprintf("tqdbproxy:tqdbproxy@tcp(%s)/tqdbproxy", addr)
	if len(addr) > 5 && addr[:5] == "unix:" {
		dsn = fmt.Sprintf("tqdbproxy:tqdbproxy@unix(%s)/tqdbproxy", addr[5:])
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("failed to connect to backend: %v", err)
	}
	p.db = db

	// Initialize write batching
	wbCfg := writebatch.Config{
		MaxBatchSize: p.config.WriteBatch.MaxBatchSize,
	}
	p.writeBatch = writebatch.New(db, wbCfg)
	log.Printf("[MariaDB] Write batching started")

	// Start TCP listener
	tcpListener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	p.listeners = append(p.listeners, tcpListener)
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
		p.listeners = append(p.listeners, unixListener)
		log.Printf("[MariaDB] Listening on %s (unix)", socket)
		go p.acceptLoop(unixListener)
	}

	return nil
}

// Stop closes all listeners and the database connection
func (p *Proxy) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Stop write batching
	if p.writeBatch != nil {
		if err := p.writeBatch.Close(); err != nil {
			log.Printf("[MariaDB] Error closing write batch manager: %v", err)
		}
	}
	if p.wbCancel != nil {
		p.wbCancel()
	}

	var errs []error
	for _, listener := range p.listeners {
		if err := listener.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	p.listeners = nil

	if p.db != nil {
		if err := p.db.Close(); err != nil {
			errs = append(errs, err)
		}
		p.db = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors during shutdown: %v", errs)
	}
	return nil
}

func (p *Proxy) acceptLoop(listener net.Listener) {
	for {
		client, err := listener.Accept()
		if err != nil {
			// Check if listener was closed
			if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
				return
			}
			log.Printf("[MariaDB] Accept error: %v", err)
			continue
		}
		connID := atomic.AddUint32(&p.connID, 1)
		go p.handleConnection(client, connID)
	}
}

func (p *Proxy) handleConnection(client net.Conn, connID uint32) {
	defer client.Close()

	p.mu.RLock()
	defaultPool := p.pools[p.config.Default]
	p.mu.RUnlock()

	conn := &clientConn{
		conn:               client,
		backendPool:        defaultPool,
		proxy:              p,
		connID:             connID,
		capability:         0,
		status:             mysql.StatusInAutocommit,
		sequence:           0,
		preparedStatements: make(map[uint32]*parser.ParsedQuery),
	}

	// For the initial connection, we don't have the username yet.
	// We must first read the client auth packet to get the username.
	// But to get the client auth packet, we first need to send a greeting with a salt.
	// So we connect to the backend FIRST to get its salt.

	addr := defaultPool.GetPrimary()
	backend, err := conn.dialAndAuth(addr)
	if err != nil {
		log.Printf("[MariaDB] Initial connection/auth error (conn %d): %v", connID, err)
		return
	}
	defer backend.Close()

	conn.backend = backend
	conn.backendAddr = addr
	conn.backendName = "primary"

	// If client specified a database in handshake, select it on backend
	if conn.db != "" {
		if _, err := conn.execBackendQuery(fmt.Sprintf("USE `%s`", conn.db)); err != nil {
			log.Printf("[MariaDB] Failed to select initial database %s: %v", conn.db, err)
		}
	}

	// Successfully authenticated both client and backend
	conn.run()
}

type clientConn struct {
	mu          sync.Mutex
	conn        net.Conn
	backend     net.Conn // Raw TCP connection to backend
	backendPool *replica.Pool
	proxy       *Proxy
	connID      uint32
	capability  mysql.CapabilityFlag
	status      mysql.StatusFlag
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
	lastBatchSize     int

	// Prepared statements
	preparedStatements map[uint32]*parser.ParsedQuery

	// Transaction state
	inTransaction bool
}

func (c *clientConn) writeServerGreeting() error {
	payload := mysql.WriteHandshakeV10(c.connID, c.salt, c.capability, c.status)
	c.sequence = 255 // writePacket will increment this to 0
	return c.writePacket(payload)
}

func (c *clientConn) readClientAuth() error {
	packet, err := c.readPacket()
	if err != nil {
		return err
	}

	hr, err := mysql.ParseHandshakeResponse(packet)
	if err != nil {
		return err
	}

	// Store username and auth for backend connection
	c.capability = hr.Capability
	c.user = hr.User
	c.auth = hr.Auth
	c.db = hr.DB
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

	if targetPool == c.backendPool && c.backend != nil {
		return nil // Already on the right shard with valid connection
	}

	// When switching shards or reconnecting, we default to the primary of that shard
	addr := targetPool.GetPrimary()
	return c.ensureBackendConn(addr, "primary", targetPool)
}

func (c *clientConn) ensureBackendConn(addr string, name string, pool *replica.Pool) error {
	if addr == c.backendAddr && c.backend != nil {
		return nil // Already on the right connection
	}

	log.Printf("[MariaDB] Switching backend for conn %d: %s -> %s (%s)", c.connID, c.backendAddr, addr, name)

	newBackend, err := c.dialAndAuth(addr)
	if err != nil {
		return err
	}

	// Switch successful
	if c.backend != nil {
		c.backend.Close()
	}
	c.backend = newBackend
	c.backendPool = pool
	c.backendAddr = addr
	c.backendName = name

	return nil
}

// resetBackend clears the backend connection state after an I/O error
func (c *clientConn) resetBackend() {
	if c.backend != nil {
		c.backend.Close()
		c.backend = nil
	}
	c.backendAddr = ""
	c.backendName = ""
}

func (c *clientConn) handleLocalInfile(initialResponse []byte, moreResults bool) error {
	// 1. Forward 0xFB response to client
	if err := c.forwardBackendResponse(initialResponse, moreResults); err != nil {
		return err
	}

	// 2. Read packets from client and forward to backend
	for {
		packet, err := c.readPacket() // Read from client
		if err != nil {
			return err
		}

		// Forward to backend
		if err := c.writeBackendPacket(packet); err != nil {
			return err
		}

		// Empty packet means EOF from client
		if len(packet) == 0 {
			break
		}
	}

	// 3. Read final OK/ERR from backend
	finalResponse, err := c.execBackendResponse()
	if err != nil {
		return err
	}

	return c.forwardBackendResponse(finalResponse, moreResults)
}

func (c *clientConn) writePacket(payload []byte) error {
	c.sequence++
	packet := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(payload)))
	packet[3] = c.sequence
	copy(packet[4:], payload)
	_, err := c.conn.Write(packet)
	return err
}

func (c *clientConn) dialAndAuth(addr string) (net.Conn, error) {
	network := "tcp"
	dialAddr := addr
	if len(addr) > 5 && addr[:5] == "unix:" {
		network = "unix"
		dialAddr = addr[5:]
	}

	wasAuthenticated := (c.user != "")
	cfg := mysql.NewConfig()
	cfg.User = c.user
	cfg.Net = network
	cfg.Addr = dialAddr
	cfg.DBName = c.db

	// Crucial: define the HandleAuth callback to forward the nonce to the client
	cfg.HandleAuth = func(backendCfg *mysql.Config, plugin string, salt []byte, serverCapabilities uint32) ([]byte, error) {
		if c.user == "" {
			// This is the INITIAL handshake.
			// We need to send the Server Greeting to the client with this salt.
			c.salt = salt
			c.capability = mysql.CapabilityFlag(serverCapabilities & ^uint32(mysql.ClientSSL|mysql.ClientDeprecateEOF)) // Strip SSL and DeprecateEOF from backend capabilities
			if err := c.writeServerGreeting(); err != nil {
				return nil, err
			}
			// Then read the client's auth response.
			if err := c.readClientAuth(); err != nil {
				return nil, err
			}
			// IMPORTANT: Update the backend config with the username we just got from the client
			backendCfg.User = c.user
			// The driver expects the auth response body.
			return c.auth, nil
		} else {
			// This is a SHARD SWITCH.
			// The client is already connected and authenticated once.
			// We need to send an Auth Switch Request (0xFE) to the client.
			// https://mariadb.com/kb/en/connection/#auth-switch-request
			payload := make([]byte, 1+len(plugin)+1+len(salt))
			payload[0] = 0xFE
			copy(payload[1:], plugin)
			payload[1+len(plugin)] = 0
			copy(payload[1+len(plugin)+1:], salt)

			// Send to client
			if err := c.writePacket(payload); err != nil {
				return nil, err
			}

			// Read response from client
			resp, err := c.readPacket()
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
	}

	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}

	// connect using the driver
	dbConn, err := connector.Connect(context.Background())
	if err != nil {
		return nil, err
	}

	// After successful Connect, we have the auth OK from backend.
	// But we still need to send the OK packet to the client if this was a shard switch
	// or if the driver didn't send it yet.
	// Actually, for the initial handshake, the driver handles the handshake response.
	// If it's a switch, we might need to send OK to the client.

	// If this was the initial handshake (c.user was empty before dialAndAuth),
	// we need to send the final OK packet to the client to complete the handshake.
	// For shard switches, the client is waiting for the result of the command that triggered the switch.
	if !wasAuthenticated {
		if err := c.writeOK(); err != nil {
			dbConn.Close()
			return nil, err
		}
	}

	// Get the raw connection from the driver
	raw := mysql.GetRawConn(dbConn)
	if raw == nil {
		dbConn.Close()
		return nil, fmt.Errorf("failed to get raw connection from driver")
	}

	return raw, nil
}

func (c *clientConn) dispatch(cmd byte, data []byte) error {
	switch cmd {
	case mysql.ComQuit:
		return io.EOF
	case mysql.ComInitDB:
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
	case mysql.ComFieldList:
		// COM_FIELD_LIST is deprecated and used for table completion
		// Just return EOF to indicate no fields (client will fall back to other methods)
		return c.writeEOF()
	case mysql.ComPing:
		return c.writeOK()
	case mysql.ComQuery:
		return c.handleQuery(string(data))
	case mysql.ComStmtPrepare:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.handlePrepare(string(data))
	case mysql.ComStmtExecute:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.handleExecute(data)
	case mysql.ComStmtClose:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.handleStmtClose(data)
	case mysql.ComStmtReset:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.handleStmtReset(data)
	default:
		return fmt.Errorf("command %d not supported", cmd)
	}
}

func (c *clientConn) handleBegin(moreResults bool) error {
	_, err := c.execBackendQuery("BEGIN")
	if err != nil {
		return err
	}
	c.status |= mysql.StatusInTrans
	c.inTransaction = true
	return c.writeOKWithInfo("", moreResults)
}

func (c *clientConn) handleCommit(moreResults bool) error {
	_, err := c.execBackendQuery("COMMIT")
	if err != nil {
		return err
	}
	c.status &= ^mysql.StatusInTrans
	c.inTransaction = false
	return c.writeOKWithInfo("", moreResults)
}

func (c *clientConn) handleRollback(moreResults bool) error {
	_, err := c.execBackendQuery("ROLLBACK")
	if err != nil {
		return err
	}
	c.status &= ^mysql.StatusInTrans
	c.inTransaction = false
	return c.writeOKWithInfo("", moreResults)
}

func (c *clientConn) handleQuery(query string) error {
	start := time.Now()
	log.Printf("[MariaDB] handleQuery received: %q", query)
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

	// Check for FQN-based sharding
	if parsed.DB != "" && parsed.DB != c.db {
		if err := c.ensureBackend(parsed.DB); err != nil {
			return err
		}
		c.db = parsed.DB
	}

	// Check for USE statement
	if strings.HasPrefix(queryUpper, "USE ") {
		parts := strings.Fields(parsed.Query)
		if len(parts) >= 2 {
			dbName := strings.Trim(parts[1], "`;")
			if err := c.ensureBackend(dbName); err != nil {
				return err
			}
			c.db = dbName
			// Execute USE on backend
			_, err := c.execBackendQuery(fmt.Sprintf("USE `%s`", dbName))
			if err != nil {
				return err
			}
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

	// Route batchable writes to write batch manager (only outside transactions)
	if c.proxy.writeBatch != nil && !c.inTransaction && parsed.IsWritable() && parsed.IsBatchable() {
		return c.handleBatchedWrite(parsed.Query, parsed.BatchMs, start, file, lineStr, queryType, moreResults)
	}

	// Check cache with thundering herd protection
	if parsed.IsCacheable() {
		cached, flags, ok := c.proxy.cache.Get(parsed.Query)
		if ok {
			if flags == cache.FlagFresh {
				// Fresh cache hit - serve immediately
				metrics.CacheHits.WithLabelValues(file, lineStr).Inc()
				metrics.QueryTotal.WithLabelValues(file, lineStr, queryType, "true").Inc()
				metrics.QueryLatency.WithLabelValues(file, lineStr, queryType).Observe(time.Since(start).Seconds())
				c.lastQueryBackend = "cache"
				c.lastQueryCacheHit = true
				return c.forwardBackendResponse(cached, moreResults)
			}

			if flags == cache.FlagStale {
				// Stale but another request is already refreshing - serve stale
				metrics.CacheHits.WithLabelValues(file, lineStr).Inc()
				metrics.QueryTotal.WithLabelValues(file, lineStr, queryType, "true").Inc()
				metrics.QueryLatency.WithLabelValues(file, lineStr, queryType).Observe(time.Since(start).Seconds())
				c.lastQueryBackend = "cache (stale)"
				c.lastQueryCacheHit = true
				return c.forwardBackendResponse(cached, moreResults)
			}

			// FlagRefresh: First stale access - this request does the refresh (sync)
			// Fall through to query backend below
		}
		metrics.CacheMisses.WithLabelValues(file, lineStr).Inc()

		// Cold cache or stale refresh: use single-flight pattern
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

	if parsed.IsCacheable() && (c.status&mysql.StatusInTrans == 0) {
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

	response, err := c.execBackendQuery(parsed.Query)
	if err != nil {
		// Cancel inflight if we were the first request
		if parsed.IsCacheable() {
			c.proxy.cache.CancelInflight(parsed.Query)
		}
		return err
	}

	// Check for LOAD DATA LOCAL INFILE response (0xFB)
	if len(response) >= 5 && response[4] == 0xFB {
		return c.handleLocalInfile(response, moreResults)
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
	// 1. Forward COM_STMT_PREPARE to backend
	payload := make([]byte, 1+len(query))
	payload[0] = mysql.ComStmtPrepare
	copy(payload[1:], query)

	c.backendSeq = 255
	if err := c.writeBackendPacket(payload); err != nil {
		return err
	}

	// 2. Read STMT_PREPARE_OK response
	response, err := c.readBackendPacket()
	if err != nil {
		return err
	}

	// Read and forward the full response chain
	var fullResponse []byte
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(response)))
	header[3] = c.backendSeq
	fullResponse = append(fullResponse, header...)
	fullResponse = append(fullResponse, response...)

	if len(response) > 0 && response[0] == 0x00 { // OK
		stmtID := binary.LittleEndian.Uint32(response[1:5])
		numCols := binary.LittleEndian.Uint16(response[5:7])
		numParams := binary.LittleEndian.Uint16(response[7:9])

		// Store parsed query with batch hints
		c.preparedStatements[stmtID] = parser.Parse(query)

		// Read parameters if any
		if numParams > 0 {
			for i := uint16(0); i < numParams; i++ {
				p, err := c.readBackendPacket()
				if err != nil {
					return err
				}
				binary.LittleEndian.PutUint32(header[0:4], uint32(len(p)))
				header[3] = c.backendSeq
				fullResponse = append(fullResponse, header...)
				fullResponse = append(fullResponse, p...)
			}
			// EOF after parameters
			eof, err := c.readBackendPacket()
			if err != nil {
				return err
			}
			binary.LittleEndian.PutUint32(header[0:4], uint32(len(eof)))
			header[3] = c.backendSeq
			fullResponse = append(fullResponse, header...)
			fullResponse = append(fullResponse, eof...)
		}

		// Read columns if any
		if numCols > 0 {
			for i := uint16(0); i < numCols; i++ {
				p, err := c.readBackendPacket()
				if err != nil {
					return err
				}
				binary.LittleEndian.PutUint32(header[0:4], uint32(len(p)))
				header[3] = c.backendSeq
				fullResponse = append(fullResponse, header...)
				fullResponse = append(fullResponse, p...)
			}
			// EOF after columns
			eof, err := c.readBackendPacket()
			if err != nil {
				return err
			}
			binary.LittleEndian.PutUint32(header[0:4], uint32(len(eof)))
			header[3] = c.backendSeq
			fullResponse = append(fullResponse, header...)
			fullResponse = append(fullResponse, eof...)
		}
	}

	return c.forwardBackendResponse(fullResponse, false)
}

func (c *clientConn) execQueryOnConn(conn net.Conn, query string) ([]byte, error) {
	// Build COM_QUERY packet
	payload := make([]byte, 1+len(query))
	payload[0] = mysql.ComQuery
	copy(payload[1:], query)

	// Send packet
	seq := byte(0)
	packet := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(payload)))
	packet[3] = seq
	copy(packet[4:], payload)
	conn.SetWriteDeadline(time.Now().Add(backendTimeout))
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	conn.SetWriteDeadline(time.Time{})

	// Read response using a temporary sequence tracker
	var response []byte
	eofCount := 0
	for {
		header := make([]byte, 4)
		conn.SetReadDeadline(time.Now().Add(backendTimeout))
		if _, err := io.ReadFull(conn, header); err != nil {
			return nil, err
		}
		length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)

		packetPayload := make([]byte, length)
		if _, err := io.ReadFull(conn, packetPayload); err != nil {
			return nil, err
		}
		conn.SetReadDeadline(time.Time{})

		// Build result
		response = append(response, header...)
		response = append(response, packetPayload...)

		if len(packetPayload) > 0 {
			switch packetPayload[0] {
			case 0x00, 0xFE, 0xFF:
				// Simplified end detection for background refresh (usually SELECTs)
				if packetPayload[0] == 0xFF {
					return response, nil
				}
				if packetPayload[0] == 0x00 || (packetPayload[0] == 0xFE && len(packetPayload) < 9) {
					// Check for more results or EOF count
					if packetPayload[0] == 0xFE {
						eofCount++
						if eofCount >= 2 {
							return response, nil
						}
					} else {
						return response, nil
					}
				}
			}
		}
	}
}

func (c *clientConn) handleExecute(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("malformed COM_STMT_EXECUTE packet")
	}
	stmtID := binary.LittleEndian.Uint32(data[0:4])
	parsed, ok := c.preparedStatements[stmtID]
	if !ok {
		return fmt.Errorf("unknown statement ID %d", stmtID)
	}

	// Check if this prepared statement should be batched
	// Only batch writes outside of transactions
	if c.proxy.writeBatch != nil && !c.inTransaction && parsed.IsWritable() && parsed.IsBatchable() {
		return c.handleBatchedPreparedExecute(stmtID, data, parsed)
	}

	var cacheKey string
	if parsed.IsCacheable() {
		// Form a cache key from query, parameters and current database
		// We use the stripped query to be consistent with COM_QUERY caching.
		// We hash the parameters and flags (data[4:]) but NOT the stmtID (data[0:4])
		// to ensure that cached results can be shared across different sessions.
		h := sha1.New()
		h.Write([]byte(c.db))
		h.Write([]byte(parsed.Query))
		h.Write(data[4:])
		cacheKey = "ps:" + hex.EncodeToString(h.Sum(nil))

		// Check cache
		cached, _, ok := c.proxy.cache.Get(cacheKey)
		if ok {
			c.lastQueryBackend = "cache"
			c.lastQueryCacheHit = true
			return c.forwardBackendResponse(cached, false)
		}
	}

	// Forward COM_STMT_EXECUTE to backend
	payload := make([]byte, 1+len(data))
	payload[0] = mysql.ComStmtExecute
	copy(payload[1:], data)

	c.backendSeq = 255
	if err := c.writeBackendPacket(payload); err != nil {
		return err
	}

	response, err := c.execBackendResponse()
	if err != nil {
		return err
	}

	// Check for LOAD DATA LOCAL INFILE response (0xFB)
	if len(response) >= 5 && response[4] == 0xFB {
		return c.handleLocalInfile(response, false)
	}

	// Don't cache error responses
	isError := len(response) >= 5 && response[4] == 0xFF
	if cacheKey != "" && !isError {
		c.proxy.cache.Set(cacheKey, response, time.Duration(parsed.TTL)*time.Second)
	}

	c.lastQueryBackend = c.backendName
	c.lastQueryCacheHit = false
	return c.forwardBackendResponse(response, false)
}

func (c *clientConn) handleStmtClose(data []byte) error {
	if len(data) >= 4 {
		stmtID := binary.LittleEndian.Uint32(data[0:4])
		delete(c.preparedStatements, stmtID)
	}

	payload := make([]byte, 1+len(data))
	payload[0] = mysql.ComStmtClose
	copy(payload[1:], data)

	c.backendSeq = 255
	return c.writeBackendPacket(payload)
}

func (c *clientConn) handleStmtReset(data []byte) error {
	payload := make([]byte, 1+len(data))
	payload[0] = mysql.ComStmtReset
	copy(payload[1:], data)

	c.backendSeq = 255
	if err := c.writeBackendPacket(payload); err != nil {
		return err
	}

	response, err := c.readBackendPacket()
	if err != nil {
		return err
	}
	return c.forwardBackendResponse(response, false)
}

func (c *clientConn) execBackendResponse() ([]byte, error) {
	// This is similar to execBackendQuery but without sending the command
	var response []byte
	eofCount := 0
	packetCount := 0
	for {
		packet, err := c.readBackendPacket()
		if err != nil {
			return nil, err
		}
		packetCount++

		header := make([]byte, 4)
		binary.LittleEndian.PutUint32(header[0:4], uint32(len(packet)))
		header[3] = c.backendSeq
		response = append(response, header...)
		response = append(response, packet...)

		if len(packet) > 0 {
			headerByte := packet[0]
			switch headerByte {
			case 0x00: // OK or Binary Row
				if eofCount == 1 {
					// After first EOF, 0x00 is a Binary Data Row, not an OK packet.
					// Keep reading until we see the final EOF/OK.
					continue
				}
				// Otherwise, treat as OK packet
				statusOffset := 1
				_, _, n1 := mysql.ReadLengthEncodedInteger(packet[statusOffset:])
				statusOffset += n1
				_, _, n2 := mysql.ReadLengthEncodedInteger(packet[statusOffset:])
				statusOffset += n2
				if len(packet) >= statusOffset+2 {
					status := binary.LittleEndian.Uint16(packet[statusOffset:])
					if status&uint16(mysql.StatusMoreResultsExists) == 0 {
						return response, nil
					}
					eofCount = 0
					continue
				}
				return response, nil
			case 0xFF: // Error
				return response, nil
			case 0xFB: // Local Infile Request
				if packetCount == 1 {
					return response, nil // Immediately return on 0xFB as first packet
				}
				// Otherwise it's a NULL value in a Row Data packet, continue reading
			case 0xFE: // EOF
				if len(packet) < 9 {
					eofCount++
					if eofCount >= 2 {
						// Checked for more results in EOF as well
						if len(packet) >= 5 {
							status := binary.LittleEndian.Uint16(packet[3:])
							if status&uint16(mysql.StatusMoreResultsExists) == 0 {
								return response, nil
							}
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

func (c *clientConn) buildResultSet(rows *sql.Rows) ([]byte, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var result []byte

	// Column count packet
	colCount := len(columns)
	packet := make([]byte, 4)
	packet = append(packet, mysql.AppendLengthEncodedInteger(nil, uint64(colCount))...)
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
		packet = append(packet, mysql.AppendLengthEncodedInteger(nil, uint64(len(col)))...)
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
	eofPacket := mysql.WriteEOFPacket(c.status, c.capability)
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
				packet = append(packet, mysql.AppendLengthEncodedInteger(nil, uint64(len(str)))...)
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
	eofPacket = mysql.WriteEOFPacket(c.status, c.capability)
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
	c.backend.SetReadDeadline(time.Now().Add(backendTimeout))
	if _, err := io.ReadFull(c.backend, header); err != nil {
		c.backend.SetReadDeadline(time.Time{})
		c.resetBackend()
		return nil, err
	}

	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	c.backendSeq = header[3]

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.backend, payload); err != nil {
		c.backend.SetReadDeadline(time.Time{})
		c.resetBackend()
		return nil, err
	}

	c.backend.SetReadDeadline(time.Time{})
	return payload, nil
}

func (c *clientConn) writeBackendPacket(payload []byte) error {
	c.backendSeq++
	packet := make([]byte, 4+len(payload))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(payload)))
	packet[3] = c.backendSeq
	copy(packet[4:], payload)

	c.backend.SetWriteDeadline(time.Now().Add(backendTimeout))
	_, err := c.backend.Write(packet)
	c.backend.SetWriteDeadline(time.Time{})
	if err != nil {
		c.resetBackend()
	}
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
	payload[0] = mysql.ComQuery
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
					_, _, n1 := mysql.ReadLengthEncodedInteger(packet[statusOffset:])
					statusOffset += n1
					_, _, n2 := mysql.ReadLengthEncodedInteger(packet[statusOffset:])
					statusOffset += n2
					if len(packet) >= statusOffset+2 {
						status := binary.LittleEndian.Uint16(packet[statusOffset:])
						if status&uint16(mysql.StatusMoreResultsExists) == 0 {
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
							if status&uint16(mysql.StatusMoreResultsExists) == 0 {
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

	// If more results follow, set the StatusMoreResultsExists flag in the last packet
	if moreResults && lastPacketPos < len(respCopy) {
		packet := respCopy[lastPacketPos:]
		if len(packet) > 4 {
			header := packet[4]
			if header == 0x00 { // OK_HEADER
				// OK packet: 00 <lenenc_rows> <lenenc_id> <status_flags(2)>
				statusOffset := 5
				_, _, n1 := mysql.ReadLengthEncodedInteger(packet[statusOffset:])
				statusOffset += n1
				_, _, n2 := mysql.ReadLengthEncodedInteger(packet[statusOffset:])
				statusOffset += n2
				if len(packet) >= statusOffset+2 {
					status := binary.LittleEndian.Uint16(packet[statusOffset:])
					status |= uint16(mysql.StatusMoreResultsExists)
					binary.LittleEndian.PutUint16(packet[statusOffset:], status)
				}
			} else if header == 0xfe { // EOF_HEADER
				// EOF packet: FE <warnings(2)> <status_flags(2)>
				if len(packet) >= 9 {
					status := binary.LittleEndian.Uint16(packet[7:9])
					status |= uint16(mysql.StatusMoreResultsExists)
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
		status |= mysql.StatusMoreResultsExists
	}
	packet := mysql.WriteOKPacket(0, 0, status, c.capability)
	// Add header
	payload := make([]byte, 4+len(packet))
	binary.LittleEndian.PutUint32(payload[0:4], uint32(len(packet)))
	payload[3] = c.sequence
	copy(payload[4:], packet)
	_, err := c.conn.Write(payload)
	return err
}

func (c *clientConn) writeError(e error) error {
	c.sequence++
	packet := mysql.WriteErrorPacket(1105, "HY000", e.Error(), c.capability)
	// Add header
	payload := make([]byte, 4+len(packet))
	binary.LittleEndian.PutUint32(payload[0:4], uint32(len(packet)))
	payload[3] = c.sequence
	copy(payload[4:], packet)
	_, err := c.conn.Write(payload)
	return err
}

func (c *clientConn) writeAuthError(user string) error {
	c.sequence++
	msg := fmt.Sprintf("Access denied for user '%s'@'localhost' (using password: YES)", user)
	packet := mysql.WriteErrorPacket(1045, "28000", msg, c.capability)
	// Add header
	payload := make([]byte, 4+len(packet))
	binary.LittleEndian.PutUint32(payload[0:4], uint32(len(packet)))
	payload[3] = c.sequence
	copy(payload[4:], packet)
	_, err := c.conn.Write(payload)
	return err
}

func (c *clientConn) writeEOF() error {
	c.sequence++
	packet := mysql.WriteEOFPacket(c.status, c.capability)
	// Add header
	payload := make([]byte, 4+len(packet))
	binary.LittleEndian.PutUint32(payload[0:4], uint32(len(packet)))
	payload[3] = c.sequence
	copy(payload[4:], packet)
	_, err := c.conn.Write(payload)
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
	query := fmt.Sprintf("SELECT 'Backend' AS `Variable_name`, '%s' AS `Value` UNION ALL SELECT 'Shard', '%s'", backend, shard)

	// Add batch size if available (from last write batch operation)
	if c.lastBatchSize > 0 {
		query = fmt.Sprintf("%s UNION ALL SELECT 'LastBatchSize', '%d'", query, c.lastBatchSize)
	}

	response, err := c.execBackendQuery(query)
	if err != nil {
		return err
	}

	return c.forwardBackendResponse(response, moreResults)
}

func (c *clientConn) handleBatchedWrite(query string, batchMs int, start time.Time, file, lineStr, queryType string, moreResults bool) error {
	// Parse the query to get the batch key
	parsed := parser.Parse(query)
	batchKey := parsed.GetBatchKey()

	// Enqueue the write (blocks until result is available)
	ctx := context.Background()
	result := c.proxy.writeBatch.Enqueue(ctx, batchKey, query, nil, batchMs, func(batchSize int) {
		// Update this connection's batch size when batch completes
		c.mu.Lock()
		c.lastBatchSize = batchSize
		c.mu.Unlock()
	})

	// Record metrics
	metrics.QueryTotal.WithLabelValues(file, lineStr, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, lineStr, queryType).Observe(time.Since(start).Seconds())

	// Handle error
	if result.Error != nil {
		// Fall back to immediate execution on certain errors
		if result.Error == writebatch.ErrManagerClosed || result.Error == writebatch.ErrTimeout {
			log.Printf("[MariaDB] Write batch error (%v), executing immediately", result.Error)
			return c.executeImmediateWrite(query, start, file, lineStr, queryType, moreResults)
		}
		return c.writeError(result.Error)
	}

	// Track metadata
	c.lastQueryBackend = "write-batch"
	c.lastQueryCacheHit = false
	c.lastBatchSize = result.BatchSize

	// Send OK packet with affected rows and last insert ID
	return c.writeOKWithRowsAndID(result.AffectedRows, result.LastInsertID, moreResults)
}

func (c *clientConn) handleBatchedPreparedExecute(stmtID uint32, data []byte, parsed *parser.ParsedQuery) error {
	start := time.Now()
	batchKey := parsed.GetBatchKey()
	batchMs := parsed.BatchMs
	file := parsed.File
	lineStr := strconv.Itoa(parsed.Line)
	queryType := queryTypeLabel(parsed.Type)

	// Enqueue the prepared statement execution
	ctx := context.Background()
	result := c.proxy.writeBatch.Enqueue(ctx, batchKey, parsed.Query, nil, batchMs, func(batchSize int) {
		c.mu.Lock()
		c.lastBatchSize = batchSize
		c.mu.Unlock()
	})

	// Record metrics
	metrics.QueryTotal.WithLabelValues(file, lineStr, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, lineStr, queryType).Observe(time.Since(start).Seconds())

	// Handle error
	if result.Error != nil {
		// Fall back to executing the prepared statement directly
		if result.Error == writebatch.ErrManagerClosed || result.Error == writebatch.ErrTimeout {
			log.Printf("[MariaDB] Write batch error (%v), executing prepared statement directly", result.Error)
			// Fall back to normal prepared statement execution
			payload := make([]byte, 1+len(data))
			payload[0] = mysql.ComStmtExecute
			copy(payload[1:], data)

			c.backendSeq = 255
			if err := c.writeBackendPacket(payload); err != nil {
				return err
			}

			response, err := c.execBackendResponse()
			if err != nil {
				return err
			}

			c.lastQueryBackend = c.backendName
			c.lastQueryCacheHit = false
			return c.forwardBackendResponse(response, false)
		}
		return c.writeError(result.Error)
	}

	// Track metadata
	c.lastQueryBackend = "write-batch"
	c.lastQueryCacheHit = false
	c.lastBatchSize = result.BatchSize

	// Send OK packet with affected rows and last insert ID
	return c.writeOKWithRowsAndID(result.AffectedRows, result.LastInsertID, false)
}

func (c *clientConn) executeImmediateWrite(query string, start time.Time, file, lineStr, queryType string, moreResults bool) error {
	// Ensure we have a backend connection
	backendAddr := c.backendPool.GetPrimary()
	backendName := "primary"

	if err := c.ensureBackendConn(backendAddr, backendName, c.backendPool); err != nil {
		return err
	}

	response, err := c.execBackendQuery(query)
	if err != nil {
		return err
	}

	metrics.DatabaseQueries.WithLabelValues(backendName).Inc()
	metrics.QueryTotal.WithLabelValues(file, lineStr, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, lineStr, queryType).Observe(time.Since(start).Seconds())

	c.lastQueryBackend = backendName
	c.lastQueryCacheHit = false

	return c.forwardBackendResponse(response, moreResults)
}

func (c *clientConn) writeOKWithRowsAndID(affectedRows, lastInsertID int64, moreResults bool) error {
	c.sequence++
	status := c.status
	if moreResults {
		status |= mysql.StatusMoreResultsExists
	}
	packet := mysql.WriteOKPacket(uint64(affectedRows), uint64(lastInsertID), status, c.capability)
	// Add header
	payload := make([]byte, 4+len(packet))
	binary.LittleEndian.PutUint32(payload[0:4], uint32(len(packet)))
	payload[3] = c.sequence
	copy(payload[4:], packet)
	_, err := c.conn.Write(payload)
	return err
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
