package mariadb

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
)

// MariaDB protocol constants
const (
	OK_HEADER  = 0x00
	ERR_HEADER = 0xff
	EOF_HEADER = 0xfe

	// Server capabilities
	CLIENT_LONG_PASSWORD                  = 0x00000001
	CLIENT_FOUND_ROWS                     = 0x00000002
	CLIENT_LONG_FLAG                      = 0x00000004
	CLIENT_CONNECT_WITH_DB                = 0x00000008
	CLIENT_NO_SCHEMA                      = 0x00000010
	CLIENT_COMPRESS                       = 0x00000020
	CLIENT_ODBC                           = 0x00000040
	CLIENT_LOCAL_FILES                    = 0x00000080
	CLIENT_IGNORE_SPACE                   = 0x00000100
	CLIENT_PROTOCOL_41                    = 0x00000200
	CLIENT_INTERACTIVE                    = 0x00000400
	CLIENT_SSL                            = 0x00000800
	CLIENT_IGNORE_SIGPIPE                 = 0x00001000
	CLIENT_TRANSACTIONS                   = 0x00002000
	CLIENT_RESERVED                       = 0x00004000
	CLIENT_SECURE_CONNECTION              = 0x00008000
	CLIENT_MULTI_STATEMENTS               = 0x00010000
	CLIENT_MULTI_RESULTS                  = 0x00020000
	CLIENT_PS_MULTI_RESULTS               = 0x00040000
	CLIENT_PLUGIN_AUTH                    = 0x00080000
	CLIENT_CONNECT_ATTRS                  = 0x00100000
	CLIENT_PLUGIN_AUTH_LENENC_CLIENT_DATA = 0x00200000
	CLIENT_CAN_HANDLE_EXPIRED_PASSWORDS   = 0x00400000
	CLIENT_SESSION_TRACK                  = 0x00800000
	CLIENT_DEPRECATE_EOF                  = 0x01000000

	// Default server capability
	DEFAULT_CAPABILITY = CLIENT_LONG_PASSWORD | CLIENT_LONG_FLAG |
		CLIENT_CONNECT_WITH_DB | CLIENT_PROTOCOL_41 |
		CLIENT_TRANSACTIONS | CLIENT_SECURE_CONNECTION

	// Server status flags
	SERVER_STATUS_IN_TRANS             = 0x0001
	SERVER_STATUS_AUTOCOMMIT           = 0x0002
	SERVER_MORE_RESULTS_EXISTS         = 0x0008
	SERVER_STATUS_NO_GOOD_INDEX_USED   = 0x0010
	SERVER_STATUS_NO_INDEX_USED        = 0x0020
	SERVER_STATUS_CURSOR_EXISTS        = 0x0040
	SERVER_STATUS_LAST_ROW_SENT        = 0x0080
	SERVER_STATUS_DB_DROPPED           = 0x0100
	SERVER_STATUS_NO_BACKSLASH_ESCAPES = 0x0200
	SERVER_STATUS_METADATA_CHANGED     = 0x0400
	SERVER_QUERY_WAS_SLOW              = 0x0800
	SERVER_PS_OUT_PARAMS               = 0x1000
)

var ServerVersion = []byte("5.7.0-tqdbproxy")

// GenerateSalt generates a 20-byte random salt for authentication
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, 20)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	// Ensure no null bytes
	for i := range salt {
		if salt[i] == 0 {
			salt[i] = 'a'
		}
	}
	return salt, nil
}

// CalcPassword calculates MariaDB native password hash
// scramble = SHA1(salt + SHA1(SHA1(password)))
func CalcPassword(salt, password []byte) []byte {
	if len(password) == 0 {
		return nil
	}

	// stage1Hash = SHA1(password)
	crypt := sha1.New()
	crypt.Write(password)
	stage1 := crypt.Sum(nil)

	// stage2Hash = SHA1(stage1Hash)
	crypt.Reset()
	crypt.Write(stage1)
	stage2 := crypt.Sum(nil)

	// scrambleHash = SHA1(salt + stage2Hash)
	crypt.Reset()
	crypt.Write(salt)
	crypt.Write(stage2)
	scramble := crypt.Sum(nil)

	// token = stage1Hash XOR scrambleHash
	for i := range scramble {
		scramble[i] ^= stage1[i]
	}
	return scramble
}

// PutLengthEncodedInt encodes an integer as MariaDB length-encoded integer
func PutLengthEncodedInt(n uint64) []byte {
	switch {
	case n < 251:
		return []byte{byte(n)}
	case n < 1<<16:
		return []byte{0xfc, byte(n), byte(n >> 8)}
	case n < 1<<24:
		return []byte{0xfd, byte(n), byte(n >> 8), byte(n >> 16)}
	default:
		return []byte{0xfe,
			byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24),
			byte(n >> 32), byte(n >> 40), byte(n >> 48), byte(n >> 56)}
	}
}

// PutLengthEncodedString encodes a string with its length prefix
func PutLengthEncodedString(s []byte) []byte {
	result := PutLengthEncodedInt(uint64(len(s)))
	return append(result, s...)
}

// ReadLengthEncodedInt reads a MariaDB length-encoded integer
// Returns: value, isNull, bytesRead
func ReadLengthEncodedInt(b []byte) (uint64, bool, int) {
	if len(b) == 0 {
		return 0, false, 0
	}

	switch b[0] {
	case 0xfb: // NULL
		return 0, true, 1
	case 0xfc: // 2-byte int
		if len(b) < 3 {
			return 0, false, 0
		}
		return uint64(b[1]) | uint64(b[2])<<8, false, 3
	case 0xfd: // 3-byte int
		if len(b) < 4 {
			return 0, false, 0
		}
		return uint64(b[1]) | uint64(b[2])<<8 | uint64(b[3])<<16, false, 4
	case 0xfe: // 8-byte int
		if len(b) < 9 {
			return 0, false, 0
		}
		return uint64(b[1]) | uint64(b[2])<<8 | uint64(b[3])<<16 | uint64(b[4])<<24 |
			uint64(b[5])<<32 | uint64(b[6])<<40 | uint64(b[7])<<48 | uint64(b[8])<<56, false, 9
	default: // 1-byte int
		return uint64(b[0]), false, 1
	}
}

// WriteOKPacket creates a MariaDB OK packet
func WriteOKPacket(affectedRows, insertId uint64, status uint16, capability uint32) []byte {
	data := make([]byte, 4, 32)
	data = append(data, OK_HEADER)
	data = append(data, PutLengthEncodedInt(affectedRows)...)
	data = append(data, PutLengthEncodedInt(insertId)...)

	if capability&CLIENT_PROTOCOL_41 > 0 {
		data = append(data, byte(status), byte(status>>8))
		data = append(data, 0, 0) // warnings
	}

	// Set packet length
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(data)-4))
	return data
}

// WriteErrorPacket creates a MariaDB error packet
func WriteErrorPacket(errno uint16, sqlState, message string, capability uint32) []byte {
	data := make([]byte, 4, 16+len(message))
	data = append(data, ERR_HEADER)
	data = append(data, byte(errno), byte(errno>>8))

	if capability&CLIENT_PROTOCOL_41 > 0 {
		data = append(data, '#')
		data = append(data, []byte(sqlState)...)
	}

	data = append(data, []byte(message)...)

	// Set packet length
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(data)-4))
	return data
}

// WriteEOFPacket creates a MariaDB EOF packet
func WriteEOFPacket(status uint16, capability uint32) []byte {
	data := make([]byte, 4, 9)
	data = append(data, EOF_HEADER)

	if capability&CLIENT_PROTOCOL_41 > 0 {
		data = append(data, 0, 0) // warnings
		data = append(data, byte(status), byte(status>>8))
	}

	// Set packet length
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(data)-4))
	return data
}
