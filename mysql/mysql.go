package mysql

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/metrics"
	"github.com/mevdschee/tqdbproxy/parser"
)

const (
	comQuery       = 0x03
	comStmtPrepare = 0x16
	comStmtExecute = 0x17
)

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

// Proxy handles MySQL protocol connections with caching
type Proxy struct {
	listen  string
	backend string
	cache   *cache.Cache
}

// New creates a new MySQL proxy
func New(listen, backend string, c *cache.Cache) *Proxy {
	return &Proxy{
		listen:  listen,
		backend: backend,
		cache:   c,
	}
}

// Start begins accepting MySQL connections
func (p *Proxy) Start() error {
	listener, err := net.Listen("tcp", p.listen)
	if err != nil {
		return err
	}
	log.Printf("[MySQL] Listening on %s, forwarding to %s", p.listen, p.backend)

	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				log.Printf("[MySQL] Accept error: %v", err)
				continue
			}
			go p.handleConnection(client)
		}
	}()

	return nil
}

func (p *Proxy) handleConnection(client net.Conn) {
	defer client.Close()

	backend, err := net.Dial("tcp", p.backend)
	if err != nil {
		log.Printf("[MySQL] Backend connection error: %v", err)
		return
	}
	defer backend.Close()

	// Phase 1: Handshake - pass through until authenticated
	if err := p.handleHandshake(client, backend); err != nil {
		log.Printf("[MySQL] Handshake error: %v", err)
		return
	}

	// Track prepared statements: stmt_id -> parsed query
	stmts := make(map[uint32]*parser.ParsedQuery)

	// Phase 2: Command phase - intercept queries
	p.handleCommands(client, backend, stmts)
}

func (p *Proxy) handleHandshake(client, backend net.Conn) error {
	// Forward server greeting to client
	greeting, err := p.readPacket(backend)
	if err != nil {
		return err
	}
	if err := p.writePacket(client, greeting); err != nil {
		return err
	}

	// Forward client auth response to server
	authResp, err := p.readPacket(client)
	if err != nil {
		return err
	}
	if err := p.writePacket(backend, authResp); err != nil {
		return err
	}

	// Forward server auth result to client (may be multi-packet)
	for {
		result, err := p.readPacket(backend)
		if err != nil {
			return err
		}
		if err := p.writePacket(client, result); err != nil {
			return err
		}
		// OK packet (0x00) or Error packet (0xFF) ends auth
		if len(result) > 4 && (result[4] == 0x00 || result[4] == 0xFF) {
			break
		}
	}

	return nil
}

func (p *Proxy) handleCommands(client, backend net.Conn, stmts map[uint32]*parser.ParsedQuery) {
	for {
		packet, err := p.readPacket(client)
		if err != nil {
			if err != io.EOF {
				log.Printf("[MySQL] Read error: %v", err)
			}
			return
		}

		if len(packet) < 5 {
			p.forwardPacketAndResponse(packet, client, backend)
			continue
		}

		cmdType := packet[4]

		switch cmdType {
		case comQuery:
			p.handleQuery(packet, client, backend)

		case comStmtPrepare:
			p.handlePrepare(packet, client, backend, stmts)

		case comStmtExecute:
			p.handleExecute(packet, client, backend, stmts)

		default:
			p.forwardPacketAndResponse(packet, client, backend)
		}
	}
}

func (p *Proxy) handleQuery(packet []byte, client, backend net.Conn) {
	start := time.Now()
	query := string(packet[5:])
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

	// Check cache for cacheable queries
	if parsed.IsCacheable() {
		if cached, ok := p.cache.Get(parsed.Query); ok {
			metrics.CacheHits.WithLabelValues(file, line).Inc()
			metrics.QueryTotal.WithLabelValues(file, line, queryType, "true").Inc()
			metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())
			if err := p.writeRaw(client, cached); err != nil {
				log.Printf("[MySQL] Cache response error: %v", err)
			}
			return
		}
		metrics.CacheMisses.WithLabelValues(file, line).Inc()
	}

	// Forward to backend and capture response
	if err := p.writePacket(backend, packet); err != nil {
		log.Printf("[MySQL] Backend write error: %v", err)
		return
	}

	response, err := p.readFullResponse(backend)
	if err != nil {
		log.Printf("[MySQL] Backend read error: %v", err)
		return
	}

	metrics.DatabaseQueries.WithLabelValues("primary").Inc()
	metrics.QueryTotal.WithLabelValues(file, line, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())

	// Cache the response if cacheable
	if parsed.IsCacheable() {
		p.cache.Set(parsed.Query, response, time.Duration(parsed.TTL)*time.Second)
	}

	if err := p.writeRaw(client, response); err != nil {
		log.Printf("[MySQL] Client write error: %v", err)
	}
}

func (p *Proxy) handlePrepare(packet []byte, client, backend net.Conn, stmts map[uint32]*parser.ParsedQuery) {
	query := string(packet[5:])
	parsed := parser.Parse(query)

	// Forward PREPARE to backend
	if err := p.writePacket(backend, packet); err != nil {
		log.Printf("[MySQL] Backend write error: %v", err)
		return
	}

	response, err := p.readPrepareResponse(backend)
	if err != nil {
		log.Printf("[MySQL] Backend read error: %v", err)
		return
	}

	// Extract statement ID from response and store parsed query
	if len(response) >= 9 && response[4] == 0x00 {
		stmtID := binary.LittleEndian.Uint32(response[5:9])
		stmts[stmtID] = parsed
	}

	if err := p.writeRaw(client, response); err != nil {
		log.Printf("[MySQL] Client write error: %v", err)
	}
}

func (p *Proxy) handleExecute(packet []byte, client, backend net.Conn, stmts map[uint32]*parser.ParsedQuery) {
	if len(packet) < 9 {
		p.forwardPacketAndResponse(packet, client, backend)
		return
	}

	stmtID := binary.LittleEndian.Uint32(packet[5:9])
	parsed, ok := stmts[stmtID]

	// Build cache key: query + params (raw packet bytes after stmt_id)
	var cacheKey string
	if ok && parsed.IsCacheable() {
		params := packet[9:]
		cacheKey = parsed.Query + string(params)

		if cached, cacheOk := p.cache.Get(cacheKey); cacheOk {
			if err := p.writeRaw(client, cached); err != nil {
				log.Printf("[MySQL] Cache response error: %v", err)
			}
			return
		}
	}

	// Forward to backend
	if err := p.writePacket(backend, packet); err != nil {
		log.Printf("[MySQL] Backend write error: %v", err)
		return
	}

	response, err := p.readFullResponse(backend)
	if err != nil {
		log.Printf("[MySQL] Backend read error: %v", err)
		return
	}

	// Cache if cacheable
	if ok && parsed.IsCacheable() && cacheKey != "" {
		p.cache.Set(cacheKey, response, time.Duration(parsed.TTL)*time.Second)
	}

	if err := p.writeRaw(client, response); err != nil {
		log.Printf("[MySQL] Client write error: %v", err)
	}
}

func (p *Proxy) forwardPacketAndResponse(packet []byte, client, backend net.Conn) {
	if err := p.writePacket(backend, packet); err != nil {
		return
	}
	response, err := p.readFullResponse(backend)
	if err != nil {
		return
	}
	p.writeRaw(client, response)
}

func (p *Proxy) readPrepareResponse(conn net.Conn) ([]byte, error) {
	var buf bytes.Buffer

	// Read first packet (OK or Error)
	packet, err := p.readPacket(conn)
	if err != nil {
		return nil, err
	}
	buf.Write(packet)

	// Error or malformed packet
	if len(packet) < 13 || packet[4] != 0x00 {
		return buf.Bytes(), nil
	}

	// Parse OK_Prepare response
	numParams := int(binary.LittleEndian.Uint16(packet[9:11]))
	numColumns := int(binary.LittleEndian.Uint16(packet[11:13]))

	// Read param definitions
	for i := 0; i < numParams; i++ {
		packet, err := p.readPacket(conn)
		if err != nil {
			return nil, err
		}
		buf.Write(packet)
	}

	// Read EOF after params if numParams > 0
	if numParams > 0 {
		packet, err := p.readPacket(conn)
		if err != nil {
			return nil, err
		}
		buf.Write(packet)
	}

	// Read column definitions
	for i := 0; i < numColumns; i++ {
		packet, err := p.readPacket(conn)
		if err != nil {
			return nil, err
		}
		buf.Write(packet)
	}

	// Read EOF after columns if numColumns > 0
	if numColumns > 0 {
		packet, err := p.readPacket(conn)
		if err != nil {
			return nil, err
		}
		buf.Write(packet)
	}

	return buf.Bytes(), nil
}

func (p *Proxy) readPacket(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}

	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}

	return append(header, payload...), nil
}

func (p *Proxy) writePacket(conn net.Conn, packet []byte) error {
	_, err := conn.Write(packet)
	return err
}

func (p *Proxy) writeRaw(conn net.Conn, data []byte) error {
	_, err := conn.Write(data)
	return err
}

func (p *Proxy) readFullResponse(conn net.Conn) ([]byte, error) {
	var buf bytes.Buffer

	// Read first packet
	packet, err := p.readPacket(conn)
	if err != nil {
		return nil, err
	}
	buf.Write(packet)

	// Check if it's OK, Error, or EOF packet (single packet response)
	if len(packet) > 4 {
		firstByte := packet[4]
		if firstByte == 0x00 || firstByte == 0xFF || firstByte == 0xFE {
			return buf.Bytes(), nil
		}
	}

	// It's a result set - read column count
	columnCount := int(packet[4])
	if columnCount == 0 {
		return buf.Bytes(), nil
	}

	// Read column definitions
	for i := 0; i < columnCount; i++ {
		packet, err := p.readPacket(conn)
		if err != nil {
			return nil, err
		}
		buf.Write(packet)
	}

	// Read EOF after columns (deprecated in MySQL 5.7.5+ with CLIENT_DEPRECATE_EOF)
	packet, err = p.readPacket(conn)
	if err != nil {
		return nil, err
	}
	buf.Write(packet)

	// Read rows until EOF or Error
	for {
		packet, err := p.readPacket(conn)
		if err != nil {
			return nil, err
		}
		buf.Write(packet)

		if len(packet) > 4 && (packet[4] == 0xFE || packet[4] == 0xFF) {
			break
		}
	}

	return buf.Bytes(), nil
}

var _ = sync.Pool{}         // Ensure sync is imported
var _ = binary.LittleEndian // Ensure binary is imported
