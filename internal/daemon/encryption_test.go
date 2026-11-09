package daemon

import (
	"context"
	"testing"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// A plaintext volume isn't taken for an encrypted one because a file's name
// starts like an encrypted name. A volume with names of the encrypted form
// and no marker is remembered as encrypted, so it shows as locked, but only
// for as long as such names are there: 0.2.0 kept the record after the
// file was renamed, and the volume stayed locked out for good.
func TestEncryptionIsNotGuessedFromAName(t *testing.T) {
	t.Setenv("MODALFS_HOME", t.TempDir())
	t.Setenv("MODALFS_NO_KEYCHAIN", "1")
	fake, err := modalfake.Start(t.TempDir(), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	d := testDaemon(t)
	if d.store, err = config.Open(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a, err := d.AddAccount(ctx, "test", "ak-test", "as-test", fake.URL, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.dropClients("") })
	c, _, err := d.clientFor(a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	id := fake.CreateVolume("plain")
	put := func(name string) {
		t.Helper()
		if _, err := c.Put(ctx, id, []modalapi.PutFile{{Path: name, Size: 1, Hashes: modalapi.HashBlocks([]byte("x")),
			Block: func(int) ([]byte, error) { return []byte("x"), nil }}}); err != nil {
			t.Fatal(err)
		}
	}
	put("mfs1_notes.txt")
	if _, encrypted, err := d.inspectVolume(ctx, c, id); err != nil || encrypted {
		t.Fatalf("a plaintext file named mfs1_notes.txt: encrypted=%v, %v", encrypted, err)
	}
	if err := d.saveEncryption(config.VolumeEncryption{VolumeID: id}); err != nil { // remembered on a guess, by 0.2.0
		t.Fatal(err)
	}
	if _, encrypted, err := d.inspectVolume(ctx, c, id); err != nil || encrypted {
		t.Fatalf("remembered on a guess that no longer holds: encrypted=%v, %v", encrypted, err)
	}
	if n := len(d.store.Snapshot().Encryption); n != 0 {
		t.Fatalf("%d encryption records kept, want the guessed one gone", n)
	}
	raw, err := encryption.New(encryption.NewKey()).EncodePath("secret.bin")
	if err != nil {
		t.Fatal(err)
	}
	put(raw)
	if _, encrypted, err := d.inspectVolume(ctx, c, id); err != nil || !encrypted {
		t.Fatalf("a real encrypted name: encrypted=%v, %v", encrypted, err)
	}
	if n := len(d.store.Snapshot().Encryption); n != 1 {
		t.Fatalf("%d encryption records, want the volume remembered while its encrypted name is there", n)
	}
	if err := c.Remove(ctx, id, raw, false); err != nil {
		t.Fatal(err)
	}
	if _, encrypted, err := d.inspectVolume(ctx, c, id); err != nil || encrypted {
		t.Fatalf("after the encrypted name went: encrypted=%v, %v", encrypted, err)
	}
	if n := len(d.store.Snapshot().Encryption); n != 0 {
		t.Fatalf("%d encryption records kept after the volume stopped looking encrypted, want none", n)
	}
}
