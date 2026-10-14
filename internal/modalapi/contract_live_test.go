//go:build live

package modalapi

// Contract tests: Modal behaviour modalfs relies on that Modal doesn't
// document. They run against real Modal, in a scratch volume each creates
// and deletes, on a schedule (.github/workflows/modal-live.yml), so a change
// on Modal's side is found here before a user finds it.
//
//	MODALFS_E2E_PROFILE=<profile> go test -tags live -run Contract ./internal/modalapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
)

// scratch returns a client and a fresh Volume v2, deleted when the test ends.
func scratch(t *testing.T) (*Client, string) {
	t.Helper()
	profile := os.Getenv("MODALFS_E2E_PROFILE")
	if profile == "" {
		t.Skip("set MODALFS_E2E_PROFILE to a Modal profile to run against real Modal")
	}
	cfg, err := LoadConfig(profile)
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("modalfs-contract-%d", time.Now().UnixNano())
	v, err := c.CreateVolume(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.DeleteVolume(context.Background(), v.ID); err != nil {
			t.Errorf("delete scratch volume %s: %v", name, err)
		}
		c.Close()
	})
	return c, v.ID
}

func fresh(t *testing.T, n int) ([]byte, []byte) {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return b, h[:]
}

func put(t *testing.T, c *Client, volID, path string, data []byte) error {
	t.Helper()
	_, err := c.Put(context.Background(), volID, []PutFile{{Path: path, Size: int64(len(data)), Hashes: HashBlocks(data),
		Block: func(i int) ([]byte, error) { return data[i*BlockSize : min((i+1)*BlockSize, len(data))], nil }}})
	return err
}

func exists(t *testing.T, c *Client, volID, path string) bool {
	t.Helper()
	es, err := c.List(context.Background(), volID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Path == path {
			return true
		}
	}
	return false
}

// A request for upload URLs whose first block is made up never commits,
// whether or not Modal has the real block; the real block's upload works.
func TestContractAheadNeverCommits(t *testing.T) {
	c, vol := scratch(t)
	ctx := context.Background()
	data, hash := fresh(t, BlockSize)
	sent, err := c.Ahead(ctx, vol, ".modalfs/upload-ahead", hash, data)
	if err != nil || !sent {
		t.Fatalf("uploading ahead: sent %v, err %v", sent, err)
	}
	// Modal has a committed copy of this one: still nothing is committed
	known := bytes.Repeat([]byte{7}, BlockSize)
	if err := put(t, c, vol, "known.bin", known); err != nil {
		t.Fatal(err)
	}
	kh := sha256.Sum256(known)
	if sent, err := c.Ahead(ctx, vol, ".modalfs/upload-ahead", kh[:], known); err != nil || sent {
		t.Fatalf("a block Modal has: sent %v, err %v", sent, err)
	}
	if exists(t, c, vol, ".modalfs/upload-ahead") {
		t.Fatal("asking for upload URLs created the file it named")
	}
}

// A block uploaded ahead counts as missing to a commit without its receipt,
// and with the receipt it commits, minutes later, in a file other than the
// one its URL was asked for.
func TestContractReceiptCommitsLater(t *testing.T) {
	c, vol := scratch(t)
	ctx := context.Background()
	data, hash := fresh(t, BlockSize)
	if _, err := c.Ahead(ctx, vol, ".modalfs/upload-ahead", hash, data); err != nil {
		t.Fatal(err)
	}
	r, err := c.Stub.VolumePutFiles2(ctx, putReq(vol, "no-receipt.bin", int64(len(data)), hash, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.GetMissingBlocks()) != 1 {
		t.Fatalf("without its receipt the block uploaded ahead isn't missing (%d missing): modalfs could skip uploads it needs", len(r.GetMissingBlocks()))
	}
	if exists(t, c, vol, "no-receipt.bin") {
		t.Fatal("a commit with a missing block created the file")
	}
	wait := 2 * time.Minute
	if testing.Short() {
		wait = 0
	}
	time.Sleep(wait)
	n, err := c.Put(ctx, vol, []PutFile{{Path: "later.bin", Size: int64(len(data)), Hashes: [][]byte{hash},
		Block: func(int) ([]byte, error) { return data, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the commit uploaded %d blocks: the receipt from %v ago wasn't honoured", n, wait)
	}
	got, err := c.ReadAll(ctx, vol, "later.bin")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("committed with a receipt, the file reads back wrong (err %v)", err)
	}
}

// Modal refuses a block whose bytes don't match the hash it was asked for.
func TestContractWrongBytesRefused(t *testing.T) {
	c, vol := scratch(t)
	ctx := context.Background()
	_, hash := fresh(t, BlockSize)
	other, _ := fresh(t, BlockSize)
	made := make([]byte, 32)
	_, _ = rand.Read(made)
	r, err := c.Stub.VolumePutFiles2(ctx, putReq(vol, "wrong.bin", BlockSize, hash, nil))
	if err != nil || len(r.GetMissingBlocks()) != 1 {
		t.Fatalf("missing %d, err %v", len(r.GetMissingBlocks()), err)
	}
	req, _ := http.NewRequestWithContext(ctx, "PUT", r.GetMissingBlocks()[0].GetPutUrl(), bytes.NewReader(other))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		t.Fatalf("Modal took bytes that don't match their hash (HTTP %d)", resp.StatusCode)
	}
}

// Block transfers go over HTTP/1.1, one connection each (see New).
func TestContractBlocksOverHTTP1(t *testing.T) {
	c, vol := scratch(t)
	ctx := context.Background()
	data, _ := fresh(t, 3*BlockSize/2)
	if err := put(t, c, vol, "h1.bin", data); err != nil {
		t.Fatal(err)
	}
	urls, size, err := c.BlockURLs(ctx, vol, "h1.bin", 0, 2)
	if err != nil || size != int64(len(data)) || len(urls) != 2 {
		t.Fatalf("block URLs: %d, size %d, err %v", len(urls), size, err)
	}
	resp, err := c.HTTP.Get(urls[1])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 1 {
		t.Fatalf("a block download went over %s", resp.Proto)
	}
}

// putReq asks to commit path as one block, with an optional receipt.
func putReq(volID, path string, size int64, hash, receipt []byte) *pb.VolumePutFiles2Request {
	b := pb.VolumePutFiles2Request_Block_builder{ContentsSha256: hash}
	if receipt != nil {
		b.PutResponse = receipt
	}
	return pb.VolumePutFiles2Request_builder{VolumeId: volID, Files: []*pb.VolumePutFiles2Request_File{
		pb.VolumePutFiles2Request_File_builder{Path: path, Size: uint64(size), Blocks: []*pb.VolumePutFiles2Request_Block{b.Build()}}.Build(),
	}}.Build()
}

// ReadAll reads a committed file of one block or more.
func (c *Client) ReadAll(ctx context.Context, volID, path string) ([]byte, error) {
	urls, size, err := c.BlockURLs(ctx, volID, path, 0, 1<<20)
	if err != nil {
		return nil, err
	}
	var out []byte
	for i, u := range urls {
		b, err := c.Download(ctx, u, int(min(size-int64(i)*BlockSize, BlockSize)))
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}
