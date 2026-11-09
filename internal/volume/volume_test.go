package volume

import (
	"bytes"
	"context"
	"testing"

	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// A block shorter than the file says it is reads as its bytes followed by
// zeros, in a plaintext volume and in an encrypted one. modalfs 0.2.0 could
// commit a file that way (see Download), and that is the content it had.
func TestShortBlockReadsAsZeros(t *testing.T) {
	fake, err := modalfake.Start(t.TempDir(), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	ctx := context.Background()
	c, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	vol, err := c.LookupVolume(ctx, "test", true)
	if err != nil {
		t.Fatal(err)
	}
	for name, cipher := range map[string]*encryption.Cipher{"plain": nil, "encrypted": encryption.New(encryption.NewKey())} {
		v := &Volume{Client: c, ID: vol.ID, Cipher: cipher}
		short := []byte("A") // the block as it was hashed and uploaded
		const size = 100     // the file's length, as committed
		_, err := v.PutChecked(ctx, []modalapi.PutFile{{Path: name, Size: size, Hashes: [][]byte{v.HashBlock(short)},
			Block: func(int) ([]byte, error) { return short, nil }}}, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := v.ReadFile(ctx, name, 1<<20)
		if want := append([]byte("A"), make([]byte, size-1)...); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: read %d bytes (%v), want %q then zeros", name, len(got), err, "A")
		}
	}
}

// Only a name of the encrypted form (the prefix, then base64url) says a
// volume may be encrypted; a plaintext name that starts the same way doesn't.
func TestLooksEncrypted(t *testing.T) {
	for name, want := range map[string]bool{
		"mfs1_notes.txt": false, "mfs1_": false, "notes.txt": false, "mfs1_abc=": false,
		"mfs1_QUJDREVGR0g": true, "mfs1_-_09azAZ": true,
	} {
		if got := looksEncrypted(name); got != want {
			t.Errorf("looksEncrypted(%q) = %v, want %v", name, got, want)
		}
	}
}
