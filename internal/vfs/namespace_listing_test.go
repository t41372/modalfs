package vfs

import (
	"context"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

// Unlike listDir, freshNames intentionally does not join the in-flight RPC.
// Its newer result must remain cached when the older listDir returns later.
func TestFreshNamesSurvivesOlderListCompletion(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, DirCacheTTL: time.Hour})
	f.put(t, "dir/old", []byte("old"))
	release := make(chan struct{})
	var once sync.Once
	releaseOld := func() { once.Do(func() { close(release) }) }
	defer releaseOld()
	g := &gatedList{ModalClientClient: f.client.Stub, gate: release, listed: make(chan struct{})}
	f.client.Stub = g
	old := make(chan map[string]modalapi.Entry, 1)
	go func() {
		entries, err := f.listDir("dir", false)
		if err != nil {
			t.Error(err)
		}
		old <- entries
	}()
	select {
	case <-g.listed:
	case <-time.After(5 * time.Second):
		t.Fatal("old listing did not reach its response gate")
	}
	f.put(t, "dir/new", []byte("new"))
	fresh, err := f.freshNames("dir", "new")
	if err != nil {
		t.Fatal(err)
	}
	f.releaseNameSnapshot("dir", "new", fresh)
	if _, present := fresh.entries["new"]; !present {
		t.Fatal("fresh listing missed the new file")
	}
	releaseOld()
	if _, present := (<-old)["new"]; present {
		t.Fatal("old RPC unexpectedly contained the new file")
	}
	if _, _, err := f.Names().Lookup("dir", "new"); err != nil {
		t.Fatalf("older RPC hid the fresh namespace result: %v", err)
	}
}

func TestNameProblemReportsAgainAfterResolution(t *testing.T) {
	for _, kind := range []string{"collision", "unconvertible", "too-long"} {
		t.Run(kind, func(t *testing.T) {
			const raw, alias = "caf\u00e9", "cafe\u0301"
			var mu sync.Mutex
			reports := 0
			key := testNameKey
			limit := 255
			problem := alias
			if kind == "unconvertible" {
				problem = "bad"
				key = func(name string) (string, error) {
					if name == problem {
						return "", syscall.EINVAL
					}
					return testNameKey(name)
				}
			} else if kind == "too-long" {
				problem = "too-long"
				limit = 6
			}
			f := newFakeFS(t, Options{NameKey: key, DirCacheTTL: time.Hour, OnEvent: func(kind, _, _ string) {
				if kind == "unreadable" {
					mu.Lock()
					reports++
					mu.Unlock()
				}
			}})
			f.put(t, raw, []byte("first"))
			f.put(t, problem, []byte("problem"))
			check := func(want int) {
				t.Helper()
				if _, err := f.Names().ReadDirLimit("", limit); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				got := reports
				mu.Unlock()
				if got != want {
					t.Fatalf("unreadable reports: got %d, want %d", got, want)
				}
			}
			check(1)
			check(1)
			f.put(t, "other", []byte("unrelated"))
			f.invalidate("")
			check(1) // an unrelated refresh must not repeat the same problem
			if err := f.client.Remove(context.Background(), f.vol.ID, problem, false); err != nil {
				t.Fatal(err)
			}
			f.invalidate("")
			check(1) // a healthy listing resets only the resolved problem
			f.put(t, problem, []byte("problem returned"))
			f.invalidate("")
			check(2)
			check(2)
		})
	}
}
