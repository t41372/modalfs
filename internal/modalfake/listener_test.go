package modalfake

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type injectedListener struct {
	net.Listener
	first net.Conn
	err   error
}

func (l *injectedListener) Accept() (net.Conn, error) {
	if l.first != nil {
		conn := l.first
		l.first = nil
		return conn, nil
	}
	if l.err != nil {
		return nil, l.err
	}
	return l.Listener.Accept()
}

func closedTCPConnection(t *testing.T) net.Conn {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestBoundedListenerKeepsAcceptError(t *testing.T) {
	want := errors.New("underlying accept failure")
	l := boundedListener{&injectedListener{err: want}}
	if conn, err := l.Accept(); conn != nil || err != want {
		t.Fatalf("underlying accept error changed: conn=%v err=%v", conn, err)
	}
}

func TestBoundedListenerRejectsOnlyFailedConnection(t *testing.T) {
	l := boundedListener{&injectedListener{first: closedTCPConnection(t)}}
	conn, err := l.Accept()
	if conn != nil || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("failed socket was accepted or its error lost: conn=%v err=%v", conn, err)
	}
	var retry net.Error
	if !errors.As(err, &retry) || !retry.Temporary() || retry.Timeout() {
		t.Fatalf("connection setup failure must trigger server backoff: %v", err)
	}
}

// Both real server loops must survive one failed accepted connection and
// continue to serve the next socket with the required buffer bounds.
func TestServersRetrySocketBufferFailure(t *testing.T) {
	for _, protocol := range []string{"http", "grpc"} {
		t.Run(protocol, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			wrapped := boundedListener{&injectedListener{Listener: listener, first: closedTCPConnection(t)}}
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if protocol == "http" {
				server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "healthy") })}
				go func() { done <- server.Serve(wrapped) }()
				defer func() { _ = server.Close(); <-done }()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String(), nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("HTTP stopped after a rejected socket: %v", err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || string(body) != "healthy" {
					t.Fatalf("healthy HTTP connection failed: %q %v", body, err)
				}
			} else {
				server := grpc.NewServer()
				pb.RegisterModalClientServer(server, &Server{})
				go func() { done <- server.Serve(wrapped) }()
				defer func() { server.Stop(); <-done }()
				client, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				response, err := pb.NewModalClientClient(client).TokenInfoGet(ctx, pb.TokenInfoGetRequest_builder{}.Build())
				if err != nil || response.GetTokenId() != "ak-fake" {
					t.Fatalf("gRPC stopped after a rejected socket: response=%v err=%v", response, err)
				}
			}
		})
	}
}
