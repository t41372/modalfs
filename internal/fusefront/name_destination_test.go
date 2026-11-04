package fusefront

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
)

func TestNewDestinationsFitConvertedClientNames(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			f := newTestFrontend(t, encrypted, decomposedName)
			f.put(t, "source", "source data")
			source := f.lookup(t, f.rootNode(), "source")
			count := 80
			if encrypted {
				count = 46
			}
			long := strings.Repeat("가", count)
			if _, _, _, status := f.root.Create(context.Background(), long, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o600, &fuse.EntryOut{}); status != syscall.ENAMETOOLONG {
				t.Errorf("create: %v", status)
			}
			if _, status := f.root.Mkdir(context.Background(), long, 0o700, &fuse.EntryOut{}); status != syscall.ENAMETOOLONG {
				t.Errorf("mkdir: %v", status)
			}
			if status := f.root.Rename(context.Background(), "source", f.root, long, 0); status != syscall.ENAMETOOLONG {
				t.Errorf("rename: %v", status)
			}
			if got, status := readNode(source.n); status != 0 || got != "source data" {
				t.Fatalf("rejected rename changed source binding: %q %v", got, status)
			}
			// A name already on the volume remains available for recovery.
			f.put(t, long, "remote data")
			if status := f.root.Rename(context.Background(), long, f.root, "recovered", 0); status != 0 {
				t.Fatalf("raw long-to-short recovery: %v", status)
			}
			f.readRemote(t, "recovered", "remote data")
		})
	}
}
