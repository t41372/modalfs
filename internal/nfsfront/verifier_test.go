package nfsfront

import (
	"testing"
	"time"

	nfsfile "github.com/willscott/go-nfs/file"
)

// A new listing with the same names must not replace a retained cookie's
// object identities after one of those names has been removed and recreated.
func TestDirectoryVerifierKeepsReplacementIdentitiesSeparate(t *testing.T) {
	b, fake, _, id := newNamespaceFS(t, time.Nanosecond)
	seedNamespaceFile(t, fake, id, "target", "old")
	h := newHandler(b)
	old, err := b.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	verifier := h.VerifierFor("", old)
	oldID := nfsfile.GetInfo(old[0]).Fileid
	h.Removed(b, []string{"target"})
	seedNamespaceFile(t, fake, id, "target", "new successor")
	newer, err := b.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	newID := nfsfile.GetInfo(newer[0]).Fileid
	if oldID == newID {
		t.Fatal("fixture did not replace the object identity")
	}
	h.VerifierFor("", newer)
	saved := h.DataForVerifier("", verifier)
	if len(saved) != 1 || nfsfile.GetInfo(saved[0]).Fileid != oldID {
		t.Fatalf("old READDIR cookie substituted its successor: old=%d new=%d entries=%+v", oldID, newID, saved)
	}
}
