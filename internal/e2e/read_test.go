//go:build e2e && darwin

package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// putFile stores data under name on the fake, bypassing the mount.
func putFile(t *testing.T, c *modalapi.Client, volID, name string, data []byte) {
	t.Helper()
	_, err := c.Put(context.Background(), volID, []modalapi.PutFile{{
		Path: name, Size: int64(len(data)), Hashes: modalapi.HashBlocks(data),
		Block: func(i int) ([]byte, error) {
			return data[i*modalfake.BlockSize : min((i+1)*modalfake.BlockSize, len(data))], nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// Opening many large files and reading the start of each (what Quick Look
// and Finder previews do) must not download them whole. The macOS NFS
// client reads 16 MiB ahead by itself, so that's two blocks per file; read-
// ahead in modalfs must add nothing to that.
func TestReadAheadBounded(t *testing.T) {
	const files, blocks = 12, 6
	base := randomBytes(t, modalfake.BlockSize)
	content := func(f int) []byte {
		data := make([]byte, 0, blocks*modalfake.BlockSize)
		for b := 0; b < blocks; b++ {
			blk := append([]byte(nil), base...)
			copy(blk, fmt.Sprintf("block %08d", f*blocks+b)) // every block is distinct
			data = append(data, blk...)
		}
		return data
	}
	put := func(c *modalapi.Client, volID string) {
		for f := 0; f < files; f++ {
			putFile(t, c, volID, fmt.Sprintf("v%02d.mov", f), content(f))
		}
	}
	e := setup(t, modalfake.Options{Latency: 20 * time.Millisecond}, []string{"-read-ahead", "8"}, put)

	var wg sync.WaitGroup
	errs := make(chan error, files)
	sem := make(chan struct{}, 8)
	for f := 0; f < files; f++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(f int) {
			defer wg.Done()
			defer func() { <-sem }()
			fh, err := os.Open(filepath.Join(e.mnt, fmt.Sprintf("v%02d.mov", f)))
			if err != nil {
				errs <- err
				return
			}
			defer fh.Close()
			got := make([]byte, 64<<10)
			if _, err := io.ReadFull(fh, got); err != nil {
				errs <- err
				return
			}
			if want := content(f)[:len(got)]; sum(got) != sum(want) {
				errs <- fmt.Errorf("v%02d.mov: wrong content", f)
			}
		}(f)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second) // let any read-ahead that was started finish
	served := e.fake.Served()
	t.Logf("read 64 KiB of %d files of %d MiB: %d MiB downloaded", files, blocks*8, served/MiB)
	if limit := int64(files) * modalfake.BlockSize * 5 / 2; served > limit {
		t.Errorf("downloaded %d MiB to read the start of %d files; want at most %d MiB (what the NFS client itself asks for)", served/MiB, files, limit/MiB)
	}
}

// Files written while their folder is listed over and over must not get
// conflict copies: a commit's check that its file arrived must see a
// listing made after the commit, not join one that started before it.
func TestNoConflictWhileListing(t *testing.T) {
	put := func(c *modalapi.Client, volID string) {
		var fs []modalapi.PutFile
		for i := 0; i < 300; i++ {
			fs = append(fs, modalapi.PutFile{Path: fmt.Sprintf("d/old%03d", i)})
		}
		if _, err := c.Put(context.Background(), volID, fs); err != nil {
			t.Fatal(err)
		}
	}
	e := setup(t, modalfake.Options{Latency: 50 * time.Millisecond}, []string{"-write-back", "100ms", "-dir-cache-ttl", "1ms"}, put)
	dir := filepath.Join(e.mnt, "d")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = os.ReadDir(dir)
			}
		}()
	}
	want := map[string][]byte{}
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("d/new%02d.txt", i)
		want[name] = randomBytes(t, 1000+i)
		if err := os.WriteFile(filepath.Join(e.mnt, name), want[name], 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	for name, data := range want {
		e.waitRemote(name, data, time.Minute)
	}
	time.Sleep(2 * time.Second) // a commit retried after a failed check would make its copy now
	close(stop)
	wg.Wait()
	ents, err := e.client.List(context.Background(), e.vol.ID, "d", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range ents {
		if strings.Contains(en.Path, ".conflict-") {
			t.Errorf("spurious conflict copy: %s", en.Path)
		}
	}
}

// Streaming a large file reads at close to the link's speed and downloads
// each block once; reading it again comes from the cache, a range at a time.
func TestStreamingReadThroughput(t *testing.T) {
	const link = 40 * MiB
	data := randomBytes(t, 256*MiB)
	put := func(c *modalapi.Client, volID string) { putFile(t, c, volID, "movie.mov", data) }
	e := setup(t, modalfake.Options{DownBytesPerSec: link, Latency: 30 * time.Millisecond}, []string{"-read-ahead", "8"}, put)
	name := filepath.Join(e.mnt, "movie.mov")

	read := func(nocache bool) (string, float64) {
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if nocache {
			// bypass the NFS client's own cache, so every read reaches modalfs
			if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_NOCACHE, 1); errno != 0 {
				t.Fatal(errno)
			}
		}
		t0 := time.Now()
		var got []byte
		buf := make([]byte, MiB)
		for {
			n, err := f.Read(buf)
			got = append(got, buf[:n]...)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		return sum(got), float64(len(got)) / MiB / time.Since(t0).Seconds()
	}

	h, cold := read(false)
	if h != sum(data) {
		t.Fatal("first read returned the wrong content")
	}
	served := e.fake.Served()
	h, warm := read(true)
	if h != sum(data) {
		t.Fatal("cached read returned the wrong content")
	}
	t.Logf("cold %.1f MiB/s over a %d MiB/s link (%d MiB downloaded); cached %.1f MiB/s", cold, link/MiB, served/MiB, warm)
	if cold < 0.7*link/MiB {
		t.Errorf("streaming read ran at %.1f MiB/s, under 70%% of the %d MiB/s link", cold, link/MiB)
	}
	if served > int64(len(data))+2*modalfake.BlockSize {
		t.Errorf("downloaded %d MiB to read a %d MiB file", served/MiB, len(data)/MiB)
	}
	if again := e.fake.Served() - served; again > 0 {
		t.Errorf("reading a cached file downloaded %d MiB again", again/MiB)
	}
	if warm < 100 {
		t.Errorf("reading a cached file ran at %.1f MiB/s; want at least 100 MiB/s", warm)
	}
}
