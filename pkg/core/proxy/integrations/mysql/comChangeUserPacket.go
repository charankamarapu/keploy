package mysql

import (
	"errors"
	"strings"
)

type ComChangeUserPacket struct {
	User         string `yaml:"user"`
	Auth         []byte `yaml:"auth,omitempty,flow"`
	Db           string `yaml:"db"`
	CharacterSet uint8  `yaml:"character_set"`
	AuthPlugin   string `yaml:"auth_plugin"`
}

func decodeComChangeUser(data []byte) (ComChangeUserPacket, error) {
	if len(data) < 2 {
		return ComChangeUserPacket{}, errors.New("Data too short for COM_CHANGE_USER")
	}

	nullTerminatedStrings := strings.Split(string(data[1:]), "\x00")
	if len(nullTerminatedStrings) < 5 {
		return ComChangeUserPacket{}, errors.New("Data malformed for COM_CHANGE_USER")
	}

	user := nullTerminatedStrings[0]
	authLength := data[len(user)+2]
	auth := data[len(user)+3 : len(user)+3+int(authLength)]
	db := nullTerminatedStrings[2]
	characterSet := data[len(user)+4+int(authLength)]
	authPlugin := nullTerminatedStrings[3]

	return ComChangeUserPacket{
		User:         user,
		Auth:         auth,
		Db:           db,
		CharacterSet: characterSet,
		AuthPlugin:   authPlugin,
	}, nil
}
