//go:build windows

package player

import (
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func dialPipe(path string) (net.Conn, error) {
	t := 5 * time.Second
	return winio.DialPipe(path, &t)
}
