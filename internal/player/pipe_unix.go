//go:build !windows

package player

import (
	"errors"
	"net"
)

func dialPipe(_ string) (net.Conn, error) {
	return nil, errors.New("named pipes are not available on this platform")
}
