package postgres

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
	"sync/atomic"
	"time"

	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/metrics"
	"github.com/mevdschee/tqdbproxy/parser"
	"github.com/mevdschee/tqdbproxy/replica"

	_ "github.com/lib/pq"
)

const (
	msgQuery           = 'Q'
	msgParse           = 'P'
	msgBind            = 'B'
	msgExecute         = 'E'
	msgDescribe        = 'D'
	msgSync            = 'S'
	msgTerminate       = 'X'
	msgReadyForQuery   = 'Z'
	msgCommandComplete = 'C'
	msgRowDescription  = 'T'
	msgDataRow         = 'D'
	msgErrorResponse   = 'E'
	msgAuthentication  = 'R'
	msgParameterStatus = 'S'
	msgBackendKeyData  = 'K'
)

var connCounter uint32

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

// Proxy handles PostgreSQL protocol connections with caching
type Proxy struct {
	listen      string
	socket      string // Optional Unix socket path
	replicaPool *replica.Pool
	cache       *cache.Cache
}

// connState tracks per-connection state for TQDB status
type connState struct {
	lastBackend  string
	lastCacheHit bool
}

// New creates a new PostgreSQL proxy
func New(listen, socket string, pool *replica.Pool, c *cache.Cache) *Proxy {
	return &Proxy{
		listen:      listen,
		socket:      socket,
		replicaPool: pool,
		cache:       c,
	}
}

// Start begins accepting PostgreSQL connections
func (p *Proxy) Start() error {
	// Start TCP listener
	tcpListener, err := net.Listen("tcp", p.listen)
	if err != nil {
		return err
	}
	log.Printf("[PostgreSQL] Listening on %s (tcp), forwarding to %s", p.listen, p.replicaPool.GetPrimary())

	go p.acceptLoop(tcpListener)

	// Start Unix socket listener if configured
	if p.socket != "" {
		// Remove existing socket file if present
		if err := os.Remove(p.socket); err != nil && !os.IsNotExist(err) {
			log.Printf("[PostgreSQL] Warning: could not remove existing socket: %v", err)
		}
		unixListener, err := net.Listen("unix", p.socket)
		if err != nil {
			return fmt.Errorf("failed to listen on unix socket: %v", err)
		}
		log.Printf("[PostgreSQL] Listening on %s (unix)", p.socket)
		go p.acceptLoop(unixListener)
	}

	return nil
}

func (p *Proxy) acceptLoop(listener net.Listener) {
	for {
		client, err := listener.Accept()
		if err != nil {
			log.Printf("[PostgreSQL] Accept error: %v", err)
			continue
		}
		connID := atomic.AddUint32(&connCounter, 1)
		go p.handleConnection(client, connID)
	}
}

func (p *Proxy) handleConnection(client net.Conn, connID uint32) {
	defer client.Close()

	// Read startup message from client
	startupMsg, err := p.readStartupMessage(client)
	if err != nil {
		log.Printf("[PostgreSQL] Startup read error (conn %d): %v", connID, err)
		return
	}

	// Check for SSL request
	if len(startupMsg) == 8 {
		code := binary.BigEndian.Uint32(startupMsg[4:8])
		if code == 80877103 { // SSLRequest
			// Deny SSL
			if _, err := client.Write([]byte{'N'}); err != nil {
				return
			}
			// Read actual startup message
			startupMsg, err = p.readStartupMessage(client)
			if err != nil {
				return
			}
		}
	}

	// Parse startup message to get user and database
	params := p.parseStartupParams(startupMsg)
	user := params["user"]
	database := params["database"]
	if database == "" {
		database = user
	}

	// Request cleartext password from client (AuthenticationCleartextPassword)
	p.writeMessage(client, msgAuthentication, []byte{0, 0, 0, 3})

	// Read password message from client
	msgType, payload, err := p.readMessage(client)
	if err != nil {
		log.Printf("[PostgreSQL] Password read error (conn %d): %v", connID, err)
		return
	}
	if msgType != 'p' {
		log.Printf("[PostgreSQL] Expected password message, got %c (conn %d)", msgType, connID)
		p.sendError(client, "08P01", "expected password message")
		return
	}

	// Password is null-terminated
	password := string(payload)
	if len(password) > 0 && password[len(password)-1] == 0 {
		password = password[:len(password)-1]
	}

	// Connect to backend using the client's credentials
	dsn := fmt.Sprintf("host=%s port=5432 user=%s password=%s dbname=%s sslmode=disable",
		"127.0.0.1", user, password, database)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("[PostgreSQL] Backend connection error (conn %d): %v", connID, err)
		p.sendError(client, "08006", fmt.Sprintf("cannot connect to backend: %v", err))
		return
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Printf("[PostgreSQL] Backend ping error (conn %d): %v", connID, err)
		// Strip "pq: " prefix from error message to match native PostgreSQL
		errMsg := err.Error()
		if strings.HasPrefix(errMsg, "pq: ") {
			errMsg = errMsg[4:]
		}
		p.sendFatalError(client, "28P01", errMsg)
		return
	}

	// Send AuthenticationOk
	p.writeMessage(client, msgAuthentication, []byte{0, 0, 0, 0})

	// Send some parameter statuses
	p.sendParameterStatus(client, "server_version", "16.0")
	p.sendParameterStatus(client, "client_encoding", "UTF8")
	p.sendParameterStatus(client, "DateStyle", "ISO, MDY")
	p.sendParameterStatus(client, "TimeZone", "UTC")

	// Send BackendKeyData (fake)
	keyData := make([]byte, 8)
	binary.BigEndian.PutUint32(keyData[0:4], connID)
	binary.BigEndian.PutUint32(keyData[4:8], 12345)
	p.writeMessage(client, msgBackendKeyData, keyData)

	// Send ReadyForQuery
	p.writeMessage(client, msgReadyForQuery, []byte{'I'})

	// Handle commands
	p.handleMessages(client, db, connID)
}

func (p *Proxy) parseStartupParams(msg []byte) map[string]string {
	params := make(map[string]string)
	if len(msg) < 8 {
		return params
	}
	// Skip length (4 bytes) and protocol version (4 bytes)
	data := msg[4:]
	if len(data) < 4 {
		return params
	}
	data = data[4:] // Skip protocol version

	// Parse null-terminated key-value pairs
	for len(data) > 0 {
		// Find key
		keyEnd := bytes.IndexByte(data, 0)
		if keyEnd <= 0 {
			break
		}
		key := string(data[:keyEnd])
		data = data[keyEnd+1:]

		// Find value
		valEnd := bytes.IndexByte(data, 0)
		if valEnd < 0 {
			break
		}
		value := string(data[:valEnd])
		data = data[valEnd+1:]

		params[key] = value
	}
	return params
}

func (p *Proxy) sendParameterStatus(client net.Conn, name, value string) {
	payload := append([]byte(name), 0)
	payload = append(payload, []byte(value)...)
	payload = append(payload, 0)
	p.writeMessage(client, msgParameterStatus, payload)
}

func (p *Proxy) sendError(client net.Conn, code, message string) {
	p.sendErrorWithSeverity(client, "ERROR", code, message)
}

func (p *Proxy) sendFatalError(client net.Conn, code, message string) {
	p.sendErrorWithSeverity(client, "FATAL", code, message)
}

func (p *Proxy) sendErrorWithSeverity(client net.Conn, severity, code, message string) {
	var payload bytes.Buffer
	payload.WriteByte('S') // Severity
	payload.WriteString(severity)
	payload.WriteByte(0)
	payload.WriteByte('C') // Code
	payload.WriteString(code)
	payload.WriteByte(0)
	payload.WriteByte('M') // Message
	payload.WriteString(message)
	payload.WriteByte(0)
	payload.WriteByte(0) // Terminator
	p.writeMessage(client, msgErrorResponse, payload.Bytes())
}

func (p *Proxy) handleMessages(client net.Conn, db *sql.DB, connID uint32) {
	state := &connState{}
	for {
		msgType, payload, err := p.readMessage(client)
		if err != nil {
			if err != io.EOF {
				log.Printf("[PostgreSQL] Read error (conn %d): %v", connID, err)
			}
			return
		}

		switch msgType {
		case msgQuery:
			p.handleQuery(payload, client, db, state)
		case msgTerminate:
			return
		default:
			// For unhandled messages, send ReadyForQuery
			p.writeMessage(client, msgReadyForQuery, []byte{'I'})
		}
	}
}

func (p *Proxy) handleQuery(payload []byte, client net.Conn, db *sql.DB, state *connState) {
	start := time.Now()

	// Query is null-terminated string
	queryBytes := payload
	if len(queryBytes) > 0 && queryBytes[len(queryBytes)-1] == 0 {
		queryBytes = queryBytes[:len(queryBytes)-1]
	}
	query := string(queryBytes)

	// Check for TQDB status query (PostgreSQL style: pg_tqdb_status)
	queryUpper := strings.ToUpper(strings.TrimSpace(query))
	if queryUpper == "SELECT * FROM PG_TQDB_STATUS" {
		p.handleShowTQDBStatus(client, state)
		return
	}

	parsed := parser.Parse(query)

	file := parsed.File
	if file == "" {
		file = "unknown"
	}
	line := "0"
	if parsed.Line > 0 {
		line = strconv.Itoa(parsed.Line)
	}
	queryType := queryTypeLabel(parsed.Type)

	// Check cache with thundering herd protection
	if parsed.IsCacheable() {
		cached, flags, ok := p.cache.Get(parsed.Query)
		if ok {
			// Handle stale data with background refresh
			if flags == cache.FlagRefresh {
				// First stale access - trigger background refresh
				go p.refreshQueryInBackground(parsed.Query, db)
			}
			// Serve from cache (fresh or stale)
			metrics.CacheHits.WithLabelValues(file, line).Inc()
			metrics.QueryTotal.WithLabelValues(file, line, queryType, "true").Inc()
			metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())

			state.lastBackend = "cache"
			state.lastCacheHit = true

			if _, err := client.Write(cached); err != nil {
				log.Printf("[PostgreSQL] Cache response error: %v", err)
			}
			return
		}
		metrics.CacheMisses.WithLabelValues(file, line).Inc()

		// Cold cache: use single-flight pattern
		cached, _, ok, waited := p.cache.GetOrWait(parsed.Query)
		if waited && ok {
			// Another goroutine fetched it for us
			metrics.CacheHits.WithLabelValues(file, line).Inc()
			state.lastBackend = "cache"
			state.lastCacheHit = true
			if _, err := client.Write(cached); err != nil {
				log.Printf("[PostgreSQL] Cache response error: %v", err)
			}
			return
		}
		// We need to fetch from DB (either first request or waited but still miss)
	}

	// Execute query
	var response bytes.Buffer

	rows, err := db.Query(parsed.Query)
	if err != nil {
		// Cancel inflight if we were the first request
		if parsed.IsCacheable() {
			p.cache.CancelInflight(parsed.Query)
		}
		// Send error response
		p.sendError(client, "42000", err.Error())
		p.writeMessage(client, msgReadyForQuery, []byte{'I'})
		return
	}
	defer rows.Close()

	// Get column info
	cols, _ := rows.Columns()
	if len(cols) > 0 {
		// Send RowDescription
		rowDesc := p.buildRowDescription(cols)
		response.Write(rowDesc)

		// Send data rows
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		rowCount := 0
		for rows.Next() {
			rows.Scan(valuePtrs...)
			dataRow := p.buildDataRow(values)
			response.Write(dataRow)
			rowCount++
		}

		// Send CommandComplete
		cmdComplete := fmt.Sprintf("SELECT %d", rowCount)
		cmdPayload := append([]byte(cmdComplete), 0)
		response.Write(p.encodeMessage(msgCommandComplete, cmdPayload))
	} else {
		// Non-SELECT query
		cmdPayload := append([]byte("OK"), 0)
		response.Write(p.encodeMessage(msgCommandComplete, cmdPayload))
	}

	// Send ReadyForQuery
	response.Write(p.encodeMessage(msgReadyForQuery, []byte{'I'}))

	// Track state
	state.lastBackend = "primary"
	state.lastCacheHit = false

	metrics.DatabaseQueries.WithLabelValues("primary").Inc()
	metrics.QueryTotal.WithLabelValues(file, line, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())

	// Cache response if cacheable - use SetAndNotify for single-flight
	if parsed.IsCacheable() {
		p.cache.SetAndNotify(parsed.Query, response.Bytes(), time.Duration(parsed.TTL)*time.Second)
	}

	// Send response to client
	if _, err := client.Write(response.Bytes()); err != nil {
		log.Printf("[PostgreSQL] Client write error: %v", err)
	}
}

// refreshQueryInBackground refreshes a stale cache entry.
// Called when cache.FlagRefresh is returned (first stale access).
func (p *Proxy) refreshQueryInBackground(query string, db *sql.DB) {
	parsed := parser.Parse(query)
	rows, err := db.Query(parsed.Query)
	if err != nil {
		log.Printf("[PostgreSQL] Background refresh failed for query: %v", err)
		return
	}
	defer rows.Close()

	// Build response
	var response bytes.Buffer
	cols, _ := rows.Columns()
	if len(cols) > 0 {
		response.Write(p.buildRowDescription(cols))

		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		rowCount := 0
		for rows.Next() {
			rows.Scan(valuePtrs...)
			response.Write(p.buildDataRow(values))
			rowCount++
		}

		cmdPayload := append([]byte(fmt.Sprintf("SELECT %d", rowCount)), 0)
		response.Write(p.encodeMessage(msgCommandComplete, cmdPayload))
	}
	response.Write(p.encodeMessage(msgReadyForQuery, []byte{'I'}))

	// Update cache with fresh data
	p.cache.Set(parsed.Query, response.Bytes(), time.Duration(parsed.TTL)*time.Second)
}

func (p *Proxy) buildRowDescription(cols []string) []byte {
	var buf bytes.Buffer

	// Number of fields
	fieldCount := make([]byte, 2)
	binary.BigEndian.PutUint16(fieldCount, uint16(len(cols)))
	buf.Write(fieldCount)

	for _, col := range cols {
		buf.WriteString(col)
		buf.WriteByte(0)                      // null terminator
		buf.Write([]byte{0, 0, 0, 0})         // table OID
		buf.Write([]byte{0, 0})               // column attr number
		buf.Write([]byte{0, 0, 0, 25})        // data type OID (25 = text)
		buf.Write([]byte{255, 255})           // data type size (-1)
		buf.Write([]byte{255, 255, 255, 255}) // type modifier
		buf.Write([]byte{0, 0})               // format code (text)
	}

	return p.encodeMessage(msgRowDescription, buf.Bytes())
}

func (p *Proxy) buildDataRow(values []interface{}) []byte {
	var buf bytes.Buffer

	// Number of columns
	colCount := make([]byte, 2)
	binary.BigEndian.PutUint16(colCount, uint16(len(values)))
	buf.Write(colCount)

	for _, v := range values {
		if v == nil {
			buf.Write([]byte{255, 255, 255, 255}) // NULL (-1)
		} else {
			str := fmt.Sprintf("%v", v)
			lenBytes := make([]byte, 4)
			binary.BigEndian.PutUint32(lenBytes, uint32(len(str)))
			buf.Write(lenBytes)
			buf.WriteString(str)
		}
	}

	return p.encodeMessage(msgDataRow, buf.Bytes())
}

func (p *Proxy) encodeMessage(msgType byte, payload []byte) []byte {
	length := uint32(len(payload) + 4)
	msg := make([]byte, 1+4+len(payload))
	msg[0] = msgType
	binary.BigEndian.PutUint32(msg[1:5], length)
	copy(msg[5:], payload)
	return msg
}

func (p *Proxy) readStartupMessage(conn net.Conn) ([]byte, error) {
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBuf); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}

	return append(lengthBuf, payload...), nil
}

func (p *Proxy) readMessage(conn net.Conn) (byte, []byte, error) {
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, typeBuf); err != nil {
		return 0, nil, err
	}

	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBuf); err != nil {
		return 0, nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, nil, err
	}

	return typeBuf[0], payload, nil
}

func (p *Proxy) writeMessage(conn net.Conn, msgType byte, payload []byte) error {
	length := uint32(len(payload) + 4)

	if _, err := conn.Write([]byte{msgType}); err != nil {
		return err
	}

	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, length)
	if _, err := conn.Write(lengthBuf); err != nil {
		return err
	}

	if _, err := conn.Write(payload); err != nil {
		return err
	}

	return nil
}

func (p *Proxy) handleShowTQDBStatus(client net.Conn, state *connState) {
	var response bytes.Buffer

	// Prepare data
	backend := state.lastBackend
	if backend == "" {
		backend = "none"
	}
	cacheHit := "0"
	if state.lastCacheHit {
		cacheHit = "1"
	}

	// Build result set with columns: variable_name, value
	cols := []string{"variable_name", "value"}
	response.Write(p.buildRowDescription(cols))

	// Row 1: Backend
	response.Write(p.buildDataRow([]interface{}{"Backend", backend}))

	// Row 2: Cache_hit
	response.Write(p.buildDataRow([]interface{}{"Cache_hit", cacheHit}))

	// CommandComplete
	cmdPayload := append([]byte("SELECT 2"), 0)
	response.Write(p.encodeMessage(msgCommandComplete, cmdPayload))

	// ReadyForQuery
	response.Write(p.encodeMessage(msgReadyForQuery, []byte{'I'}))

	if _, err := client.Write(response.Bytes()); err != nil {
		log.Printf("[PostgreSQL] TQDB status response error: %v", err)
	}
}
