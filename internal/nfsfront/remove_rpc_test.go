package nfsfront

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// A directory-operation name is one child component, never a path. Both
// procedures share onRemove and must preserve the namespace's EINVAL reply.
func TestNamedRemoveRPCRejectsInvalidChildComponents(t *testing.T) {
	b, fake, client, id := newNamespaceFS(t, time.Hour)
	files := map[string]string{"sentinel": "keep root file", "child/target": "keep child file"}
	for name, content := range files {
		seedNamespaceFile(t, fake, id, name, content)
	}
	r := newNamespaceRPC(t, b, nil)
	for _, proc := range []struct {
		name string
		id   uint32
	}{{"REMOVE", 12}, {"RMDIR", 13}} {
		for _, name := range []string{"", ".", "..", "/sentinel", "child/target"} {
			t.Run(fmt.Sprintf("%s/%q", proc.name, name), func(t *testing.T) {
				response := r.call(struct {
					rpc.Header
					Where namedDirArg
				}{r.header(proc.id), namedDirArg{r.root, name}})
				if got := nfs.NFSStatus(binary.BigEndian.Uint32(response)); got != nfs.NFSStatusInval {
					t.Errorf("invalid child %q returned NFS status %d; want INVAL (%d)", name, got, nfs.NFSStatusInval)
				}
			})
		}
	}
	for name, want := range files {
		got, err := client.ReadFile(context.Background(), id, name, 1024)
		if err != nil || string(got) != want {
			t.Errorf("invalid operation changed %q: content=%q error=%v", name, got, err)
		}
	}
}
