package nfsfront

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// unixCred builds AUTH_UNIX credentials as the kernel sends them.
func unixCred(machine string, uid, gid uint32) rpc.Auth {
	var b bytes.Buffer
	w := func(v uint32) { _ = binary.Write(&b, binary.BigEndian, v) }
	w(12345) // stamp
	w(uint32(len(machine)))
	b.WriteString(machine)
	b.Write(make([]byte, (4-len(machine)%4)%4))
	w(uid)
	w(gid)
	w(1) // one extra gid
	w(gid)
	return rpc.Auth{Flavor: 1, Body: b.Bytes()}
}

func TestAuthorize(t *testing.T) {
	h := &handler{uid: 501}
	for _, c := range []struct {
		name string
		hdr  rpc.Header
		ok   bool
	}{
		{"owner", rpc.Header{Proc: 1, Cred: unixCred("mac.local", 501, 20)}, true},
		{"root", rpc.Header{Proc: 1, Cred: unixCred("mac", 0, 0)}, true},
		{"another user", rpc.Header{Proc: 1, Cred: unixCred("mac.local", 502, 20)}, false},
		{"no credentials", rpc.Header{Proc: 1, Cred: rpc.Auth{Flavor: 0}}, false},
		{"truncated", rpc.Header{Proc: 1, Cred: rpc.Auth{Flavor: 1, Body: []byte{0, 0, 0, 1, 0, 0, 0, 9, 'a'}}}, false},
		{"NULL ping without credentials", rpc.Header{Proc: 0}, true},
	} {
		if got := h.Authorize(c.hdr); got != c.ok {
			t.Errorf("%s: Authorize = %v, want %v", c.name, got, c.ok)
		}
	}
}

func TestHandlesAreStable(t *testing.T) {
	h := newHandler(nil)
	a := h.ToHandle(nil, []string{"dir", "file"})
	if b := h.ToHandle(nil, []string{"dir", "file"}); !bytes.Equal(a, b) {
		t.Fatal("the same path got two handles")
	}
	if c := h.ToHandle(nil, []string{"dir", "other"}); bytes.Equal(a, c) {
		t.Fatal("two paths share a handle")
	}
	if _, p, err := h.FromHandle(a); err != nil || len(p) != 2 || p[1] != "file" {
		t.Fatalf("FromHandle = %v, %v", p, err)
	}
	_ = h.InvalidateHandle(nil, a)
	if _, _, err := h.FromHandle(a); err == nil {
		t.Fatal("an invalidated handle still resolves")
	}
	if _, _, err := newHandler(nil).FromHandle(a); err == nil {
		t.Fatal("another server's handle resolves")
	}
}
