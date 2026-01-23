package postgres

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mevdschee/tqdbproxy/parser"
)

// mockConn wraps a bytes.Buffer to implement net.Conn for testing
type mockConn struct {
	*bytes.Buffer
}

func (m *mockConn) Close() error                       { return nil }
func (m *mockConn) LocalAddr() net.Addr                { return nil }
func (m *mockConn) RemoteAddr() net.Addr               { return nil }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

func newMockConn() *mockConn {
	return &mockConn{Buffer: &bytes.Buffer{}}
}

func TestReadMessage(t *testing.T) {
	// Create a test message: Type 'Q', Length 14 (4 + 10), Payload "SELECT 1;\x00"
	conn := newMockConn()
	conn.WriteByte('Q')                              // Type
	binary.Write(conn, binary.BigEndian, uint32(14)) // Length (includes itself)
	conn.WriteString("SELECT 1;")                    // Payload
	conn.WriteByte(0)                                // Null terminator

	p := &Proxy{}
	msgType, payload, err := p.readMessage(conn)

	if err != nil {
		t.Fatalf("readMessage failed: %v", err)
	}

	if msgType != 'Q' {
		t.Errorf("Expected message type 'Q', got %c", msgType)
	}

	expectedPayload := "SELECT 1;\x00"
	if string(payload) != expectedPayload {
		t.Errorf("Expected payload %q, got %q", expectedPayload, string(payload))
	}
}

func TestWriteMessage(t *testing.T) {
	conn := newMockConn()
	p := &Proxy{}

	payload := []byte("SELECT 1;\x00")
	err := p.writeMessage(conn, 'Q', payload)

	if err != nil {
		t.Fatalf("writeMessage failed: %v", err)
	}

	// Verify the written data
	data := conn.Bytes()

	// Check type byte
	if data[0] != 'Q' {
		t.Errorf("Expected type 'Q', got %c", data[0])
	}

	// Check length (should be 4 + len(payload))
	length := binary.BigEndian.Uint32(data[1:5])
	expectedLength := uint32(4 + len(payload))
	if length != expectedLength {
		t.Errorf("Expected length %d, got %d", expectedLength, length)
	}

	// Check payload
	if string(data[5:]) != string(payload) {
		t.Errorf("Expected payload %q, got %q", payload, data[5:])
	}
}

func TestReadStartupMessage(t *testing.T) {
	// Create a test startup message: Length 8, Protocol 196608
	conn := newMockConn()
	binary.Write(conn, binary.BigEndian, uint32(8))      // Length
	binary.Write(conn, binary.BigEndian, uint32(196608)) // Protocol version

	p := &Proxy{}
	msg, err := p.readStartupMessage(conn)

	if err != nil {
		t.Fatalf("readStartupMessage failed: %v", err)
	}

	// Verify length
	length := binary.BigEndian.Uint32(msg[0:4])
	if length != 8 {
		t.Errorf("Expected length 8, got %d", length)
	}

	// Verify protocol version
	protocol := binary.BigEndian.Uint32(msg[4:8])
	if protocol != 196608 {
		t.Errorf("Expected protocol 196608, got %d", protocol)
	}
}

func TestQueryTypeLabel(t *testing.T) {
	tests := []struct {
		queryType parser.QueryType
		expected  string
	}{
		{parser.QuerySelect, "select"},
		{parser.QueryInsert, "insert"},
		{parser.QueryUpdate, "update"},
		{parser.QueryDelete, "delete"},
		{parser.QueryUnknown, "unknown"},
	}

	for _, tt := range tests {
		result := queryTypeLabel(tt.queryType)
		if result != tt.expected {
			t.Errorf("queryTypeLabel(%v) = %q, expected %q", tt.queryType, result, tt.expected)
		}
	}
}

func TestMessageConstants(t *testing.T) {
	// Verify message type constants are correct
	if msgQuery != 'Q' {
		t.Errorf("msgQuery should be 'Q', got %c", msgQuery)
	}
	if msgParse != 'P' {
		t.Errorf("msgParse should be 'P', got %c", msgParse)
	}
	if msgBind != 'B' {
		t.Errorf("msgBind should be 'B', got %c", msgBind)
	}
	if msgExecute != 'E' {
		t.Errorf("msgExecute should be 'E', got %c", msgExecute)
	}
	if msgSync != 'S' {
		t.Errorf("msgSync should be 'S', got %c", msgSync)
	}
	if msgTerminate != 'X' {
		t.Errorf("msgTerminate should be 'X', got %c", msgTerminate)
	}
	if msgReadyForQuery != 'Z' {
		t.Errorf("msgReadyForQuery should be 'Z', got %c", msgReadyForQuery)
	}
}

func TestRoundTripMessage(t *testing.T) {
	// Test writing and reading back a message
	conn := newMockConn()
	p := &Proxy{}

	originalPayload := []byte("SELECT * FROM users WHERE id = $1\x00")

	// Write message
	err := p.writeMessage(conn, 'Q', originalPayload)
	if err != nil {
		t.Fatalf("writeMessage failed: %v", err)
	}

	// Read it back
	msgType, payload, err := p.readMessage(conn)
	if err != nil && err != io.EOF {
		t.Fatalf("readMessage failed: %v", err)
	}

	if msgType != 'Q' {
		t.Errorf("Expected message type 'Q', got %c", msgType)
	}

	if !bytes.Equal(payload, originalPayload) {
		t.Errorf("Payload mismatch.\nExpected: %q\nGot: %q", originalPayload, payload)
	}
}
