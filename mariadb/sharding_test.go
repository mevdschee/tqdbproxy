package mariadb

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mevdschee/tqdbproxy/config"
	"github.com/mevdschee/tqdbproxy/replica"
)

// DEFAULT_CAPABILITY is used for testing
const DEFAULT_CAPABILITY uint32 = 0x018206FF

// mockBackend simulates a MariaDB server at a basic level
func mockBackend(t *testing.T, addr string) (net.Listener, string, chan bool) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("Failed to listen on %s: %v", addr, err)
	}
	actualAddr := l.Addr().String()
	stop := make(chan bool)
	go func() {
		<-stop
		l.Close() // Close listener to unblock Accept()
	}()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return // Listener closed
			}
			go handleMockConn(conn)
		}
	}()
	return l, actualAddr, stop
}

func handleMockConn(conn net.Conn) {
	defer conn.Close()
	// 1. Send Handshake Greeting
	version := "10.5.mock-MariaDB"
	data := make([]byte, 0, 128)
	data = append(data, 10) // Protocol
	data = append(data, []byte(version)...)
	data = append(data, 0)
	data = append(data, 1, 0, 0, 0)    // Conn ID
	data = append(data, "12345678"...) // Salt 1
	data = append(data, 0)             // Filler
	data = append(data, 0x00, 0xf7)    // Cap low (including Protocol 41 and Secure Connection)
	data = append(data, 33)            // Charset
	data = append(data, 0, 0)          // Status
	data = append(data, 0x00, 0x00)    // Cap high
	data = append(data, 21)            // Auth plugin data len
	data = append(data, make([]byte, 10)...)
	data = append(data, "123456789012"...) // Salt 2
	data = append(data, 0)                 // Terminator

	packet := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(packet[0:4], uint32(len(data)))
	packet[3] = 0
	copy(packet[4:], data)
	conn.Write(packet)

	// 2. Read Auth Response
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		fmt.Printf("Mock received error reading auth header: %v\n", err)
		return
	}
	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		fmt.Printf("Mock received error reading auth payload: %v\n", err)
		return
	}
	fmt.Printf("Mock received auth packet, len=%d\n", length)

	// 3. Send OK
	ok := []byte{0x07, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
	conn.Write(ok)
	fmt.Printf("Mock sent OK packet\n")

	// 4. Handle commands
	for {
		header := make([]byte, 4)
		conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := conn.Read(header); err != nil {
			return
		}
		length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}

		// Response will be OK most of the time
		conn.Write(ok)
	}
}

func TestShardSwitching(t *testing.T) {
	// Setup two mock backends
	l1, addr1, stop1 := mockBackend(t, "127.0.0.1:0")
	defer func() { stop1 <- true }()
	l2, addr2, stop2 := mockBackend(t, "127.0.0.1:0")
	defer func() { stop2 <- true }()

	// Setup Proxy with sharding config
	pcfg := config.ProxyConfig{
		Default: "main",
		DBMap: map[string]string{
			"shard_db": "shard1",
		},
	}
	pools := map[string]*replica.Pool{
		"main":   replica.NewPool(addr1, nil),
		"shard1": replica.NewPool(addr2, nil),
	}
	proxy := New(pcfg, pools, nil)

	// Create a mock clientConn
	conn1, _ := net.Dial("tcp", addr1)
	clientRemote, clientLocal := net.Pipe()
	defer clientRemote.Close()
	defer clientLocal.Close()

	// Background client to respond to auth switch
	go func() {
		for {
			header := make([]byte, 4)
			if _, err := io.ReadFull(clientRemote, header); err != nil {
				return
			}
			length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
			payload := make([]byte, length)
			if _, err := io.ReadFull(clientRemote, payload); err != nil {
				return
			}
			if len(payload) > 0 && payload[0] == 0xFE {
				// Respond to Auth Switch with dummy data
				resp := []byte{0x00, 0x00, 0x00, 0x00}
				packet := make([]byte, 4+len(resp))
				binary.LittleEndian.PutUint32(packet[0:4], uint32(len(resp)))
				packet[3] = header[3] + 1
				copy(packet[4:], resp)
				clientRemote.Write(packet)
			}
		}
	}()

	c := &clientConn{
		conn:        clientLocal,
		backend:     conn1,
		backendPool: pools["main"],
		backendAddr: addr1,
		proxy:       proxy,
		connID:      1,
		user:        "tqdbproxy",
		db:          "testdb",
		rawAuthPkt:  []byte{0x00, 0x00, 0x00, 0x00}, // dummy auth
	}

	// Test case 1: Stay on main
	err := c.ensureBackend("main_db")
	if err != nil {
		t.Fatalf("ensureBackend failed for main: %v", err)
	}
	if c.backendPool != pools["main"] {
		t.Error("Expected to stay on main pool")
	}

	// Test case 2: Switch to shard1
	err = c.ensureBackend("shard_db")
	if err != nil {
		t.Fatalf("ensureBackend failed for shard: %v", err)
	}
	if c.backendPool != pools["shard1"] {
		t.Error("Expected to switch to shard1 pool")
	}

	// Verify we are actually connected to the new port
	remoteAddr := c.backend.RemoteAddr().String()
	if remoteAddr != addr2 {
		t.Errorf("Backend port mismatch: got %v, want %s", remoteAddr, addr2)
	}

	l1.Close()
	l2.Close()
}

func TestInitialHandshake(t *testing.T) {
	// Setup mock backend
	l, addr, stop := mockBackend(t, "127.0.0.1:0")
	defer func() { stop <- true }() // Runs second (LIFO)
	defer l.Close()                 // Runs first - unblocks Accept()

	pcfg := config.ProxyConfig{Default: "main"}
	pools := map[string]*replica.Pool{
		"main": replica.NewPool(addr, nil),
	}
	proxy := New(pcfg, pools, nil)

	// Mock client connection
	clientRemote, clientLocal := net.Pipe()
	defer clientRemote.Close()
	defer clientLocal.Close()

	// Background client to handle the handshake
	go func() {
		// 1. Read Greeting from Proxy
		header := make([]byte, 4)
		if _, err := io.ReadFull(clientRemote, header); err != nil {
			return
		}
		length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
		payload := make([]byte, length)
		if _, err := io.ReadFull(clientRemote, payload); err != nil {
			return
		}

		// 2. Send Auth Response
		user := "tqdbproxy"
		authResp := []byte{0x01, 0x02, 0x03, 0x04} // Dummy scramble

		// Handshake Response Packet: [cap 4] [max_packet 4] [charset 1] [reserved 23] [user\0] [auth_len 1] [auth]
		resp := make([]byte, 4+4+1+23)
		binary.LittleEndian.PutUint32(resp[0:4], DEFAULT_CAPABILITY)
		resp[8] = 33 // charset
		resp = append(resp, []byte(user)...)
		resp = append(resp, 0)
		resp = append(resp, byte(len(authResp)))
		resp = append(resp, authResp...)

		packet := make([]byte, 4+len(resp))
		binary.LittleEndian.PutUint32(packet[0:4], uint32(len(resp)))
		packet[3] = 1 // seq
		copy(packet[4:], resp)
		clientRemote.Write(packet)

		// 3. Read OK from Proxy
		if _, err := io.ReadFull(clientRemote, header); err != nil {
			return
		}
		length = int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
		payload = make([]byte, length)
		io.ReadFull(clientRemote, payload)
	}()

	// Simulate Proxy's handleConnection logic
	conn := &clientConn{
		conn:        clientLocal,
		backendPool: pools["main"],
		proxy:       proxy,
		connID:      1,
		sequence:    0,
	}

	backend, err := conn.dialAndAuth(addr)
	if err != nil {
		t.Fatalf("Initial dialAndAuth failed: %v", err)
	}
	defer backend.Close()

	if conn.user != "tqdbproxy" {
		t.Errorf("Expected user tqdbproxy, got %s", conn.user)
	}
}
func TestSharding_Use(t *testing.T) {
	// Setup two mock backends
	l1, addr1, stop1 := mockBackend(t, "127.0.0.1:0")
	defer l1.Close()
	defer func() { stop1 <- true }()
	l2, addr2, stop2 := mockBackend(t, "127.0.0.1:0")
	defer l2.Close()
	defer func() { stop2 <- true }()

	// Setup Proxy with sharding config
	pcfg := config.ProxyConfig{
		Default: "main",
		DBMap: map[string]string{
			"shard_db": "shard1",
		},
	}
	pools := map[string]*replica.Pool{
		"main":   replica.NewPool(addr1, nil),
		"shard1": replica.NewPool(addr2, nil),
	}
	proxy := New(pcfg, pools, nil)

	// Create a mock clientConn
	conn1, _ := net.Dial("tcp", addr1)
	clientRemote, clientLocal := net.Pipe()
	defer clientRemote.Close()
	defer clientLocal.Close()

	// Background client to respond to auth switch
	go func() {
		for {
			header := make([]byte, 4)
			if _, err := io.ReadFull(clientRemote, header); err != nil {
				return
			}
			length := int(uint32(header[0]) | uint32(header[1])<<(8) | uint32(header[2])<<(16))
			payload := make([]byte, length)
			if _, err := io.ReadFull(clientRemote, payload); err != nil {
				return
			}
			if len(payload) > 0 && payload[0] == 0xFE { // Auth Switch
				resp := []byte{0x00, 0x00, 0x00, 0x00}
				packet := make([]byte, 4+len(resp))
				binary.LittleEndian.PutUint32(packet[0:4], uint32(len(resp)))
				packet[3] = header[3] + 1
				copy(packet[4:], resp)
				clientRemote.Write(packet)
			}
		}
	}()

	c := &clientConn{
		conn:        clientLocal,
		backend:     conn1,
		backendPool: pools["main"],
		backendAddr: addr1,
		proxy:       proxy,
		connID:      1,
		user:        "tqdbproxy",
		db:          "testdb",
		rawAuthPkt:  []byte{0x00, 0x00, 0x00, 0x00},
	}

	// Test USE shard_db
	err := c.handleQuery("USE shard_db")

	// Close clientRemote to unblock the mock client goroutine
	clientRemote.Close()

	if err != nil {
		t.Fatalf("USE shard_db failed: %v", err)
	}

	if c.backendPool != pools["shard1"] {
		t.Error("Expected to switch to shard1 pool after USE")
	}
}

func TestSharding_FQN(t *testing.T) {
	// Setup two mock backends
	l1, addr1, stop1 := mockBackend(t, "127.0.0.1:0")
	defer l1.Close()
	defer func() { stop1 <- true }()
	l2, addr2, stop2 := mockBackend(t, "127.0.0.1:0")
	defer l2.Close()
	defer func() { stop2 <- true }()

	// Setup Proxy with sharding config
	pcfg := config.ProxyConfig{
		Default: "main",
		DBMap: map[string]string{
			"shard_db": "shard1",
		},
	}
	pools := map[string]*replica.Pool{
		"main":   replica.NewPool(addr1, nil),
		"shard1": replica.NewPool(addr2, nil),
	}
	proxy := New(pcfg, pools, nil)

	// Create a mock clientConn
	conn1, _ := net.Dial("tcp", addr1)
	clientRemote, clientLocal := net.Pipe()
	defer clientRemote.Close()
	defer clientLocal.Close()

	// Background client to respond to auth switch
	go func() {
		for {
			header := make([]byte, 4)
			if _, err := io.ReadFull(clientRemote, header); err != nil {
				return
			}
			length := int(uint32(header[0]) | uint32(header[1])<<(8) | uint32(header[2])<<(16))
			payload := make([]byte, length)
			if _, err := io.ReadFull(clientRemote, payload); err != nil {
				return
			}
			if len(payload) > 0 {
				if payload[0] == 0xFE { // Auth Switch
					resp := []byte{0x00, 0x00, 0x00, 0x00}
					packet := make([]byte, 4+len(resp))
					binary.LittleEndian.PutUint32(packet[0:4], uint32(len(resp)))
					packet[3] = header[3] + 1
					copy(packet[4:], resp)
					clientRemote.Write(packet)
				} else {
					// Respond with a dummy result set for the query
					// [len 3] [seq] [col count 1]
					// [len 3] [seq] [col def]
					// [len 3] [seq] [EOF]
					// [len 3] [seq] [OK]
					ok := []byte{0x07, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
					ok[3] = header[3] + 1
					clientRemote.Write(ok)
				}
			}
		}
	}()

	c := &clientConn{
		conn:        clientLocal,
		backend:     conn1,
		backendPool: pools["main"],
		backendAddr: addr1,
		proxy:       proxy,
		connID:      1,
		user:        "tqdbproxy",
		db:          "testdb",
		rawAuthPkt:  []byte{0x00, 0x00, 0x00, 0x00},
	}

	// Test FQN query
	err := c.handleQuery("UPDATE shard_db.test_table SET val=1")

	// Close clientRemote to unblock the mock client goroutine
	clientRemote.Close()

	if err != nil {
		t.Fatalf("FQN query failed: %v", err)
	}

	if c.backendPool != pools["shard1"] {
		t.Error("Expected to switch to shard1 pool after FQN query")
	}
}
