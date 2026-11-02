package modalfake

import (
	"fmt"
	"net"
)

// A slow fake must apply backpressure to its clients instead of retaining
// megabytes per connection in kernel socket buffers. On macOS, exhausting
// mbufs makes the kernel defunct every socket of the largest consumer,
// including its listeners, without closing their descriptors.
const socketBufferBytes = 256 << 10

type boundedListener struct{ net.Listener }

func (l boundedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tcp := conn.(*net.TCPConn) // Start creates TCP listeners.
	if err = tcp.SetReadBuffer(socketBufferBytes); err == nil {
		err = tcp.SetWriteBuffer(socketBufferBytes)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("setting fake Modal socket buffers: %w", err)
	}
	return conn, nil
}
