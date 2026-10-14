package modalapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
)

// Uploading blocks ahead of their commit. VolumePutFiles2 is the only call
// that hands out upload URLs, and it commits the file it names as soon as
// Modal has every block. Asking for a file whose first block is made up
// (a random hash Modal can't have) gets the URLs without ever committing.
// Modal then keeps an uploaded block for a later commit only with the
// receipt its upload returned; without it the block still counts as
// missing. None of this is documented by Modal, so the contract tests
// (contract_live_test.go) check it against real Modal on a schedule, and a
// change shows up as errAheadBroken here or as blocks uploaded twice.

// receiptTTL is how long a receipt is used. Modal has honoured receipts 15
// minutes old; a commit that finds one expired just uploads that block again.
const receiptTTL = 10 * time.Minute

// errAheadBroken: a request meant to fetch upload URLs committed instead.
var errAheadBroken = errors.New("Modal committed a request that should only have returned upload URLs")

// IsAheadBroken reports whether uploading ahead stopped working as expected.
func IsAheadBroken(err error) bool { return errors.Is(err, errAheadBroken) }

type receipt struct {
	resp []byte
	at   time.Time
}

type receipts struct {
	mu sync.Mutex
	m  map[string]receipt // volume ID + hex hash
}

func receiptKey(volID string, hash []byte) string { return volID + "/" + hex.EncodeToString(hash) }

func (r *receipts) put(volID string, hash, resp []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]receipt{}
	}
	now := time.Now()
	for k, v := range r.m {
		if now.Sub(v.at) > receiptTTL {
			delete(r.m, k)
		}
	}
	r.m[receiptKey(volID, hash)] = receipt{resp, now}
}

func (r *receipts) get(volID string, hash []byte) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.m[receiptKey(volID, hash)]
	if !ok || time.Since(v.at) > receiptTTL {
		return nil
	}
	return v.resp
}

// UploadLimit is how many block uploads run at once now (see upSlots).
func (c *Client) UploadLimit() int { return c.up.Limit() }

// UploadRate is the speed block uploads reached lately, in bytes a second
// (0 until measured).
func (c *Client) UploadRate() float64 { return c.up.Rate() }

// Ahead uploads one block (data, whose SHA-256 is hash) so a later commit
// of any file in the volume that holds it needn't upload it. stage names
// the file the URL request is made for; it's never created. It reports
// whether the block was sent: false when Modal already had it.
func (c *Client) Ahead(ctx context.Context, volID, stage string, hash, data []byte) (bool, error) {
	if c.receipts.get(volID, hash) != nil {
		return false, nil
	}
	made := make([]byte, 32)
	if _, err := rand.Read(made); err != nil {
		return false, err
	}
	req := pb.VolumePutFiles2Request_builder{VolumeId: volID, Files: []*pb.VolumePutFiles2Request_File{
		pb.VolumePutFiles2Request_File_builder{Path: stage, Size: uint64(BlockSize + len(data)), Blocks: []*pb.VolumePutFiles2Request_Block{
			pb.VolumePutFiles2Request_Block_builder{ContentsSha256: made}.Build(),
			pb.VolumePutFiles2Request_Block_builder{ContentsSha256: hash}.Build(),
		}}.Build(),
	}}.Build()
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.VolumePutFiles2Response, error) {
		return c.Stub.VolumePutFiles2(ctx, req)
	})
	if err != nil {
		return false, err
	}
	var url string
	madeMissing := false
	for _, m := range r.GetMissingBlocks() {
		switch m.GetBlockIndex() {
		case 0:
			madeMissing = true
		case 1:
			url = m.GetPutUrl()
		}
	}
	if !madeMissing {
		return false, errAheadBroken
	}
	if url == "" {
		return false, nil // Modal has it already
	}
	resp, err := c.upload(ctx, url, data)
	if err != nil {
		return false, err
	}
	c.receipts.put(volID, hash, resp)
	return true, nil
}
