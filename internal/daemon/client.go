package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/t41372/modalfs/internal/config"
)

// Client talks to a running daemon over its Unix socket.
type Client struct{ hc *http.Client }

func NewClient() *Client {
	return &Client{hc: &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dl net.Dialer
			return dl.DialContext(ctx, "unix", SocketPath())
		}},
	}}
}

// Transport exposes the socket transport (the GUI proxies through it).
func (c *Client) Transport() http.RoundTripper { return c.hc.Transport }

// Alive asks /api/ping, which takes no locks: /api/state can wait on a long
// commit, and a busy daemon mistaken for a dead one would get a twin.
func (c *Client) Alive() bool {
	hc := *c.hc
	hc.Timeout = 2 * time.Second
	resp, err := hc.Get("http://modalfsd/api/ping")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// Ensure starts a background daemon if none is running.
func (c *Client) Ensure() error {
	if c.Alive() {
		return nil
	}
	exe, err := selfExe()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	logf, err := os.OpenFile(LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	if fi, err := logf.Stat(); err == nil && fi.Size() > 20<<20 {
		_ = logf.Truncate(0)
	}
	cmd := exec.Command(exe, "daemon")
	cmd.Dir = "/" // don't keep the caller's folder (or its volume) busy
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it if it exits while we run
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		if c.Alive() {
			return nil
		}
	}
	return fmt.Errorf("daemon did not start; see %s", LogPath())
}

func (c *Client) Do(method, path string, in, out any) error {
	return c.do(c.hc, method, path, in, out)
}

// DoLong is Do without the time limit, for calls that wait on uploads
// (unmount), which can take as long as the data needs.
func (c *Client) DoLong(method, path string, in, out any) error {
	hc := *c.hc
	hc.Timeout = 0
	return c.do(&hc, method, path, in, out)
}

func (c *Client) do(hc *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, "http://modalfsd"+path, body)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
