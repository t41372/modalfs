//go:build modalfs_warp

// modalfs-warp connects to Cloudflare WARP from inside a Modal Function and
// serves a SOCKS5 and HTTP CONNECT proxy on a unix socket, for the
// downloaders of a job that asked to go through WARP (app.py, class Warp).
//
// The official WARP client can't run in a Function (no TUN device, no
// CAP_NET_ADMIN), and neither can wireproxy or sing-box: gVisor rejects a
// socket option the standard WireGuard UDP socket sets. So this is
// wireguard-go over a plain UDP socket, with gVisor's userspace TCP stack
// (netstack) for the tunnel's side. docs/design/remote-downloads.md has the
// measurements.
//
// It's built from source in the Function's image (app.py), never into
// modalfs itself: the build tag keeps it out of the main module's builds.
//
//	modalfs-warp -profile wgcf-profile.conf -listen /run/modalfs-warp/proxy.sock -group 999
//
// Once the tunnel carries traffic it prints one line of JSON to stdout,
// {"ready":true,"warp":"on","colo":"IAD","ip":"…"}, or {"ready":false,
// "error":"…"} and exits 1 when it can't.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

var (
	profile  = flag.String("profile", "wgcf-profile.conf", "the WireGuard profile wgcf generated")
	listen   = flag.String("listen", "", "the unix socket to serve the proxy on")
	group    = flag.Int("group", -1, "the group that may connect to the socket (-1: only the owner)")
	endpoint = flag.String("endpoint", "", "WARP endpoints to try in turn (host:port, comma-separated); the profile's, then a few of Cloudflare's other addresses and ports, when empty")
	mtu      = flag.Int("mtu", 1280, "the tunnel's MTU (1280 works; 1200 moved nothing in the spike)")
	bufSize  = flag.Int("buf", 8<<20, "the UDP socket's buffer size")
	checkURL = flag.String("check", "https://www.cloudflare.com/cdn-cgi/trace", "what's fetched through the tunnel to call it ready")
	verbose  = flag.Bool("v", false, "WireGuard's own log")
)

// Other ways in to WARP: from some Modal hosts the profile's endpoint
// never answered (a tunnel that didn't come up in 1 of 9 live runs).
var moreEndpoints = []string{"162.159.193.1:2408", "162.159.192.1:500", "162.159.192.1:4500"}

func main() {
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("modalfs-warp: ")
	p, err := readProfile(*profile)
	if err != nil {
		fail(err)
	}
	endpoints := []string{p.endpoint}
	endpoints = append(endpoints, moreEndpoints...)
	if *endpoint != "" {
		endpoints = strings.Split(*endpoint, ",")
	}
	var errs []string
	for _, ep := range endpoints {
		tnet, dev, err := connect(p, strings.TrimSpace(ep))
		var info map[string]string
		if err == nil {
			if info, err = check(tnet); err != nil {
				dev.Close()
			}
		}
		if err != nil {
			log.Printf("%s: %v", ep, err)
			errs = append(errs, fmt.Sprintf("%s: %v", ep, err))
			continue
		}
		ln, err := listenUnix(*listen, *group)
		if err != nil {
			fail(err)
		}
		out := map[string]any{"ready": true, "endpoint": ep}
		for k, v := range info {
			out[k] = v
		}
		say(out)
		serve(ln, tnet)
		return
	}
	fail(errors.New(strings.Join(errs, "; ")))
}

func say(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func fail(err error) {
	say(map[string]any{"ready": false, "error": err.Error()})
	os.Exit(1)
}

// connect brings the tunnel up through one endpoint.
func connect(p wgProfile, ep string) (*netstack.Net, *device.Device, error) {
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return nil, nil, fmt.Errorf("the WARP endpoint %q: %w", ep, err)
	}
	// IPv4: the Function's network has no IPv6 route.
	ips, err := net.DefaultResolver.LookupIP(context.Background(), "ip4", host)
	if err != nil || len(ips) == 0 {
		return nil, nil, fmt.Errorf("couldn't look up the WARP endpoint %s: %v", host, err)
	}
	tun, tnet, err := netstack.CreateNetTUN(p.addresses, []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")}, *mtu)
	if err != nil {
		return nil, nil, err
	}
	level := device.LogLevelError
	if *verbose {
		level = device.LogLevelVerbose
	}
	dev := device.NewDevice(tun, &plainBind{}, device.NewLogger(level, "wireguard: "))
	cfg := fmt.Sprintf("private_key=%s\npublic_key=%s\nendpoint=%s\nallowed_ip=0.0.0.0/0\nallowed_ip=::/0\npersistent_keepalive_interval=25\n",
		p.privateKey, p.publicKey, net.JoinHostPort(ips[0].String(), port))
	if err := dev.IpcSet(cfg); err != nil {
		dev.Close()
		return nil, nil, err
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, nil, err
	}
	log.Printf("tunnel up, endpoint %s (%s)", ep, ips[0])
	return tnet, dev, nil
}

type wgProfile struct {
	privateKey, publicKey, endpoint string // keys in hex, as WireGuard's UAPI wants them
	addresses                       []netip.Addr
}

func readProfile(path string) (wgProfile, error) {
	var p wgProfile
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "PrivateKey", "PublicKey":
			raw, err := base64.StdEncoding.DecodeString(v)
			if err != nil || len(raw) != 32 {
				return p, fmt.Errorf("%s: a bad %s", path, k)
			}
			if k == "PrivateKey" {
				p.privateKey = hex.EncodeToString(raw)
			} else {
				p.publicKey = hex.EncodeToString(raw)
			}
		case "Address":
			for _, a := range strings.Split(v, ",") {
				if pr, err := netip.ParsePrefix(strings.TrimSpace(a)); err == nil {
					p.addresses = append(p.addresses, pr.Addr())
				}
			}
		case "Endpoint":
			p.endpoint = v
		}
	}
	if p.privateKey == "" || p.publicKey == "" || p.endpoint == "" || len(p.addresses) == 0 {
		return p, fmt.Errorf("%s isn't a complete WireGuard profile", path)
	}
	return p, sc.Err()
}

// check fetches the trace page through the tunnel until it answers, and
// returns what it says (warp, colo, ip).
func check(tnet *netstack.Net) (map[string]string, error) {
	client := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DialContext: tnet.DialContext},
	}
	var last error
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		resp, err := client.Get(*checkURL)
		if err != nil {
			last = err
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		info := map[string]string{}
		for _, line := range strings.Split(string(body), "\n") {
			if k, v, ok := strings.Cut(line, "="); ok && (k == "warp" || k == "colo" || k == "ip" || k == "loc") {
				info[k] = strings.TrimSpace(v)
			}
		}
		if info["warp"] != "on" && info["warp"] != "plus" {
			return nil, fmt.Errorf("the tunnel works, but Cloudflare says warp=%q", info["warp"])
		}
		return info, nil
	}
	return nil, fmt.Errorf("nothing came back through the tunnel in 20 s (last: %v)", last)
}

func listenUnix(path string, gid int) (net.Listener, error) {
	if path == "" {
		return nil, errors.New("-listen is required")
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	mode := os.FileMode(0o600)
	if gid >= 0 {
		if err := os.Chown(path, -1, gid); err != nil {
			return nil, err
		}
		mode = 0o660
	}
	return ln, os.Chmod(path, mode)
}

// serve answers SOCKS5 (first byte 5) and HTTP CONNECT on the same socket:
// gopeed and yt-dlp speak the one, aria2 only the other.
func serve(ln net.Listener, tnet *netstack.Net) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() {
			defer c.Close()
			r := bufio.NewReader(c)
			first, err := r.Peek(1)
			if err != nil {
				return
			}
			if first[0] == 5 {
				socks(c, r, tnet)
			} else {
				httpConnect(c, r, tnet)
			}
		}()
	}
}

func dial(tnet *netstack.Net, target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return tnet.DialContext(ctx, "tcp", target)
}

func socks(c net.Conn, r *bufio.Reader, tnet *netstack.Net) {
	hello := make([]byte, 2)
	if _, err := io.ReadFull(r, hello); err != nil {
		return
	}
	if _, err := io.ReadFull(r, make([]byte, hello[1])); err != nil {
		return
	}
	_, _ = c.Write([]byte{5, 0}) // no authentication: only the job's downloader can reach the socket
	req := make([]byte, 4)
	if _, err := io.ReadFull(r, req); err != nil {
		return
	}
	reply := func(code byte) { _, _ = c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0}) }
	if req[1] != 1 { // CONNECT only
		reply(7)
		return
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(r, b); err != nil {
			return
		}
		host = net.IP(b).String()
	case 3:
		n, err := r.ReadByte()
		if err != nil {
			return
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			return
		}
		host = string(b)
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(r, b); err != nil {
			return
		}
		host = net.IP(b).String()
	default:
		reply(8)
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(r, pb); err != nil {
		return
	}
	up, err := dial(tnet, net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))))
	if err != nil {
		reply(5)
		return
	}
	reply(0)
	pipe(c, r, up)
}

func httpConnect(c net.Conn, r *bufio.Reader, tnet *netstack.Net) {
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	up, err := dial(tnet, req.Host)
	if err != nil {
		_, _ = fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	pipe(c, r, up)
}

// pipe copies both ways until both sides are done; r holds whatever the
// client sent after its request.
func pipe(c net.Conn, r io.Reader, up net.Conn) {
	defer up.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(up, r)
		if cw, ok := up.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(c, up)
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	<-done
}

// plainBind is an IPv4 conn.Bind over an ordinary UDP socket: the standard
// one sets IP_PKTINFO and GSO options gVisor rejects ("Unable to update
// bind: operation not supported").
type plainBind struct {
	mu sync.Mutex
	c  *net.UDPConn
}

func (b *plainBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: int(port)})
	if err != nil {
		return nil, 0, err
	}
	// Bigger buffers than the default: with them, bulk transfers stopped
	// stalling at 18 KB/s in the spike.
	if err := c.SetReadBuffer(*bufSize); err != nil {
		log.Printf("UDP read buffer: %v", err)
	}
	if err := c.SetWriteBuffer(*bufSize); err != nil {
		log.Printf("UDP write buffer: %v", err)
	}
	b.mu.Lock()
	b.c = c
	b.mu.Unlock()
	recv := func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, ap, err := c.ReadFromUDPAddrPort(packets[0])
		if err != nil {
			return 0, err
		}
		sizes[0] = n
		eps[0] = &conn.StdNetEndpoint{AddrPort: netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())}
		return 1, nil
	}
	return []conn.ReceiveFunc{recv}, uint16(c.LocalAddr().(*net.UDPAddr).Port), nil
}

func (b *plainBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c != nil {
		return b.c.Close()
	}
	return nil
}

func (b *plainBind) SetMark(uint32) error { return nil }
func (b *plainBind) BatchSize() int       { return 1 }

func (b *plainBind) Send(bufs [][]byte, ep conn.Endpoint) error {
	b.mu.Lock()
	c := b.c
	b.mu.Unlock()
	if c == nil {
		return net.ErrClosed
	}
	ap := ep.(*conn.StdNetEndpoint).AddrPort
	for _, buf := range bufs {
		if _, err := c.WriteToUDPAddrPort(buf, ap); err != nil {
			return err
		}
	}
	return nil
}

func (b *plainBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &conn.StdNetEndpoint{AddrPort: ap}, nil
}
