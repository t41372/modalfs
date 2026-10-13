//go:build e2e && darwin

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// Before choosing an encrypted format, measure the server's contract:
// path limits may differ from POSIX, short blocks may be padded before
// hashing, and a signed upload may not check its claimed digest.
func TestEncryptionModalContract(t *testing.T) {
	b := startBackend(t, t.TempDir(), modalfake.Options{})
	ctx := context.Background()
	name := "encryption-contract-fake.json"
	if live() {
		name = "encryption-contract-live.json"
	}
	a := newArtifact(t, name)
	for _, n := range []int{254, 255, 256, 512, 1024} {
		p := strings.Repeat("n", n)
		_, err := b.client.Stub.VolumePutFiles2(ctx, pb.VolumePutFiles2Request_builder{VolumeId: b.vol.ID, Files: []*pb.VolumePutFiles2Request_File{pb.VolumePutFiles2Request_File_builder{Path: p}.Build()}}.Build())
		a.step(fmt.Sprintf("component length %d", n), fmt.Sprint(err), nil)
		if (n <= 255) != (err == nil) {
			t.Fatalf("component length %d changed contract: %v", n, err)
		}
	}
	for _, n := range []int{1023, 1024, 4095, 4096, 4097, 8192, 32767, 32768, 65535, 65536, 1048576} {
		p := strings.Repeat(strings.Repeat("d", 199)+"/", n/200) + strings.Repeat("p", n%200)
		if strings.HasSuffix(p, "/") {
			p = p[:len(p)-1] + "p"
		}
		_, err := b.client.Stub.VolumePutFiles2(ctx, pb.VolumePutFiles2Request_builder{VolumeId: b.vol.ID, Files: []*pb.VolumePutFiles2Request_File{pb.VolumePutFiles2Request_File_builder{Path: p}.Build()}}.Build())
		a.step(fmt.Sprintf("path length %d", len(p)), fmt.Sprint(err), nil)
		if err == nil {
			_, size, getErr := b.client.BlockURLs(ctx, b.vol.ID, p, 0, 1)
			if getErr != nil || size != 0 {
				t.Fatalf("read exact %d-byte path: size %d, error %v", len(p), size, getErr)
			}
		}
	}
	for _, n := range []int{1, 48, modalapi.BlockSize - 1, modalapi.BlockSize, modalapi.BlockSize + 1} {
		data := randomBytes(t, n)
		// Deliberately include a zero suffix: ClientPadsBlocks must restore it.
		data[len(data)-1] = 0
		p := fmt.Sprintf("block-%d", n)
		putRemote(t, b.client, b.vol.ID, p, data)
		got, err := b.client.ReadFile(ctx, b.vol.ID, p, int64(n)+1)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("short block round trip %d: %v", n, err)
		}
		a.step(p, "exact bytes, actual-length SHA-256 accepted", nil)
	}
	data := randomBytes(t, 48)
	h := sha256.Sum256(data)
	f := pb.VolumePutFiles2Request_File_builder{
		Path: "wrong-digest", Size: 48,
		Blocks: []*pb.VolumePutFiles2Request_Block{pb.VolumePutFiles2Request_Block_builder{ContentsSha256: h[:]}.Build()},
	}.Build()
	r, err := b.client.Stub.VolumePutFiles2(ctx, pb.VolumePutFiles2Request_builder{VolumeId: b.vol.ID, Files: []*pb.VolumePutFiles2Request_File{f}}.Build())
	if err != nil || len(r.GetMissingBlocks()) != 1 {
		t.Fatalf("request signed upload: %v", err)
	}
	data[0] ^= 1
	req, err := http.NewRequestWithContext(ctx, "PUT", r.GetMissingBlocks()[0].GetPutUrl(), bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := b.client.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	a.step("upload bytes with a wrong digest", fmt.Sprintf("HTTP %d: %s", resp.StatusCode, body), nil)
	if resp.StatusCode < 400 {
		t.Fatal("server accepted bytes with the wrong hash")
	}
	listing, err := b.client.List(ctx, b.vol.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	a.step("raw root listing", "exact full paths checked separately; avoid quadratic output from deep ancestors", map[string]any{"entries": listing})
	a.pass()
}
