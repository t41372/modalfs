package nfsfront

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

func TestNewDestinationRPCRejectsUnrepresentableClientNames(t *testing.T) {
	b, fake, _, id := newNamespaceFS(t, time.Hour)
	seedNamespaceFile(t, fake, id, "source", "source data")
	r := newNamespaceRPC(t, b, nil)
	long := strings.Repeat("가", 80)
	for _, request := range []struct {
		name string
		body any
	}{
		{"create", struct {
			rpc.Header
			Where namedDirArg
			How   uint32
			Attrs [6]uint32
		}{r.header(8), namedDirArg{r.root, long}, 0, [6]uint32{}}},
		{"mkdir", struct {
			rpc.Header
			Where namedDirArg
			Attrs [6]uint32
		}{r.header(9), namedDirArg{r.root, long}, [6]uint32{}}},
		{"rename", struct {
			rpc.Header
			From, To namedDirArg
		}{r.header(14), namedDirArg{r.root, "source"}, namedDirArg{r.root, long}}},
	} {
		t.Run(request.name, func(t *testing.T) {
			response := r.call(request.body)
			if got := nfs.NFSStatus(binary.BigEndian.Uint32(response)); got != nfs.NFSStatusNameTooLong {
				t.Fatalf("unrepresentable destination returned NFS status %d; want NAMETOOLONG (%d)", got, nfs.NFSStatusNameTooLong)
			}
		})
	}
}
