//go:build integration

package vfs

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

// Random writes / truncates / extensions against an in-memory model, with
// commits in between, verified from a fresh client with an empty cache.
func TestRandomOpsModel(t *testing.T) {
	volName := os.Getenv("MODALFS_TEST_VOLUME")
	if volName == "" {
		t.Skip("MODALFS_TEST_VOLUME not set")
	}
	cfg, _ := modalapi.LoadConfig("")
	c, _ := modalapi.New(cfg)
	vol, err := c.LookupVolume(context.Background(), volName, true)
	if err != nil {
		t.Fatal(err)
	}
	mk := func() *FS {
		v, err := New(c, vol, Options{CacheDir: t.TempDir(), CacheMaxBytes: 1 << 30, WriteBack: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	const p = "modeltest/f.bin"
	v := mk()
	_ = v.Remove(p)
	if _, err := v.Stat("modeltest"); err != nil {
		if err := v.Mkdir("modeltest"); err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewSource(42))
	var model []byte
	if err := v.Create(p, false); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 5; round++ {
		for op := 0; op < 12; op++ {
			switch rng.Intn(4) {
			case 0, 1: // write somewhere (possibly past EOF)
				off := rng.Int63n(int64(len(model)) + 3<<20)
				data := make([]byte, 1+rng.Intn(64<<10))
				rng.Read(data)
				if _, err := v.WriteAt(p, data, off); err != nil {
					t.Fatal(err)
				}
				if need := off + int64(len(data)); need > int64(len(model)) {
					model = append(model, make([]byte, need-int64(len(model)))...)
				}
				copy(model[off:], data)
			case 2: // truncate down
				if len(model) > 0 {
					n := rng.Int63n(int64(len(model)))
					if err := v.Truncate(p, n); err != nil {
						t.Fatal(err)
					}
					model = model[:n]
				}
			case 3: // extend
				n := int64(len(model)) + rng.Int63n(9<<20)
				if err := v.Truncate(p, n); err != nil {
					t.Fatal(err)
				}
				model = append(model, make([]byte, n-int64(len(model)))...)
			}
			got := make([]byte, len(model))
			if n, err := v.ReadAt(p, got, 0); err != nil || n != len(model) || !bytes.Equal(got, model) {
				t.Fatalf("round %d op %d: local read mismatch (n=%d len=%d err=%v)", round, op, n, len(model), err)
			}
		}
		if err := v.Flush(p); err != nil {
			t.Fatal(err)
		}
		fresh := mk()
		got := make([]byte, len(model))
		a, err := fresh.Stat(p)
		if err != nil || a.Size != int64(len(model)) {
			t.Fatalf("round %d: size %d want %d err %v", round, a.Size, len(model), err)
		}
		if n, err := fresh.ReadAt(p, got, 0); err != nil || n != len(model) || !bytes.Equal(got, model) {
			t.Fatalf("round %d: remote mismatch", round)
		}
		t.Logf("round %d ok, size %d", round, len(model))
		fresh.Close()
	}
}
