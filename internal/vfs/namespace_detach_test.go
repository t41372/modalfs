package vfs

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

// finishCommitLocked is called at the same lock boundary as a completed
// background upload. Keep the selected state blocked until it detaches.
func TestNamespaceTruncateRetriesCommittedState(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour})
	ns := f.Names()
	raw, _, err := ns.Create("", "caf\u00e9", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("pending before truncate")
	if _, err := f.WriteAt(raw, payload, 0); err != nil {
		t.Fatal(err)
	}
	f.put(t, raw, payload)
	f.mu.Lock()
	st := f.files[raw]
	f.mu.Unlock()
	st.mu.Lock()
	locked := true
	defer func() {
		if locked {
			st.mu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() {
		size := int64(0)
		_, attr, err := ns.Create("", "cafe\u0301", false, &size)
		if err == nil && attr.Size != 0 {
			err = fmt.Errorf("truncate returned stale attributes: size=%d", attr.Size)
		}
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		if strings.Contains(string(buf[:n]), "(*fileState).truncate") { // truncate, or truncateMakes before it
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("namespace Create did not reach truncate")
		}
		runtime.Gosched()
	}
	dirty := map[int64]bool{}
	for idx := range st.dirty {
		dirty[idx] = true
	}
	st.finishCommitLocked(&snapshot{gen: st.gen, size: st.size, dirty: dirty}, raw, time.Now().Unix(), modalapi.HashBlocks(payload))
	st.mu.Unlock()
	locked = false
	if err := <-done; err != nil {
		t.Fatalf("a concurrent successful commit broke O_TRUNC create: %v", err)
	}
	if err := f.Flush(raw); err != nil {
		t.Fatal(err)
	}
	got, err := f.client.ReadFile(context.Background(), f.vol.ID, raw, 100)
	if err != nil || len(got) != 0 {
		t.Fatalf("truncate result: %q, %v", got, err)
	}
}
