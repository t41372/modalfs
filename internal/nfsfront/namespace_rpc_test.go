package nfsfront

import (
	"bytes"
	"context"
	"encoding/binary"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	"strings"
	"testing"
	"time"
)

func TestNamedRPCPinsRawHandlesAndAcceptsLongClientSpellings(t *testing.T) {
	b, fake, c, id := newNamespaceFS(t, time.Hour)
	raw := strings.Repeat("é", 127)
	clientName := strings.Repeat("e\u0301", 127)
	seedNamespaceFile(t, fake, id, raw, "original")
	r := newNamespaceRPC(t, b, nil)
	h, root := r.handler, r.root
	header, call := r.header, r.call
	type dirarg struct {
		Handle []byte
		Name   string
	}
	lookup := call(struct {
		rpc.Header
		Where dirarg
	}{header(3), dirarg{root, clientName}})
	if binary.BigEndian.Uint32(lookup) != 0 {
		t.Fatalf("long equivalent lookup: %x", lookup)
	}
	length := int(binary.BigEndian.Uint32(lookup[4:8]))
	handle := lookup[8 : 8+length]
	if !bytes.Equal(handle, h.ToHandle(b, []string{raw})) {
		t.Fatal("lookup handle wasn't pinned to raw path")
	}
	_, parts, err := h.FromHandle(handle)
	if err != nil || len(parts) != 1 || parts[0] != raw {
		t.Fatalf("handle identity: %q %v", parts, err)
	}
	// Existing long client spellings are legal aliases, but a missing new
	// component must fit the remote filesystem's actual basename limit.
	guarded := call(struct {
		rpc.Header
		Where dirarg
		How   uint32
		Attrs [6]uint32
	}{header(8), dirarg{root, clientName}, 1, [6]uint32{}})
	if binary.BigEndian.Uint32(guarded) != 17 {
		t.Fatalf("guarded existing alias: %x", guarded)
	}
	createTooLong := call(struct {
		rpc.Header
		Where dirarg
		How   uint32
		Attrs [6]uint32
	}{header(8), dirarg{root, clientName + "x"}, 0, [6]uint32{}})
	if binary.BigEndian.Uint32(createTooLong) != 63 {
		t.Fatalf("new overlong name: %x", createTooLong)
	}
	renamed := call(struct {
		rpc.Header
		From, To dirarg
	}{header(14), dirarg{root, clientName}, dirarg{root, "renamed"}})
	if binary.BigEndian.Uint32(renamed) != 0 {
		t.Fatalf("rename existing long alias: %x", renamed)
	}
	if got, err := c.ReadFile(context.Background(), id, "renamed", 1024); err != nil || string(got) != "original" {
		t.Fatalf("rename content: %q %v", got, err)
	}
	// Rename back to an existing canonical alias destination must also resolve
	// before the protocol applies the remote raw-byte limit.
	seedNamespaceFile(t, fake, id, raw, "destination")
	renamed = call(struct {
		rpc.Header
		From, To dirarg
	}{header(14), dirarg{root, "renamed"}, dirarg{root, clientName}})
	if binary.BigEndian.Uint32(renamed) != 0 {
		t.Fatalf("replace existing long alias: %x", renamed)
	}
	removed := call(struct {
		rpc.Header
		Where dirarg
	}{header(12), dirarg{root, clientName}})
	if binary.BigEndian.Uint32(removed) != 0 {
		t.Fatalf("remove existing long alias: %x", removed)
	}
	entries, err := c.List(context.Background(), id, "", false)
	if err != nil || len(entries) != 0 {
		t.Fatalf("left remote files: %+v %v", entries, err)
	}
}
