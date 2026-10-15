package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// A mount is pinned to the environment and volume it first mounts: the
// account's environment changing later doesn't move it to the volume of
// that name there, and a pin that can't be saved stops the mount.
func TestMountIsPinnedToItsEnvironment(t *testing.T) {
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
	inMain := fake.CreateVolume("data")
	m, err := d.AddMount(config.Mount{AccountID: a.ID, VolumeName: "data", Mountpoint: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	start := func(m config.Mount) (modalapi.VolumeInfo, error) {
		c, _, vol, err := d.pinMount(ctx, &m)
		if err == nil {
			closeClient(c)
		}
		return vol, err
	}
	if vol, err := start(m); err != nil || vol.ID != inMain {
		t.Fatalf("first start: %s, %v; want %s", vol.ID, err, inMain)
	}
	if saved, _ := d.store.Mount(m.ID); saved.Environment != "main" || saved.VolumeID != inMain {
		t.Fatalf("pinned to %q/%q, want main/%s", saved.Environment, saved.VolumeID, inMain)
	}

	// the account now uses another environment, with a volume of that name
	other, _, err := d.clientFor(a.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	inOther, err := other.LookupVolume(ctx, "data", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.Update(func(f *config.File) error {
		f.Accounts[0].Environment = "other"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	saved, _ := d.store.Mount(m.ID)
	if vol, err := start(saved); err != nil || vol.ID != inMain {
		t.Fatalf("started after the account's environment changed: %s, %v; want %s", vol.ID, err, inMain)
	}
	// that one is another volume, so it gets a mount of its own
	m2, err := d.AddMount(config.Mount{AccountID: a.ID, VolumeName: "data", Mountpoint: filepath.Join(t.TempDir(), "data2")})
	if err != nil {
		t.Fatalf("adding the other environment's volume: %v", err)
	}

	// a pin that can't be saved stops the mount
	if err := os.Mkdir(filepath.Join(config.Dir(), "config.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := start(m2); err == nil {
		t.Fatal("mounted a volume whose pin wasn't saved")
	}
	if saved, _ := d.store.Mount(m2.ID); saved.VolumeID != "" {
		t.Fatalf("a pin that wasn't saved shows: %s (the volume is %s)", saved.VolumeID, inOther.ID)
	}
}
