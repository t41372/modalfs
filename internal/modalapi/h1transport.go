package modalapi

// h1transport implements grpc.ClientConnInterface on top of plain HTTP/1.1.
//
// Normal deployments use real gRPC over HTTP/2 (see dialGRPC). This transport
// exists for environments where only an HTTP/1.1 forward proxy is available
// (e.g. a TLS-intercepting corporate proxy). Modal's edge (envoy) accepts
// application/grpc over HTTP/1.1 as long as `te: trailers` is present, and
// returns grpc-status either as headers (trailers-only) or as HTTP trailers.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type h1Conn struct {
	base    string // https://api.modal.com
	hc      *http.Client
	headers func() map[string]string
}

func (c *h1Conn) roundTrip(ctx context.Context, method string, req proto.Message) ([][]byte, error) {
	b, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	frame := make([]byte, 5+len(b))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(b)))
	copy(frame[5:], b)
	hr, err := http.NewRequestWithContext(ctx, "POST", c.base+method, bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("content-type", "application/grpc")
	hr.Header.Set("te", "trailers")
	for k, v := range c.headers() {
		hr.Header.Set(k, v)
	}
	if md, ok := metadata.FromOutgoingContext(ctx); ok {
		for k, vs := range md {
			if len(vs) > 0 {
				hr.Header.Set(k, vs[0])
			}
		}
	}
	resp, err := c.hc.Do(hr)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	st := resp.Header.Get("grpc-status")
	msg := resp.Header.Get("grpc-message")
	if st == "" {
		st = resp.Trailer.Get("grpc-status")
		msg = resp.Trailer.Get("grpc-message")
	}
	if resp.StatusCode != 200 && st == "" {
		return nil, status.Errorf(codes.Unavailable, "http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if st != "" && st != "0" {
		n, _ := strconv.Atoi(st)
		return nil, status.Error(codes.Code(n), msg)
	}
	var frames [][]byte
	for len(body) >= 5 {
		n := int(binary.BigEndian.Uint32(body[1:5]))
		if len(body) < 5+n {
			return nil, status.Error(codes.Internal, "short grpc frame")
		}
		frames = append(frames, body[5:5+n])
		body = body[5+n:]
	}
	return frames, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (c *h1Conn) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	frames, err := c.roundTrip(ctx, method, args.(proto.Message))
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return status.Error(codes.Internal, "empty grpc response")
	}
	return proto.Unmarshal(frames[0], reply.(proto.Message))
}

func (c *h1Conn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
	if desc.ClientStreams {
		return nil, fmt.Errorf("client streaming not supported over HTTP/1.1 transport (%s)", method)
	}
	return &h1Stream{c: c, ctx: ctx, method: method}, nil
}

type h1Stream struct {
	c      *h1Conn
	ctx    context.Context
	method string
	req    proto.Message
	frames [][]byte
	done   bool
	err    error
}

func (s *h1Stream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *h1Stream) Trailer() metadata.MD         { return metadata.MD{} }
func (s *h1Stream) Context() context.Context     { return s.ctx }
func (s *h1Stream) SendMsg(m any) error          { s.req = m.(proto.Message); return nil }
func (s *h1Stream) CloseSend() error {
	s.frames, s.err = s.c.roundTrip(s.ctx, s.method, s.req)
	s.done = true
	return nil
}
func (s *h1Stream) RecvMsg(m any) error {
	if !s.done {
		_ = s.CloseSend()
	}
	if s.err != nil {
		return s.err
	}
	if len(s.frames) == 0 {
		return io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return proto.Unmarshal(f, m.(proto.Message))
}

var _ = strings.TrimSpace
