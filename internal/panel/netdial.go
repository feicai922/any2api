package panel

import (
	"net"
	"time"
)

func netDial(address string) (net.Conn, error) {
	return net.DialTimeout("tcp", address, 700*time.Millisecond)
}
