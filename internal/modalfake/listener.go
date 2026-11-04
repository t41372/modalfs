package modalfake

import (
	"fmt"
	"log"
	"net"
)

// A slow fake must apply backpressure to its clients instead of retaining
// megabytes per connection in kernel socket buffers. On macOS, exhausting
// mbufs makes the kernel defunct every socket of the largest consumer,
// including its listeners, without closing their descriptors.
const socketBufferBytes = 256 << 10

type boundedListener struct{ net.Listener }

// Configuration failed for this accepted socket, not for the listener.
// HTTP and gRPC retry temporary Accept errors with exponential backoff.
type socketBufferError struct{ cause error }

func (e *socketBufferError) Error() string {
	return fmt.Sprintf("setting fake Modal socket buffers: %v", e.cause)
}
func (e *socketBufferError) Unwrap() error   { return e.cause }
func (e *socketBufferError) Timeout() bool   { return false }
func (e *socketBufferError) Temporary() bool { return true }

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
		err = &socketBufferError{cause: err}
		// gRPC's temporary-Accept diagnostic is only a trace event by
		// default, so report every rejected socket here as well.
		log.Printf("fake Modal rejected connection: %v", err)
		return nil, err
	}
	return conn, nil
}
