package postgres

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/metrics"
	"github.com/mevdschee/tqdbproxy/parser"
	"github.com/mevdschee/tqdbproxy/replica"
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

// Proxy handles PostgreSQL protocol connections with caching
type Proxy struct {
	listen      string
	replicaPool *replica.Pool
	cache       *cache.Cache
}

// New creates a new PostgreSQL proxy
func New(listen string, pool *replica.Pool, c *cache.Cache) *Proxy {
	return &Proxy{
		listen:      listen,
		replicaPool: pool,
		cache:       c,
	}
}

// Start begins accepting PostgreSQL connections
func (p *Proxy) Start() error {
	listener, err := net.Listen("tcp", p.listen)
	if err != nil {
		return err
	}
	log.Printf("[PostgreSQL] Listening on %s, forwarding to %s", p.listen, p.replicaPool.GetPrimary())

	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				log.Printf("[PostgreSQL] Accept error: %v", err)
				continue
			}
			go p.handleConnection(client)
		}
	}()

	return nil
}

func (p *Proxy) handleConnection(client net.Conn) {
	defer client.Close()

	primary, err := net.Dial("tcp", p.replicaPool.GetPrimary())
	if err != nil {
		log.Printf("[PostgreSQL] Primary connection error: %v", err)
		return
	}
	defer primary.Close()

	// Phase 1: Startup and authentication - pass through
	if err := p.handleStartup(client, primary); err != nil {
		log.Printf("[PostgreSQL] Startup error: %v", err)
		return
	}

	// Phase 2: Command phase - intercept queries
	p.handleMessages(client, primary)
}

func (p *Proxy) handleStartup(client, primary net.Conn) error {
	// Read startup message from client (no type byte, just length + payload)
	startupMsg, err := p.readStartupMessage(client)
	if err != nil {
		return err
	}

	// Forward to primary
	if _, err := primary.Write(startupMsg); err != nil {
		return err
	}

	// Handle authentication flow (pass through until ReadyForQuery)
	for {
		msgType, payload, err := p.readMessage(primary)
		if err != nil {
			return err
		}

		// Write to client
		if err := p.writeMessage(client, msgType, payload); err != nil {
			return err
		}

		// ReadyForQuery ('Z') signals end of startup
		if msgType == msgReadyForQuery {
			break
		}

		// If server needs auth response, read from client and forward
		if msgType == 'R' { // Authentication request
			// Check if it needs a response (not AuthenticationOk)
			if len(payload) >= 4 {
				authType := binary.BigEndian.Uint32(payload[0:4])
				if authType != 0 { // Not AuthenticationOk
					// Read client response
					respType, respPayload, err := p.readMessage(client)
					if err != nil {
						return err
					}
					// Forward to primary
					if err := p.writeMessage(primary, respType, respPayload); err != nil {
						return err
					}
				}
			}
		}
	}

	return nil
}

func (p *Proxy) handleMessages(client, primary net.Conn) {
	for {
		msgType, payload, err := p.readMessage(client)
		if err != nil {
			if err != io.EOF {
				log.Printf("[PostgreSQL] Read error: %v", err)
			}
			return
		}

		switch msgType {
		case msgQuery:
			p.handleQuery(payload, client, primary)
		case msgTerminate:
			// Forward and exit
			p.writeMessage(primary, msgType, payload)
			return
		default:
			// Pass through other messages (Parse, Bind, Execute, etc.)
			p.forwardMessageAndResponse(msgType, payload, client, primary)
		}
	}
}

func (p *Proxy) handleQuery(payload []byte, client, primary net.Conn) {
	start := time.Now()

	// Query is null-terminated string
	queryBytes := payload
	if len(queryBytes) > 0 && queryBytes[len(queryBytes)-1] == 0 {
		queryBytes = queryBytes[:len(queryBytes)-1]
	}
	query := string(queryBytes)
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
			if _, err := client.Write(cached); err != nil {
				log.Printf("[PostgreSQL] Cache response error: %v", err)
			}
			return
		}
		metrics.CacheMisses.WithLabelValues(file, line).Inc()
	}

	// Forward query to primary
	if err := p.writeMessage(primary, msgQuery, payload); err != nil {
		log.Printf("[PostgreSQL] Primary write error: %v", err)
		return
	}

	// Read and capture full response (until ReadyForQuery)
	response, err := p.readQueryResponse(primary)
	if err != nil {
		log.Printf("[PostgreSQL] Primary read error: %v", err)
		return
	}

	metrics.DatabaseQueries.WithLabelValues("primary").Inc()
	metrics.QueryTotal.WithLabelValues(file, line, queryType, "false").Inc()
	metrics.QueryLatency.WithLabelValues(file, line, queryType).Observe(time.Since(start).Seconds())

	// Cache the response if cacheable
	if parsed.IsCacheable() {
		p.cache.Set(parsed.Query, response, time.Duration(parsed.TTL)*time.Second)
	}

	// Send response to client
	if _, err := client.Write(response); err != nil {
		log.Printf("[PostgreSQL] Client write error: %v", err)
	}
}

func (p *Proxy) forwardMessageAndResponse(msgType byte, payload []byte, client, primary net.Conn) {
	// Forward message to primary
	if err := p.writeMessage(primary, msgType, payload); err != nil {
		return
	}

	// Read response until ReadyForQuery or ErrorResponse
	for {
		respType, respPayload, err := p.readMessage(primary)
		if err != nil {
			return
		}

		// Forward to client
		if err := p.writeMessage(client, respType, respPayload); err != nil {
			return
		}

		// Stop at ReadyForQuery
		if respType == msgReadyForQuery {
			break
		}
	}
}

func (p *Proxy) readStartupMessage(conn net.Conn) ([]byte, error) {
	// Read length (4 bytes)
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBuf); err != nil {
		return nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)

	// Read payload (length includes the 4 bytes we just read)
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, err
	}

	// Return complete message (length + payload)
	return append(lengthBuf, payload...), nil
}

func (p *Proxy) readMessage(conn net.Conn) (byte, []byte, error) {
	// Read type byte
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, typeBuf); err != nil {
		return 0, nil, err
	}

	// Read length (4 bytes, includes itself but not type byte)
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lengthBuf); err != nil {
		return 0, nil, err
	}

	length := binary.BigEndian.Uint32(lengthBuf)

	// Read payload (length - 4 bytes for length field itself)
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, nil, err
	}

	return typeBuf[0], payload, nil
}

func (p *Proxy) writeMessage(conn net.Conn, msgType byte, payload []byte) error {
	// Calculate length (includes length field itself, but not type byte)
	length := uint32(len(payload) + 4)

	// Write type byte
	if _, err := conn.Write([]byte{msgType}); err != nil {
		return err
	}

	// Write length
	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, length)
	if _, err := conn.Write(lengthBuf); err != nil {
		return err
	}

	// Write payload
	if _, err := conn.Write(payload); err != nil {
		return err
	}

	return nil
}

func (p *Proxy) readQueryResponse(conn net.Conn) ([]byte, error) {
	var buf bytes.Buffer

	for {
		msgType, payload, err := p.readMessage(conn)
		if err != nil {
			return nil, err
		}

		// Write message to buffer
		buf.WriteByte(msgType)
		lengthBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(lengthBuf, uint32(len(payload)+4))
		buf.Write(lengthBuf)
		buf.Write(payload)

		// Stop at ReadyForQuery
		if msgType == msgReadyForQuery {
			break
		}
	}

	return buf.Bytes(), nil
}
