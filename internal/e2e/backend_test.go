//go:build e2e && darwin

package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// The tests run against the fake by default. With MODALFS_E2E_PROFILE set to
// a profile in ~/.modal.toml, they run against real Modal instead, each in a
// scratch volume of its own that's deleted afterwards:
//
//	MODALFS_E2E_PROFILE=<profile> go test -tags e2e ./internal/e2e    (or: just test-e2e-live <profile>)
//
// On real Modal, the network knobs and byte counts come from a proxy between
// modalfs and Modal (see proxy below), so the same checks hold.

// network counts what modalfs sent to and got from "Modal".
type network interface {
	Received() int64 // bytes uploaded by modalfs
	Served() int64   // bytes downloaded by modalfs
}

// backend is where a test's volume lives and how `modalfs run` reaches it.
type backend struct {
	net    network
	client *modalapi.Client
	vol    modalapi.VolumeInfo
	flags  []string // for modalfs run, before the volume name
	env    []string // for modalfs run
}

// live reports whether the tests run against real Modal.
func live() bool { return os.Getenv("MODALFS_E2E_PROFILE") != "" }

func startBackend(t *testing.T, root string, opt modalfake.Options) backend {
	if live() {
		return startLive(t, os.Getenv("MODALFS_E2E_PROFILE"), opt)
	}
	fake, err := modalfake.Start(filepath.Join(root, "blocks"), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	client, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	vol, err := client.LookupVolume(context.Background(), "e2e", true)
	if err != nil {
		t.Fatal(err)
	}
	return backend{
		net: fake, client: client, vol: vol,
		env: []string{"MODAL_SERVER_URL=" + fake.URL, "MODAL_TOKEN_ID=ak-fake", "MODAL_TOKEN_SECRET=as-fake", "MODAL_CONFIG_PATH=/dev/null"},
	}
}

// startLive creates a scratch volume for the test and deletes it when the
// test ends (after modalfs has exited: cleanups run last-in, first-out).
func startLive(t *testing.T, profile string, opt modalfake.Options) backend {
	cfg, err := modalapi.LoadConfig(profile)
	if err != nil {
		t.Fatal(err)
	}
	client, err := modalapi.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	short := strings.ToLower(strings.TrimPrefix(t.Name(), "Test"))
	name := fmt.Sprintf("modalfs-e2e-%s-%d", short, time.Now().Unix())
	vol, err := client.LookupVolume(ctx, name, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scratch volume %s (%s) in profile %s", name, vol.ID, profile)
	t.Cleanup(func() {
		if err := client.DeleteVolume(ctx, vol.ID); err != nil {
			t.Errorf("couldn't delete the scratch volume %s (%s): %v", name, vol.ID, err)
			return
		}
		if _, err := client.LookupVolume(ctx, name, false); err == nil {
			t.Errorf("the scratch volume %s (%s) is still there after deleting it", name, vol.ID)
		}
	})
	if opt.Latency > 0 {
		t.Logf("real Modal: ignoring the added latency of %v", opt.Latency)
	}
	p := startProxy(t, opt.UpBytesPerSec, opt.DownBytesPerSec)
	return backend{
		net: p, client: client, vol: vol,
		flags: []string{"-profile", profile},
		env:   []string{"HTTPS_PROXY=http://" + p.ln.Addr().String(), "NO_PROXY="},
	}
}

// proxy is an HTTPS (CONNECT) proxy between modalfs and real Modal. Both
// the gRPC connection and the block transfers go through it, so it can
// count the bytes each way and limit them like one slow network link, as
// the fake does by itself.
type proxy struct {
	ln       net.Listener
	up, down limiter
	sent     atomic.Int64
	recv     atomic.Int64
}

func startProxy(t *testing.T, up, down int64) *proxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{ln: ln, up: limiter{rate: up}, down: limiter{rate: down}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(c)
		}
	}()
	return p
}

func (p *proxy) Received() int64 { return p.sent.Load() }
func (p *proxy) Served() int64   { return p.recv.Load() }

func (p *proxy) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		return
	}
	up, err := net.DialTimeout("tcp", req.Host, 15*time.Second)
	if err != nil {
		fmt.Fprint(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer up.Close()
	fmt.Fprint(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		pump(up, br, &p.sent, &p.up)
		_ = up.(*net.TCPConn).CloseWrite()
	}()
	go func() {
		defer wg.Done()
		pump(c, up, &p.recv, &p.down)
		_ = c.(*net.TCPConn).CloseWrite()
	}()
	wg.Wait()
}

func pump(dst io.Writer, src io.Reader, n *atomic.Int64, l *limiter) {
	buf := make([]byte, 32<<10)
	for {
		k, err := src.Read(buf)
		if k > 0 {
			l.wait(k)
			if _, err := dst.Write(buf[:k]); err != nil {
				return
			}
			n.Add(int64(k))
		}
		if err != nil {
			return
		}
	}
}

// limiter spaces out transfers to rate bytes a second, shared by all the
// connections going one way. 0 means unlimited.
type limiter struct {
	rate int64
	mu   sync.Mutex
	next time.Time
}

func (l *limiter) wait(n int) {
	if l.rate <= 0 {
		return
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	at := l.next
	l.next = l.next.Add(time.Duration(float64(n) / float64(l.rate) * float64(time.Second)))
	l.mu.Unlock()
	time.Sleep(time.Until(at))
}
