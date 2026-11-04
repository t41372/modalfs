//go:build e2e

package e2e

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/daemon"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// TestDaemonLargeNativeCopies exercises the app's actual data path: the
// daemon, a shared bounded cache, native copy processes, and directory/API
// readers running throughout. Each source block is different, so neither
// compression nor content deduplication can make this a small-file test.
func TestDaemonLargeNativeCopies(t *testing.T) {
	size := largeWriteSize(t, "MODALFS_E2E_NATIVE_GIB", 3)
	root, b, p := setupWriteDaemon(t, size)

	type source struct {
		name string
		size int64
		hash string
	}
	sources := []source{{name: "large.bin", size: size}, {name: "medium.bin", size: 512 * MiB}, {name: "small.bin", size: 64*MiB + 123}}
	for i := range sources {
		s := &sources[i]
		f, err := os.Create(filepath.Join(root, s.name))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		// A new seed on every run also defeats cross-volume deduplication
		// by the real block store, including when scratch volumes are reused.
		var seed [32]byte
		if _, err := cryptorand.Read(seed[:]); err != nil {
			f.Close()
			t.Fatal(err)
		}
		_, err = io.CopyN(io.MultiWriter(f, h), rand.NewChaCha8(seed), s.size)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("prepare source: %v; close: %v", err, closeErr)
		}
		s.hash = hex.EncodeToString(h.Sum(nil))
	}
	t.Log("sources ready; starting concurrent native copies with a 2 GiB cache")
	ctx, cancel := context.WithTimeout(context.Background(), largeWriteBudget(size))
	defer cancel()
	stop := probeDaemonCopy(t, p, cancel)
	defer stop()
	results := make(chan error, len(sources))
	start := time.Now()
	for _, s := range sources {
		go func() {
			cmd := exec.CommandContext(ctx, "/bin/cp", filepath.Join(root, s.name), filepath.Join(p.mnt, s.name))
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("copy %s: %w: %s", s.name, err, out)
			}
			results <- err
		}()
	}
	copyFailed := false
	for range sources {
		if err := <-results; err != nil {
			copyFailed = true
			t.Error(err)
		}
	}
	t.Logf("native copies returned after %v", time.Since(start).Round(time.Millisecond))
	if copyFailed {
		return
	}
	for _, s := range sources {
		waitRemoteHash(t, b, s.name, s.size, s.hash)
	}
	stop()
	if held := (&env{logs: p.logs}).heldUp(); len(held) != 0 {
		t.Errorf("server held metadata requests during copies:\n%s", strings.Join(held, "\n"))
	}
	t.Log("all remote file sizes and SHA-256 hashes match the local sources")
}

// probeDaemonCopy checks all three surfaces. A fast error is a failure too:
// measuring latency alone would accept an unavailable mount or server.
func probeDaemonCopy(t *testing.T, p *daemonProc, failed context.CancelFunc) func() {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(p.home, "daemon.json"))
	if err != nil {
		t.Fatal(err)
	}
	var info daemon.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(info.UIURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", info.Socket)
	}}
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, surface := range []string{"directory", "unix", "tcp"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var worst time.Duration
			lastReport := time.Now()
			defer func() { t.Logf("slowest %s probe: %v", surface, worst) }()
			limit := maxStall
			if surface == "directory" {
				// Native ls also stats files. The kernel can flush their
				// pending writes before issuing GETATTR, as in writeTimed.
				limit = maxHeld
			}
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				start := time.Now()
				var err error
				switch surface {
				case "directory":
					call, done := context.WithTimeout(ctx, limit+5*time.Second)
					err = exec.CommandContext(call, "/bin/ls", p.mnt).Run()
					done()
				case "unix":
					var resp *http.Response
					resp, err = client.Get("http://modalfsd/api/state")
					if err == nil {
						var state daemon.StateView
						err = json.NewDecoder(resp.Body).Decode(&state)
						resp.Body.Close()
						if resp.StatusCode != http.StatusOK {
							err = fmt.Errorf("HTTP %d", resp.StatusCode)
						}
						if err == nil && time.Since(lastReport) >= 30*time.Second {
							for _, mount := range state.Mounts {
								if s := mount.Status; s != nil {
									t.Logf("upload progress: %d MiB sent, %d MiB pending, %d commits", s.BytesUp/MiB, s.PendingBytes/MiB, s.Commits)
								}
							}
							lastReport = time.Now()
						}
					}
				case "tcp":
					var conn net.Conn
					conn, err = net.DialTimeout("tcp", u.Host, 2*time.Second)
					if err == nil {
						conn.Close()
					}
				}
				if ctx.Err() != nil {
					return
				}
				elapsed := time.Since(start)
				worst = max(worst, elapsed)
				if err != nil || elapsed > limit {
					t.Errorf("%s probe: %v after %v", surface, err, elapsed)
					failed()
					return
				}
			}
		}()
	}
	return func() { cancel(); wg.Wait(); transport.CloseIdleConnections() }
}

// Read every committed block independently of the mount and its caches,
// keeping memory bounded regardless of the source's size.
func waitRemoteHash(t *testing.T, b backend, name string, size int64, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), max(5*time.Minute, time.Duration(size>>30)*time.Minute))
	defer cancel()
	for {
		_, got, err := b.client.BlockURLs(ctx, b.vol.ID, name, 0, 1)
		if err == nil && got == size {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s: size %d, want %d; %v", name, got, size, err)
		case <-time.After(time.Second):
		}
	}
	h := sha256.New()
	blockCount := int((size + modalapi.BlockSize - 1) / modalapi.BlockSize)
	var urls []string
	// Four downloads hide WAN round trips without loading the whole file
	// into RAM. Hash in file order, with at most 32 MiB resident here.
	for first := 0; first < blockCount; first += 4 {
		// Refresh URLs every 2 GiB: a 100 GiB verification must not rely
		// on URLs issued at its start still being valid at the end.
		if first%256 == 0 {
			count := min(256, blockCount-first)
			var got int64
			var err error
			urls, got, err = b.client.BlockURLs(ctx, b.vol.ID, name, int64(first), int64(count))
			if err != nil || got != size || len(urls) != count {
				t.Fatalf("%s block %d: size %d, URLs %d (want %d): %v", name, first, got, len(urls), count, err)
			}
		}
		count := min(4, blockCount-first)
		blocks := make([][]byte, count)
		errs := make([]error, count)
		var wg sync.WaitGroup
		for j := range count {
			wg.Add(1)
			go func() {
				defer wg.Done()
				i := first + j
				off := int64(i) * modalapi.BlockSize
				blocks[j], errs[j] = b.client.Download(ctx, urls[i%256], int(min(modalapi.BlockSize, size-off)))
			}()
		}
		wg.Wait()
		for j, data := range blocks {
			if errs[j] != nil {
				t.Fatalf("verify %s block %d/%d: %v", name, first+j, blockCount, errs[j])
			}
			h.Write(data)
		}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		t.Fatalf("%s remote SHA-256 %s, want %s", name, got, want)
	}
	t.Logf("verified %s: %d bytes, SHA-256 %s", name, size, want)
}

// setupWriteDaemon uses the production daemon and a 2 GiB shared cache.
func setupWriteDaemon(t *testing.T, size int64) (string, backend, *daemonProc) {
	t.Helper()
	root, err := os.MkdirTemp("", "mfs-w-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	b := startBackend(t, root, modalfake.Options{UpBytesPerSec: 20 * MiB})
	profile := filepath.Join(root, "modal.toml")
	cfg := b.config
	configText := fmt.Sprintf("[test]\ntoken_id = %q\ntoken_secret = %q\nserver_url = %q\nenvironment = %q\nactive = true\n", cfg.TokenID, cfg.TokenSecret, cfg.ServerURL, cfg.Environment)
	if err := os.WriteFile(profile, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &daemonProc{t: t, bin: modalfsBinary(t), home: filepath.Join(root, "h"), mnt: filepath.Join(root, "mnt")}
	p.env = append(os.Environ(), "MODALFS_HOME="+p.home, "MODALFS_NO_KEYCHAIN=1", "MODAL_CONFIG_PATH="+profile,
		"MODAL_TOKEN_ID=", "MODAL_TOKEN_SECRET=", "MODAL_SERVER_URL=", "MODAL_PROFILE=")
	// Use direct real-Modal connections, as the app does. A loopback proxy
	// changes kernel socket buffering, which is part of this investigation.
	p.start()
	t.Cleanup(p.quit)
	watchForStall(t, p.cmd, p.logs, p.mnt, 2*largeWriteBudget(size)+5*time.Minute)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(p.logs.String())
		}
	})
	p.must("account", "import", "test")
	p.must("settings", "cache-size", "2")
	p.must("mount", b.vol.Name, p.mnt)

	disk := watchDisk(filepath.Join(p.home, "cache"))
	t.Cleanup(func() {
		most := disk()
		t.Logf("peak cache disk usage: %d MiB", most/MiB)
		if most > (2<<30)+64*MiB {
			t.Errorf("cache used %d MiB; budget is 2 GiB plus 64 MiB for in-flight writes", most/MiB)
		}
	})
	return root, b, p
}
