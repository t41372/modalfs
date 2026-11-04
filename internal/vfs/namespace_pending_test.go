package vfs

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// An editor commonly creates, writes and renames its temporary file before
// write-back uploads it. Local-only membership has no remote FileEntry yet.
func TestNamespacePendingFileOperations(t *testing.T) {
	for _, operation := range []string{"reopen", "rename", "remove"} {
		t.Run(operation, func(t *testing.T) {
			f := newFakeFS(t, Options{WriteBack: time.Hour, NameKey: testNameKey})
			ns := f.Names()
			raw, _, err := ns.Create("", "café.tmp", true, nil)
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte("not uploaded yet")
			if _, err := f.WriteExistingAt(raw, payload, 0); err != nil {
				t.Fatal(err)
			}
			if entries, err := f.client.List(context.Background(), f.vol.ID, "", false); err != nil || len(entries) != 0 {
				t.Fatalf("fixture already uploaded: %+v, %v", entries, err)
			}
			switch operation {
			case "reopen":
				path, attr, err := ns.Create("", "cafe\u0301.tmp", false, nil)
				if err != nil || path != raw || attr.Size != int64(len(payload)) {
					t.Fatalf("reopen pending file: %q, %+v, %v", path, attr, err)
				}
			case "rename":
				_, target, err := ns.Rename("", "cafe\u0301.tmp", "", "saved.txt")
				if err != nil {
					t.Fatal(err)
				}
				got, err := f.client.ReadFile(context.Background(), f.vol.ID, target, 1024)
				if err != nil || string(got) != string(payload) {
					t.Fatalf("renamed pending data: %q, %v", got, err)
				}
			case "remove":
				if _, err := ns.Remove("", "cafe\u0301.tmp"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.Stat(raw); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("removed pending file: %v", err)
				}
				if f.Status().PendingFiles != 0 {
					t.Fatal("removed file retained pending writes")
				}
			}
		})
	}
}
