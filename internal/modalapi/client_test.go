package modalapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
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
