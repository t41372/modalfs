package modalapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A body cut short of its Content-Length must be an error, not zero padding.
func TestDownloadTruncated(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			http.NotFound(w, r) // not retryable: ends the test quickly
			return
		}
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write(bytes.Repeat([]byte{1}, 50)) // connection closes early
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client()}
	if b, err := c.Download(context.Background(), srv.URL, 100); err == nil {
		t.Fatalf("truncated body accepted: %d bytes, %d non-zero", len(b), bytes.Count(b, []byte{1}))
	}
}

// A complete body shorter than the block is padded with zeros.
func TestDownloadPads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("abc"))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client()}
	b, err := c.Download(context.Background(), srv.URL, 8)
	if err != nil || !bytes.Equal(b, []byte("abc\x00\x00\x00\x00\x00")) {
		t.Fatalf("got %q, %v", b, err)
	}
}

// Out of reach: no network or a timeout, wrapped or not. Not an answer from
// Modal, nor an error of modalfs's own.
func TestIsUnreachable(t *testing.T) {
	for err, want := range map[error]bool{
		status.Error(codes.Unavailable, "name resolver error: produced zero addresses"):         true,
		fmt.Errorf("block 0: %w", status.Error(codes.Unavailable, "context deadline exceeded")): true,
		status.Error(codes.DeadlineExceeded, "waiting for connections to become ready"):         true,
		fmt.Errorf("reading: %w", context.DeadlineExceeded):                                     true,
		status.Error(codes.FailedPrecondition, "block GET 403: expired"):                        false,
		status.Error(codes.NotFound, "no such file"):                                            false,
		errors.New("encryption authentication failed: damaged or unsupported block header"):     false,
	} {
		if IsUnreachable(err) != want {
			t.Errorf("IsUnreachable(%v) = %v", err, !want)
		}
	}
}
