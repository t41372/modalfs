//go:build live

package modalapi

import (
	"context"
	"strings"
	"testing"
)

// Plain sidecars add ".json" to a data basename. At the valid 255-byte
// data-name boundary, that optional sidecar cannot exist. Purging its
// missing path must succeed or report NotFound, without touching the data.
func TestContractOversizedMissingSidecarRemoval(t *testing.T) {
	c, vol := scratch(t)
	ctx := context.Background()
	name := strings.Repeat("x", 255)
	if err := put(t, c, vol, name, nil); err != nil {
		t.Fatalf("create valid 255-byte data filename: %v", err)
	}
	metadata := ".modalfs/manifests/" + name + ".json"
	if err := c.Remove(ctx, vol, metadata, true); err != nil && !IsNotFound(err) {
		t.Fatalf("missing 260-byte sidecar removal must succeed or report NotFound: %v", err)
	}
	data, err := c.ReadFile(ctx, vol, name, 1)
	if err != nil || len(data) != 0 {
		t.Fatalf("sidecar removal changed the valid empty data file: size=%d err=%v", len(data), err)
	}
	t.Log("valid 255-byte data file is unchanged after removing its missing 260-byte sidecar path")
}
