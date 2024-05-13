package mysql

import (
	"encoding/binary"
	"fmt"
)

type EOFPacket struct {
	Header      byte   `yaml:"header"`
	Warnings    uint16 `yaml:"warnings"`
	StatusFlags uint16 `yaml:"status_flags"`
}

func decodeMYSQLEOF(data []byte) (*EOFPacket, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("EOF packet too short")
	}

	if data[0] != 0xfe {
		return nil, fmt.Errorf("invalid EOF packet header")
	}

	packet := &EOFPacket{}
	packet.Header = data[0]

	if len(data) >= 5 {
		packet.Warnings = binary.LittleEndian.Uint16(data[1:3])
		packet.StatusFlags = binary.LittleEndian.Uint16(data[3:5])
	}

	return packet, nil
}
