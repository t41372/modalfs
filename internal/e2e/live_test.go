//go:build e2e && live && darwin

// The e2e checks against real Modal, in a scratch volume that's created
// for the run and deleted afterwards. It uploads about 2 GB.
//
//	MODALFS_LIVE_PROFILE=<profile in ~/.modal.toml> go test -tags e2e,live -run Live -timeout 30m ./internal/e2e
package e2e

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

func TestLive(t *testing.T) {
	profile := os.Getenv("MODALFS_LIVE_PROFILE")
	if profile == "" {
		t.Skip("set MODALFS_LIVE_PROFILE to the Modal profile to test with")
	}
	cfg, err := modalapi.LoadConfig(profile)
	if err != nil {
		t.Fatal(err)
	}
	client, err := modalapi.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := fmt.Sprintf("modalfs-scratch-%d", time.Now().Unix())
	vol, err := client.LookupVolume(ctx, name, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scratch volume %s (%s) in profile %s", name, vol.ID, profile)
	t.Cleanup(func() {
		if err := client.DeleteVolume(ctx, vol.ID); err != nil {
			t.Errorf("couldn't delete the scratch volume %s: %v", name, err)
		}
	})

	// a file already on Modal, for the streaming test
	movie := randomBytes(t, 512*MiB)
	t0 := time.Now()
	putRemote(t, client, vol.ID, "movie.mov", movie)
	t.Logf("uploaded 512 MiB directly in %v", time.Since(t0).Round(time.Second))

	root, err := os.MkdirTemp("", "mfs-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	mnt := filepath.Join(root, "mnt")
	cmd := exec.Command(modalfsBinary(t), "run", "-profile", profile, "-cache-dir", filepath.Join(root, "cache"), "-cache-size", "2G", name, mnt)
	var logs syncBuffer
	cmd.Stdout, cmd.Stderr = &logs, &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGINT)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Minute):
			_ = exec.Command("/sbin/umount", "-f", mnt).Run()
			_ = cmd.Process.Kill()
			<-done
			t.Error("modalfs run didn't exit within 5 minutes of SIGINT")
		}
		t.Log(logs.String())
	})
	deadline := time.Now().Add(30 * time.Second)
	for !mounted(mnt) {
		if time.Now().After(deadline) {
			t.Fatalf("not mounted:\n%s", logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	e := &env{t: t, client: client, vol: vol, mnt: mnt}

	t.Run("streaming read", func(t *testing.T) {
		e.t = t
		stop := e.probe("")
		f, err := os.Open(filepath.Join(mnt, "movie.mov"))
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
		t.Logf("first 64 KiB after %v; 512 MiB in %v (%.1f MiB/s); slowest listing %v", ttfb, took.Round(time.Millisecond), 512/took.Seconds(), worst)
		if sum(append(first, rest...)) != sum(movie) {
			t.Fatal("read the wrong content")
		}
		if worst > maxStall {
			t.Errorf("the mount stalled for %v while reading", worst)
		}
	})

	t.Run("large copy stays responsive", func(t *testing.T) {
		e.t = t
		data := randomBytes(t, 1024*MiB)
		stop := e.probe("big.bin")
		t0 := time.Now()
		e.writeFile("big.bin", data)
		t.Logf("copy returned after %v", time.Since(t0).Round(time.Millisecond))
		e.waitRemote("big.bin", data, 15*time.Minute)
		worst := stop()
		t.Logf("on Modal after %v; slowest listing/stat meanwhile %v", time.Since(t0).Round(time.Second), worst)
		if worst > maxStall {
			t.Errorf("the mount stalled for %v while a file uploaded", worst)
		}
	})

	t.Run("write during commit", func(t *testing.T) {
		e.t = t
		data := randomBytes(t, 160*MiB)
		e.writeFile("f.bin", data)
		time.Sleep(5 * time.Second) // the commit starts after 3 s idle
		f, err := os.OpenFile(filepath.Join(mnt, "f.bin"), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		patch := randomBytes(t, 4096)
		more := randomBytes(t, 20*MiB+12345)
		if _, err := f.WriteAt(patch, 8*MiB+100); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(more, int64(len(data))); err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		f.Close()
		want := append(append([]byte{}, data...), more...)
		copy(want[8*MiB+100:], patch)
		e.waitRemote("f.bin", want, 10*time.Minute)
	})
}
