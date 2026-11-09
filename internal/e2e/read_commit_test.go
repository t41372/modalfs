//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"google.golang.org/grpc"
)

// A commit has landed on Modal but its response hasn't reached modalfs.
// A cold read of an unchanged block must wait for its own commit to finish,
// then use the new base. Check both a finished state and one with newer writes.
func TestReadDuringOwnCommit(t *testing.T) {
	if live() {
		t.Skip("needs barriers in the fake Modal API")
	}
	for _, pending := range []bool{false, true} {
		name := "finished"
		if pending {
			name = "newer writes"
		}
		t.Run(name, func(t *testing.T) {
			landed := make(chan struct{})
			release := make(chan struct{})
			readChecked := make(chan struct{})
			var landOnce, releaseOnce, readOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			opt := modalfake.Options{InterceptRPC: func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				resp, err := next(ctx, req)
				if err != nil {
					return resp, err
				}
				switch r := req.(type) {
				case *pb.VolumePutFiles2Request:
					files := r.GetFiles()
					if len(files) == 1 && files[0].GetPath() == "f.bin" && files[0].GetSize() == modalapi.BlockSize+1 && len(resp.(*pb.VolumePutFiles2Response).GetMissingBlocks()) == 0 {
						landOnce.Do(func() { close(landed) })
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
				case *pb.VolumeGetFile2Request:
					if r.GetPath() == "f.bin" && resp.(*pb.VolumeGetFile2Response).GetSize() == modalapi.BlockSize+1 {
						readOnce.Do(func() { close(readChecked) })
					}
				}
				return resp, nil
			}}
			base := randomBytes(t, modalapi.BlockSize)
			e := setup(t, opt, []string{"-write-back", "1s", "-max-pending", "-1s", "-read-ahead", "0"}, func(c *modalapi.Client, volID string) {
				_, err := c.Put(context.Background(), volID, []modalapi.PutFile{{
					Path: "f.bin", Size: int64(len(base)), Hashes: modalapi.HashBlocks(base),
					Block: func(int) ([]byte, error) { return base, nil },
				}})
				if err != nil {
					t.Fatal(err)
				}
			})
			defer unblock() // release the RPC before setup's shutdown flush
			f, err := os.OpenFile(filepath.Join(e.mnt, "f.bin"), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				unblock()
				f.Close()
			}()
			if err := uncached(f); err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteAt([]byte("A"), int64(len(base))); err != nil {
				t.Fatal(err)
			}
			if err := fsync.File(f); err != nil {
				t.Fatal(err)
			}
			select {
			case <-landed:
			case <-time.After(30 * time.Second):
				t.Fatal("commit didn't land")
			}
			want := append(bytes.Clone(base), 'A')
			if pending {
				if _, err := f.WriteAt([]byte("Z"), int64(len(want))); err != nil {
					t.Fatal(err)
				}
				if err := fsync.File(f); err != nil {
					t.Fatal(err)
				}
				want = append(want, 'Z')
			}
			buf := make([]byte, 4096)
			done := make(chan error, 1)
			go func() {
				_, err := f.ReadAt(buf, 0)
				done <- err
			}()
			select {
			case <-readChecked:
			case <-time.After(30 * time.Second):
				t.Fatal("read didn't reach Modal with the old base")
			}
			select {
			case err := <-done:
				t.Fatalf("read returned before its commit updated the base: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			unblock()
			select {
			case err := <-done:
				if err != nil || !bytes.Equal(buf, base[:len(buf)]) {
					t.Fatalf("read racing its own commit: %v; content matches: %v", err, bytes.Equal(buf, base[:len(buf)]))
				}
			case <-time.After(30 * time.Second):
				t.Fatal("read didn't resume after its commit")
			}
			if got, err := os.ReadFile(f.Name()); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("local view after the commit: %v; content matches: %v", err, bytes.Equal(got, want))
			}
			e.waitRemote("f.bin", want, time.Minute)
		})
	}
}
