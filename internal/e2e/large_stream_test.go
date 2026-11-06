//go:build e2e

package e2e

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/fsync"
)

// Large sustained transfers are opt-in: CI keeps the smaller fault
// regression, while these runs cross thousands of block/commit boundaries.
// Native copies need a local source of this size; streams do not.
func largeWriteSize(t *testing.T, variable string, fallback int64) int64 {
	t.Helper()
	if value := os.Getenv(variable); value != "" {
		n, err := strconv.ParseUint(value, 10, 16)
		if err != nil || n == 0 {
			t.Fatalf("%s must be a positive number of GiB", variable)
		}
		return int64(n) << 30
	}
	return fallback << 30
}

// Total transfer time scales with data size. Individual directory/API
// response limits do not: probeDaemonCopy cancels the writer on a failure.
func largeWriteBudget(size int64) time.Duration {
	return max(10*time.Minute, time.Duration(size>>30)*time.Minute)
}

type streamWriteJob struct {
	Path, Result string
	Size         int64
	Seed         [32]byte
}

// TestDaemonLargeStream writes real, unique bytes through a mounted volume
// from a separate process, without keeping a source file as large as the
// transfer on this computer. Every remote byte is read back and hashed.
func TestDaemonLargeStream(t *testing.T) {
	if os.Getenv("MODALFS_E2E_STREAM_GIB") == "" {
		t.Skip("set MODALFS_E2E_STREAM_GIB for a sustained large-write run")
	}
	size := largeWriteSize(t, "MODALFS_E2E_STREAM_GIB", 100)
	root, b, p := setupWriteDaemon(t, size)
	job := streamWriteJob{Path: filepath.Join(p.mnt, "stream.bin"), Result: filepath.Join(root, "sha256"), Size: size}
	if _, err := cryptorand.Read(job.Seed[:]); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), largeWriteBudget(size))
	defer cancel()
	stop := probeDaemonCopy(t, p, cancel)
	defer stop()
	cmd := exec.CommandContext(ctx, bin, "-test.run=^TestLargeStreamWriter$")
	cmd.Env = append(os.Environ(), "MODALFS_E2E_WRITE_JOB="+string(raw))
	start := time.Now()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("streaming %d GiB: %v\n%s", size>>30, err, out)
	}
	t.Logf("wrote and synced %d GiB in %v", size>>30, time.Since(start).Round(time.Millisecond))
	want, err := os.ReadFile(job.Result)
	if err != nil {
		t.Fatal(err)
	}
	waitRemoteHash(t, b, "stream.bin", size, string(want))
	stop()
}

// Only the child process writes on the mount. The parent can continue
// serving the fake/proxy and checking liveness even when a write blocks.
func TestLargeStreamWriter(t *testing.T) {
	raw := os.Getenv("MODALFS_E2E_WRITE_JOB")
	if raw == "" {
		t.Skip("writer subprocess only")
	}
	var job streamWriteJob
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(job.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(rand.NewChaCha8(job.Seed), job.Size), make([]byte, 4*MiB))
	if err != nil || n != job.Size {
		t.Fatalf("write: got %d of %d bytes: %v", n, job.Size, err)
	}
	if err := fsync.File(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(job.Result, []byte(hex.EncodeToString(h.Sum(nil))), 0o600); err != nil {
		t.Fatal(err)
	}
}
