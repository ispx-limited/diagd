//go:build !linux

package tr143

import (
	"errors"
	"net"
	"time"
)

func enableRxTimestamps(*net.UDPConn) error {
	return errors.New("kernel receive timestamps are only supported on linux")
}

func parseRxControl([]byte) (time.Time, uint32, bool) {
	return time.Time{}, 0, false
}
