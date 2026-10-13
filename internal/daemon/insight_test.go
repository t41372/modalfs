package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"github.com/t41372/modalfs/internal/volume"
)

func TestTrafficKeepsAnHourAndRestartsWithTheMount(t *testing.T) {
	var tr traffic
	for i := range trafficKeep + 20 {
		tr.add("m", TrafficSample{int64(i), int64(i) * 10, int64(i)})
	}
	got := tr.get("m", 0)
	if len(got) != trafficKeep {
		t.Fatalf("kept %d samples, want %d", len(got), trafficKeep)
	}
	if got[0][0] != 20 {
		t.Fatalf("oldest sample is %d, want 20", got[0][0])
	}
	// The UI asks only for what it hasn't seen.
	if got := tr.get("m", got[len(got)-3][0]); len(got) != 2 || got[0][0] != int64(trafficKeep+18) {
		t.Fatalf("since: %v", got)
	}
	// A new mount starts its totals at zero: the old samples would make a negative rate.
	tr.add("m", TrafficSample{1000, 5, 5})
	if got := tr.get("m", 0); len(got) != 1 || got[0][1] != 5 {
		t.Fatalf("after a restart: %v", got)
	}
	tr.drop("m")
	if got := tr.get("m", 0); len(got) != 0 {
		t.Fatalf("after drop: %v", got)
	}
}

func TestConflictsMarkedHandled(t *testing.T) {
	var c conflicts
	c.add("m", Conflict{Path: "a.txt", Copy: "a.conflict-1.txt"})
	c.add("m", Conflict{Path: "b.txt", Copy: "b.conflict-1.txt"})
	c.add("other", Conflict{Path: "c.txt", Copy: "c.conflict-1.txt"})
	c.clear("m", "a.conflict-1.txt")
	if got := c.get("m"); len(got) != 1 || got[0].Path != "b.txt" {
		t.Fatalf("after clearing one: %v", got)
	}
	c.clear("m", "")
	if c.count("m") != 0 || c.count("other") != 1 {
		t.Fatalf("after clearing all: m=%d other=%d", c.count("m"), c.count("other"))
	}
}

func TestUsageMeasuresAVolumeAndKeepsTheResult(t *testing.T) {
	dir := t.TempDir()
	fake, err := modalfake.Start(filepath.Join(dir, "fake"), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	c, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	vol, err := c.LookupVolume(ctx, "sized", true)
	if err != nil {
		t.Fatal(err)
	}
	put := func(name string, n int) {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(i * 7)
		}
		_, err := c.Put(ctx, vol.ID, []modalapi.PutFile{{Path: name, Size: int64(n), Hashes: modalapi.HashBlocks(data),
			Block: func(i int) ([]byte, error) { return data[i*modalapi.BlockSize : min((i+1)*modalapi.BlockSize, n)], nil }}})
		if err != nil {
			t.Fatal(err)
		}
	}
	put("raw/a.bin", 3000)
	put("raw/deep/b.bin", 2000)
	put("meta/c.json", 500)
	put("top.txt", 100)

	u := newUsageStore(dir)
	u.start(&volume.Volume{Client: c, ID: vol.ID})
	deadline := time.Now().Add(10 * time.Second)
	for u.view(vol.ID).Running {
		if time.Now().After(deadline) {
			t.Fatal("still measuring after 10 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
	v := u.view(vol.ID)
	if v.Error != "" || v.Last == nil {
		t.Fatalf("no result: %+v", v)
	}
	if v.Last.Total != 5600 || v.Last.Files != 4 {
		t.Fatalf("total %d in %d files, want 5600 in 4", v.Last.Total, v.Last.Files)
	}
	if len(v.Last.Folders) != 2 || v.Last.Folders[0].Name != "raw" || v.Last.Folders[0].Bytes != 5000 || v.Last.Folders[1].Name != "meta" {
		t.Fatalf("folders: %+v", v.Last.Folders)
	}
	// Kept for the next daemon.
	if again, ok := newUsageStore(dir).last(vol.ID); !ok || again.Total != 5600 {
		t.Fatalf("not saved: %+v %v", again, ok)
	}
}

// A replacement token must come from the account's workspace, so its mounts can't move.
func TestReplaceTokenKeepsTheWorkspace(t *testing.T) {
	t.Setenv("MODALFS_HOME", t.TempDir())
	t.Setenv("MODALFS_NO_KEYCHAIN", "1")
	fake, err := modalfake.Start(filepath.Join(t.TempDir(), "fake"), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	store, err := config.Open()
	if err != nil {
		t.Fatal(err)
	}
	d := testDaemon(t)
	d.store = store
	ctx := context.Background()
	add := func(id, workspace string) {
		if err := store.Update(func(f *config.File) error {
			f.Accounts = append(f.Accounts, config.Account{ID: id, Label: id, TokenID: "ak-old-" + id, ServerURL: fake.URL, Workspace: workspace, SecretStore: "file"})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("same", "fake-workspace")
	add("other", "someone-else")

	a, err := d.ReplaceToken(ctx, "same", "ak-new", "as-new")
	if err != nil {
		t.Fatal(err)
	}
	if a.TokenID != "ak-new" {
		t.Fatalf("token id %q", a.TokenID)
	}
	if s, err := config.LoadSecret(a); err != nil || s != "as-new" {
		t.Fatalf("secret %q, %v", s, err)
	}
	if _, err := d.ReplaceToken(ctx, "other", "ak-new2", "as-new2"); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("a token of another workspace was accepted: %v", err)
	}
	if _, err := d.ReplaceToken(ctx, "other", "ak-new", "as-x"); err == nil || !strings.Contains(err.Error(), "already added") {
		t.Fatalf("a token already used by another account was accepted: %v", err)
	}
}
