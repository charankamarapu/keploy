package mysql

import "fmt"

type QueryPacket struct {
	Command byte   `yaml:"command"`
	Query   string `yaml:"query"`
}

func decodeMySQLQuery(data []byte) (*QueryPacket, error) {
	if len(data) < 1 {
		return nil, fmt.Errorf("query packet too short")
	}

	packet := &QueryPacket{}
	packet.Command = data[0]
	packet.Query = string(data[1:])

	return packet, nil
}
