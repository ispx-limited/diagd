//go:build linux

package tr143

import (
	"net"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// enableRxTimestamps turns on kernel receive timestamps (SO_TIMESTAMPNS) and
// receive-queue overflow accounting (SO_RXQ_OVFL) so echo timestamps reflect
// kernel packet arrival rather than goroutine scheduling, and socket drops
// feed TestRespReplyFailureCount.
func enableRxTimestamps(conn *net.UDPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TIMESTAMPNS, 1)
		if serr != nil {
			return
		}
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RXQ_OVFL, 1)
	})
	if err != nil {
		return err
	}
	return serr
}

// parseRxControl extracts the kernel receive timestamp and the cumulative
// socket drop counter from received control messages.
func parseRxControl(oob []byte) (rxTime time.Time, drops uint32, ok bool) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return time.Time{}, 0, false
	}
	for _, m := range msgs {
		if m.Header.Level != unix.SOL_SOCKET {
			continue
		}
		switch m.Header.Type {
		case unix.SCM_TIMESTAMPNS:
			if len(m.Data) >= int(unsafe.Sizeof(unix.Timespec{})) {
				ts := *(*unix.Timespec)(unsafe.Pointer(&m.Data[0]))
				rxTime = time.Unix(ts.Unix())
			}
		case unix.SO_RXQ_OVFL:
			if len(m.Data) >= 4 {
				drops = *(*uint32)(unsafe.Pointer(&m.Data[0]))
			}
		}
	}
	return rxTime, drops, true
}
