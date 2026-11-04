package modalapi

import (
	"context"
	"fmt"
	"net"
	"time"
)

// Uploads may use 32 TCP connections. Autotuning can grow each send queue
// to several MiB while the uplink drains it. On macOS that can exhaust
// kernel mbufs and defunct every socket in the daemon, including NFS and
// HTTP listeners. Bound queued payload to 8 MiB per client's 32 uploads;
// excess data stays in the existing upload buffers and waits for TCP.
const blockSendBuffer = 256 << 10

func dialBlockConnection(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	// net/http dials TCP, before wrapping the connection in TLS or making
	// a proxy CONNECT request. Setting SO_SNDBUF also disables send-buffer
	// autotuning, so later writes cannot silently raise this budget.
	if err := conn.(*net.TCPConn).SetWriteBuffer(blockSendBuffer); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("limit block upload socket buffer: %w", err)
	}
	return conn, nil
}
