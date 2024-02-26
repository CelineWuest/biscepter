package server

import (
	"fmt"

	"github.com/DominicWuest/biscepter/pkg/biscepter"
)

type ServerType int

const (
	Websocket ServerType = iota
	HTTP
)

type Server interface {
	init(int, chan biscepter.RunningSystem, chan biscepter.OffendingCommit) error
}

func NewServer(serverType ServerType, port int, rsChan chan biscepter.RunningSystem, ocChan chan biscepter.OffendingCommit) error {
	switch serverType {
	case Websocket:
		server := &websocketServer{}
		return server.init(port, rsChan, ocChan)
	case HTTP:
		server := &httpServer{}
		return server.init(port, rsChan, ocChan)
	}
	return fmt.Errorf("%d is not a valid server type", serverType)
}