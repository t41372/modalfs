package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/daemon"
)

// serveCLI answers the CLI's requests with handle, in place of the daemon.
func serveCLI(t *testing.T, handle http.HandlerFunc) {
	home, err := os.MkdirTemp("/tmp", "mfs-cli-") // short: macOS limits socket paths
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("MODALFS_HOME", home)
	l, err := net.Listen("unix", daemon.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handle}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { srv.Close() })
}

// standIn answers the CLI as the daemon would, with one saved mount, and
// records the changes it's sent and whether the mount was started.
func standIn(t *testing.T) (changes chan map[string]any, started chan bool) {
	changes, started = make(chan map[string]any, 1), make(chan bool, 1)
	st := daemon.StateView{
		Accounts: []config.Account{{ID: "acct-1", Label: "work"}},
		Mounts:   []daemon.MountView{{Mount: config.Mount{ID: "mnt-1", AccountID: "acct-1", Environment: "main", VolumeName: "data", Mountpoint: "/tmp/data"}, State: daemon.StateStopped}},
	}
	serveCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/state":
			_ = json.NewEncoder(w).Encode(st)
			return
		case r.Method == "PATCH" && r.URL.Path == "/api/mounts/mnt-1":
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			changes <- in
		case r.URL.Path == "/api/mounts/mnt-1/start":
			started <- true
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	return changes, started
}

// `modalfs mount data --ro` on a saved mount mounts it read-only: what's
// given changes the saved mount, and what isn't stays as saved.
func TestMountChangesASavedMount(t *testing.T) {
	changes, started := standIn(t)
	cmdMount([]string{"data", "--ro", "--on-conflict", "overwrite", "--write-back", "10s"})
	select {
	case got := <-changes:
		want := map[string]any{"read_only": true, "on_conflict": "overwrite", "write_back_ms": float64(10000)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("changed %v, want %v", got, want)
		}
	default:
		t.Fatal("the flags given didn't change the saved mount")
	}
	if len(started) != 1 {
		t.Fatal("the saved mount wasn't mounted")
	}
}

// Without flags, a saved mount mounts as saved, whatever environment it's
// pinned to.
func TestMountASavedMountAsSaved(t *testing.T) {
	changes, started := standIn(t)
	cmdMount([]string{"data"})
	if len(changes) != 0 {
		t.Fatalf("changed %v without being asked", <-changes)
	}
	if len(started) != 1 {
		t.Fatal("the saved mount wasn't mounted")
	}
}
