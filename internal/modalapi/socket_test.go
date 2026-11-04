//go:build darwin || linux

package modalapi

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A slow block receiver must push back on the uploader instead of growing
// each connection's kernel queue to megabytes. Test the actual HTTP/TLS
// upload path after it has sent enough data to trigger TCP autotuning.
func TestBlockUploadBoundsSocketMemory(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		name := "http"
		if encrypted {
			name = "https"
		}
		t.Run(name, func(t *testing.T) {
			arrived := make(chan struct{})
			drain := make(chan struct{})
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(arrived)
				<-drain
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
				}
				_, _ = io.WriteString(w, "receipt")
			}))
			// Modal's receive window is not the fake's deliberately small
			// window. Advertise room before the TCP handshake so the sender
			// can queue a substantial part of a block before it is stalled.
			listenerFD, err := server.Listener.(*net.TCPListener).SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var listenErr error
			if err := listenerFD.Control(func(fd uintptr) { listenErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, 4<<20) }); err != nil {
				t.Fatal(err)
			}
			if listenErr != nil {
				t.Fatal(listenErr)
			}
			if encrypted {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			c, err := New(Config{ServerURL: "http://127.0.0.1:1"})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if encrypted {
				c.HTTP.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig
			}
			connections := make(chan net.Conn, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				select {
				case connections <- info.Conn:
				default:
				}
			}})
			done := make(chan error, 1)
			go func() {
				_, err := c.upload(ctx, server.URL, make([]byte, BlockSize))
				done <- err
			}()
			// Always release the receiver before closing the HTTP server,
			// including an assertion failure while inspecting the socket.
			defer func() {
				close(drain)
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			select {
			case <-arrived:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			conn := <-connections
			if tlsConn, ok := conn.(*tls.Conn); ok {
				conn = tlsConn.NetConn()
			}
			raw, err := conn.(*net.TCPConn).SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			// Poll during the stalled upload, not just at dial time: the
			// default buffer starts small and grows when writes fill it.
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				var size int
				var optErr error
				if err := raw.Control(func(fd uintptr) { size, optErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF) }); err != nil {
					t.Fatal(err)
				}
				if optErr != nil {
					t.Fatal(optErr)
				}
				// Linux reports twice the requested size for accounting.
				if size > 512<<10 {
					t.Fatalf("upload socket grew to %d bytes while the receiver stalled; budget is at most 512 KiB including Linux accounting", size)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
