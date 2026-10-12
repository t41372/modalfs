//go:build e2e && darwin

// End-to-end tests: a real macOS NFS mount of modalfs, backed by the fake
// Modal in internal/modalfake with a slow network, or by real Modal (see
// backend_test.go). They check what a user would see: the mount stays
// responsive while large files upload, and what lands on "Modal" is exactly
// what was written.
//
//	go test -tags e2e ./internal/e2e    (or: just test-e2e)
//
// modalfs runs as its own process (`modalfs run`, built with the race
// detector), as it does for users: a process must never do file I/O on an
// NFS mount that it serves itself, or the kernel and the Go runtime can wait
// on each other forever. The tests check results through Modal's API.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

const MiB = 1 << 20

// maxStall is the longest a listing or stat may take while the mount is busy.
// The macOS client gives up on the server after about a minute; users notice
// much sooner.
const maxStall = 2 * time.Second

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
)

// modalfsBinary builds cmd/modalfs with the race detector, once per run.
func modalfsBinary(t *testing.T) string {
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mfs-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binary = filepath.Join(dir, "modalfs")
		out, err := exec.Command("go", "build", "-race", "-o", binary, "../../cmd/modalfs").CombinedOutput()
		if err != nil {
			buildErr = &buildError{err, string(out)}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binary
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return e.err.Error() + ": " + e.out }

type env struct {
	t      *testing.T
	net    network
	client *modalapi.Client
	vol    modalapi.VolumeInfo
	mnt    string
}

// setup mounts a fresh volume with `modalfs run`: on the fake, or on real
// Modal with MODALFS_E2E_PROFILE (see backend_test.go). before, if set,
// runs before the mount, to put files on "Modal" first. args are extra
// flags for modalfs run.
func setup(t *testing.T, opt modalfake.Options, args []string, before ...func(*modalapi.Client, string)) *env {
	t.Helper()
	bin := modalfsBinary(t)
	// Short paths: macOS limits NFS mount paths.
	root, err := os.MkdirTemp("", "mfs-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	b := startBackend(t, root, opt)
	for _, f := range before {
		f(b.client, b.vol.ID)
	}
	mnt := filepath.Join(root, "mnt")
	flags := append([]string{"run", "-cache-dir", filepath.Join(root, "cache"), "-cache-size", "2G"}, b.flags...)
	cmd := exec.Command(bin, append(append(flags, args...), b.vol.Name, mnt)...)
	cmd.Env = append(os.Environ(), b.env...)
	var logs syncBuffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGINT) // commits what's pending, unmounts
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Minute):
			_ = exec.Command("/sbin/umount", "-f", mnt).Run()
			_ = cmd.Process.Kill()
			<-done
			t.Error("modalfs run didn't exit within 3 minutes of SIGINT")
		}
		out := logs.String()
		for _, line := range strings.Split(out, "\n") {
			if line != "" && !strings.Contains(line, "exclusive") {
				t.Log(line)
			}
		}
		if strings.Contains(out, "DATA RACE") || strings.Contains(out, "panic") {
			t.Error("modalfs reported a data race or a panic (see the log above)")
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for !mounted(mnt) {
		if time.Now().After(deadline) {
			t.Fatalf("not mounted after 30 s:\n%s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &env{t: t, net: b.net, client: b.client, vol: b.vol, mnt: mnt}
}

func mounted(mnt string) bool {
	out, _ := exec.Command("/sbin/mount").Output()
	return bytes.Contains(out, []byte(" "+mnt+" ")) || bytes.Contains(out, []byte("/private"+mnt+" "))
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// writeFile writes through the mount and makes sure it reached the server.
func (e *env) writeFile(name string, data []byte) {
	e.t.Helper()
	f, err := os.Create(filepath.Join(e.mnt, name))
	if err != nil {
		e.t.Fatal(err)
	}
	for off := 0; off < len(data); off += 4 * MiB {
		if _, err := f.Write(data[off:min(off+4*MiB, len(data))]); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := f.Sync(); err != nil {
		e.t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		e.t.Fatal(err)
	}
}

// remote reads a file straight from Modal, bypassing modalfs's caches.
func (e *env) remote(name string) ([]byte, error) {
	ctx := context.Background()
	urls, size, err := e.client.BlockURLs(ctx, e.vol.ID, name, 0, 1<<30)
	if err != nil {
		return nil, err
	}
	var out []byte
	for i, u := range urls {
		n := min(size-int64(i)*modalapi.BlockSize, modalapi.BlockSize)
		b, err := e.client.Download(ctx, u, int(n))
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// waitRemote waits until Modal has exactly want under name.
func (e *env) waitRemote(name string, want []byte, timeout time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got, err := e.remote(name)
		if err == nil && sum(got) == sum(want) {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%s on Modal: %d bytes (err %v), want %d bytes, after %v", name, len(got), err, len(want), timeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// waitUploading waits until a commit has sent at least one full block.
func (e *env) waitUploading(timeout time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for e.net.Received() < modalapi.BlockSize {
		if time.Now().After(deadline) {
			e.t.Fatalf("no upload started within %v", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// probe lists the mount's root and stats a file every 200 ms, and reports
// the slowest answer when stopped. That's what Finder does all the time.
func (e *env) probe(stat string) func() time.Duration {
	var worst atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(200 * time.Millisecond):
			}
			t0 := time.Now()
			_, _ = os.ReadDir(e.mnt)
			if stat != "" {
				_, _ = os.Stat(filepath.Join(e.mnt, stat))
			}
			if d := int64(time.Since(t0)); d > worst.Load() {
				worst.Store(d)
			}
		}
	}()
	return func() time.Duration {
		close(stop)
		wg.Wait()
		return time.Duration(worst.Load())
	}
}

// Copying a large file into the mount over a slow uplink: listings and
// stats keep answering while it uploads, and the upload is exact. This is
// the case that used to freeze the whole mount until macOS gave up on it.
func TestLargeCopyStaysResponsive(t *testing.T) {
	e := setup(t, modalfake.Options{UpBytesPerSec: 20 * MiB, Latency: 20 * time.Millisecond}, nil)
	data := randomBytes(t, 400*MiB)
	stop := e.probe("big.bin")
	e.writeFile("big.bin", data)
	e.waitRemote("big.bin", data, 3*time.Minute)
	worst := stop()
	t.Logf("slowest listing/stat while uploading: %v", worst)
	if worst > maxStall {
		t.Errorf("the mount stalled for %v while a file uploaded (limit %v)", worst, maxStall)
	}
}

// Writing to a file while its commit uploads: the commit sends one
// consistent version, and the next commit brings Modal up to date.
func TestWriteDuringCommit(t *testing.T) {
	e := setup(t, modalfake.Options{UpBytesPerSec: 10 * MiB}, nil)
	data := randomBytes(t, 160*MiB)
	e.writeFile("f.bin", data)
	e.waitUploading(30 * time.Second)

	// change a block the commit is sending, and grow the file
	f, err := os.OpenFile(filepath.Join(e.mnt, "f.bin"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	patch := randomBytes(t, 4096)
	if _, err := f.WriteAt(patch, 8*MiB+100); err != nil {
		t.Fatal(err)
	}
	more := randomBytes(t, 20*MiB+12345)
	if _, err := f.WriteAt(more, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	want := append(append([]byte{}, data...), more...)
	copy(want[8*MiB+100:], patch)

	// the local view is the new version at once
	local, err := os.ReadFile(filepath.Join(e.mnt, "f.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if sum(local) != sum(want) {
		t.Fatal("reading through the mount during the commit returned the wrong content")
	}
	e.waitRemote("f.bin", want, 3*time.Minute)
}

// Deleting a file while it uploads: it must not reappear on Modal.
func TestDeleteDuringCommit(t *testing.T) {
	e := setup(t, modalfake.Options{UpBytesPerSec: 10 * MiB}, nil)
	e.writeFile("gone.bin", randomBytes(t, 80*MiB))
	e.waitUploading(30 * time.Second)
	if err := os.Remove(filepath.Join(e.mnt, "gone.bin")); err != nil {
		t.Fatal(err)
	}
	// long enough for the interrupted upload to have finished, had it gone on
	time.Sleep(10 * time.Second)
	if _, err := e.remote("gone.bin"); err == nil {
		t.Fatal("a file deleted during its commit is on Modal")
	}
	if n := e.net.Received(); n >= 80*MiB {
		t.Errorf("the whole deleted file was uploaded anyway (%d MiB)", n/MiB)
	}
}

// Reading a large remote file over a slow downlink streams it: the first
// bytes come quickly, the content is exact, and the mount stays responsive.
func TestStreamingRead(t *testing.T) {
	data := randomBytes(t, 320*MiB)
	put := func(c *modalapi.Client, volID string) {
		_, err := c.Put(context.Background(), volID, []modalapi.PutFile{{
			Path: "movie.mov", Size: int64(len(data)), Hashes: modalapi.HashBlocks(data),
			Block: func(i int) ([]byte, error) {
				return data[i*modalapi.BlockSize : min((i+1)*modalapi.BlockSize, len(data))], nil
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	e := setup(t, modalfake.Options{DownBytesPerSec: 40 * MiB, Latency: 30 * time.Millisecond}, []string{"-read-ahead", "8"}, put)
	stop := e.probe("")
	f, err := os.Open(filepath.Join(e.mnt, "movie.mov"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	t0 := time.Now()
	first := make([]byte, 64<<10)
	if _, err := io.ReadFull(f, first); err != nil {
		t.Fatal(err)
	}
	ttfb := time.Since(t0)
	rest, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(t0)
	worst := stop()
	t.Logf("first 64 KiB after %v; %d MiB in %v (%.1f MiB/s); slowest listing %v", ttfb, len(data)/MiB, took, float64(len(data))/MiB/took.Seconds(), worst)
	if sum(append(first, rest...)) != sum(data) {
		t.Fatal("read the wrong content")
	}
	if ttfb > 3*time.Second {
		t.Errorf("first bytes took %v", ttfb)
	}
	if worst > maxStall {
		t.Errorf("the mount stalled for %v while a file was read (limit %v)", worst, maxStall)
	}
}

// Renaming a folder right after writing into it keeps the pending writes.
func TestRenameFolderWithPendingWrites(t *testing.T) {
	e := setup(t, modalfake.Options{}, nil)
	if err := os.Mkdir(filepath.Join(e.mnt, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := randomBytes(t, 3*MiB)
	e.writeFile("d/a.bin", data)
	if err := os.Rename(filepath.Join(e.mnt, "d"), filepath.Join(e.mnt, "d2")); err != nil {
		t.Fatal(err)
	}
	e.waitRemote("d2/a.bin", data, time.Minute)
	if _, err := e.remote("d/a.bin"); err == nil {
		t.Fatal("the old folder came back")
	}
}

// A file that keeps being written for longer than -max-pending is committed
// in parts while it's written, so a long copy is mostly uploaded by the time
// it ends instead of starting to upload then.
func TestUploadsWhileWriting(t *testing.T) {
	e := setup(t, modalfake.Options{}, []string{"-write-back", "2s", "-max-pending", "3s"})
	f, err := os.Create(filepath.Join(e.mnt, "long.bin"))
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for i := 0; i < 8; i++ { // ~8 s of writing, never idle for 2 s
		chunk := randomBytes(t, 8*MiB)
		data = append(data, chunk...)
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	upBeforeClose := e.net.Received()
	f.Close()
	if upBeforeClose < 8*MiB {
		t.Errorf("only %d bytes were uploaded while the file was being written", upBeforeClose)
	}
	e.waitRemote("long.bin", data, time.Minute)
	t.Logf("%d of %d MiB were uploaded before the writer finished", upBeforeClose/MiB, len(data)/MiB)
}

// putRemote writes a file straight to "Modal", as a Modal Function would.
func putRemote(t *testing.T, c *modalapi.Client, volID, name string, data []byte) {
	t.Helper()
	_, err := c.Put(context.Background(), volID, []modalapi.PutFile{{
		Path: name, Size: int64(len(data)), Hashes: modalapi.HashBlocks(data),
		Block: func(i int) ([]byte, error) {
			return data[i*modalapi.BlockSize : min((i+1)*modalapi.BlockSize, len(data))], nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// Someone else changes a file while modalfs uploads our version of it: their
// version stays, and ours is saved as a conflict copy.
func TestConflictDuringUpload(t *testing.T) {
	original := randomBytes(t, 4*MiB)
	e := setup(t, modalfake.Options{UpBytesPerSec: 5 * MiB}, nil, func(c *modalapi.Client, volID string) {
		putRemote(t, c, volID, "shared.bin", original)
	})
	ours := randomBytes(t, 40*MiB)
	time.Sleep(1100 * time.Millisecond) // Modal's mtimes are in seconds
	e.writeFile("shared.bin", ours)
	e.waitUploading(30 * time.Second)
	// Their version reuses blocks Modal already has, so it's committed at
	// once, in the middle of our upload (only its mtime differs).
	theirs := original
	putRemote(t, e.client, e.vol.ID, "shared.bin", theirs)

	deadline := time.Now().Add(2 * time.Minute)
	var copyName string
	for copyName == "" && time.Now().Before(deadline) {
		es, _ := e.client.List(context.Background(), e.vol.ID, "", false)
		for _, en := range es {
			if strings.HasPrefix(en.Path, "shared.conflict-") {
				copyName = en.Path
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if copyName == "" {
		t.Fatal("no conflict copy was made")
	}
	e.waitRemote(copyName, ours, time.Minute)
	got, err := e.remote("shared.bin")
	if err != nil || sum(got) != sum(theirs) {
		t.Fatalf("the other writer's version was overwritten (err %v)", err)
	}
}

// An edit to a file modalfs never read whose original is replaced on Modal
// before the commit can't be saved without mixing two versions. Nothing is
// committed, the edit is set aside with a note, and unmounting isn't blocked.
func TestEditOfReplacedFileIsSetAside(t *testing.T) {
	original := randomBytes(t, 64*MiB)
	e := setup(t, modalfake.Options{DownBytesPerSec: MiB}, []string{"-write-back", "2s"}, func(c *modalapi.Client, volID string) {
		putRemote(t, c, volID, "data.bin", original)
	})
	// overwrite block 0 whole, so the edit itself doesn't need the old bytes
	f, err := os.OpenFile(filepath.Join(e.mnt, "data.bin"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(randomBytes(t, 8*MiB), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	replacement := randomBytes(t, 3*MiB)
	putRemote(t, e.client, e.vol.ID, "data.bin", replacement)

	root := filepath.Dir(e.mnt)
	deadline := time.Now().Add(time.Minute)
	var notes []string
	for len(notes) == 0 && time.Now().Before(deadline) {
		notes, _ = filepath.Glob(filepath.Join(root, "cache", "*", "unsaved", "*", "README.txt"))
		time.Sleep(300 * time.Millisecond)
	}
	if len(notes) == 0 {
		t.Fatal("the edit wasn't set aside")
	}
	got, err := e.remote("data.bin")
	if err != nil || sum(got) != sum(replacement) {
		t.Fatalf("Modal's version was changed (err %v)", err)
	}
}
