package mariadb

import (
	"encoding/binary"
	"net"
	"testing"

	"github.com/mevdschee/tqdbproxy/config"
	"github.com/mevdschee/tqdbproxy/replica"
)

// mockBackend simulates a MariaDB server at a basic level
func mockBackend(t *testing.T, addr string) (net.Listener, chan bool) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("Failed to listen on %s: %v", addr, err)
	}
	stop := make(chan bool)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			go handleMockConn(conn)
		}
	}()
	return l, stop
}

func handleMockConn(conn net.Conn) {
	defer conn.Close()
	// 1. Send Handshake Greeting
	// [len 3] [seq 1] [protocol 1] [version...] [conn_id 4] [salt 8] [null 1] [cap_low 2] [charset 1] [status 2] [cap_high 2] [auth_plugin_len 1] [reserved 10] [salt2 12] [null 1]
	greeting := make([]byte, 80)
	binary.LittleEndian.PutUint32(greeting[0:4], uint32(len(greeting)-4))
	greeting[3] = 0  // seq
	greeting[4] = 10 // protocol version
	copy(greeting[5:], "10.5.mock")
	// salt
	copy(greeting[32:40], "12345678")
	conn.Write(greeting)

	// 2. Read Auth Response
	header := make([]byte, 4)
	net.Conn(conn).Read(header)
	length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
	payload := make([]byte, length)
	conn.Read(payload)

	// 3. Send OK
	ok := []byte{0x07, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
	conn.Write(ok)

	// 4. Handle commands
	for {
		header := make([]byte, 4)
		if _, err := conn.Read(header); err != nil {
			return
		}
		length := int(uint32(header[0]) | uint32(header[1])<<8 | uint32(header[2])<<16)
		payload := make([]byte, length)
		if _, err := conn.Read(payload); err != nil {
			return
		}

		// Response will be OK most of the time
		conn.Write(ok)
	}
}

func TestShardSwitching(t *testing.T) {
	// Setup two mock backends
	l1, stop1 := mockBackend(t, "127.0.0.1:3311")
	defer func() { stop1 <- true }()
	l2, stop2 := mockBackend(t, "127.0.0.1:3322")
	defer func() { stop2 <- true }()

	// Setup Proxy with sharding config
	pcfg := config.ProxyConfig{
		Default: "main",
		DBMap: map[string]string{
			"shard_db": "shard1",
		},
	}
	pools := map[string]*replica.Pool{
		"main":   replica.NewPool("127.0.0.1:3311", nil),
		"shard1": replica.NewPool("127.0.0.1:3322", nil),
	}
	proxy := New(pcfg, pools, nil)

	// Create a mock clientConn
	conn1, _ := net.Dial("tcp", "127.0.0.1:3311")
	c := &clientConn{
		backend:     conn1,
		backendPool: pools["main"],
		proxy:       proxy,
		connID:      1,
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
	if remoteAddr != "127.0.0.1:3322" {
		t.Errorf("Backend port mismatch: got %v, want 127.0.0.1:3322", remoteAddr)
	}

	l1.Close()
	l2.Close()
}
