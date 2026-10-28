//go:build e2e

package e2e

// Symlinks and other entries Modal's Volume API can't change (see
// docs/design/fs-metadata.md). Every syscall here runs outside the mount
// server, with an isolated daemon and home.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	modal "github.com/modal-labs/modal-client/go"
	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"github.com/t41372/modalfs/internal/volume"
)

type metadataFixture struct {
	Path   string `json:"path"`
	Target string `json:"target,omitempty"`
	Data   string `json:"data,omitempty"`
	Mode   uint32 `json:"mode"`
	Mtime  int64  `json:"mtime"`
}

// metadataContainer is test-only compute, exclusively in a scratch volume.
func metadataContainer(t *testing.T, b backend, art *artifact, script string) string {
	t.Helper()
	cfg, err := modalapi.LoadConfig(liveProfile())
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := modal.NewClientWithOptions(&modal.ClientParams{TokenID: cfg.TokenID, TokenSecret: cfg.TokenSecret, Environment: cfg.Environment})
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	ctx := context.Background()
	app, err := sdk.Apps.FromName(ctx, fmt.Sprintf("modalfs-e2e-metadata-%d", time.Now().UnixNano()), &modal.AppFromNameParams{Environment: cfg.Environment, CreateIfMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := b.client.StopApp(ctx, app.AppID); err != nil {
			t.Error(err)
		}
	}()
	sb, err := sdk.Sandboxes.Create(ctx, app, sdk.Images.FromRegistry("python:3.12-slim", nil), &modal.SandboxCreateParams{Volumes: map[string]*modal.Volume{"/vol": {VolumeID: b.vol.ID}}, Timeout: 3 * time.Minute, CPU: 0.25, MemoryMiB: 512})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		code, err := sb.Terminate(ctx, &modal.SandboxTerminateParams{Wait: true})
		art.step("terminate test Sandbox", fmt.Sprintf("%s exit=%d error=%v", sb.SandboxID, code, err), nil)
		if err != nil {
			t.Error(err)
		}
	}()
	script += "\nimport subprocess\nsubprocess.run(['sync','/vol'],check=True)\n"
	proc, err := sb.Exec(ctx, []string{"python3", "-c", script}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(proc.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := io.ReadAll(proc.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	code, err := proc.Wait(ctx, nil)
	art.step("container observation", string(out), map[string]any{"script": script, "exit": code, "stderr": string(stderr), "sandbox": sb.SandboxID})
	if err != nil || code != 0 {
		t.Fatalf("container: %d %v %s", code, err, stderr)
	}
	return string(out)
}

func seedMetadata(t *testing.T, b backend, art *artifact, fixtures []metadataFixture) {
	t.Helper()
	if live() {
		data, err := json.Marshal(fixtures)
		if err != nil {
			t.Fatal(err)
		}
		metadataContainer(t, b, art, "import os,json,socket\nfixtures=json.loads("+fmt.Sprintf("%q", string(data))+`)
for f in fixtures:
 p='/vol/'+f['path']
 os.makedirs(os.path.dirname(p),exist_ok=True)
 if os.path.lexists(p): os.unlink(p)
 if f['mode'] & 0o170000 == 0o120000: os.symlink(f['target'],p)
 elif f['mode'] & 0o170000 == 0o140000:
  short='/vol/socket-seed'
  socket.socket(socket.AF_UNIX).bind(short)
  os.rename(short,p)
 else:
  open(p,'w').write(f.get('data',''))
  os.chmod(p,f['mode'] & 0o7777)
 if f['mtime']: os.utime(p,(f['mtime'],f['mtime']),follow_symlinks=False)
print(json.dumps([dict(path=f['path'],mode=oct(os.lstat('/vol/'+f['path']).st_mode),mtime_ns=os.lstat('/vol/'+f['path']).st_mtime_ns) for f in fixtures]))
`)
		return
	}
	fake := b.net.(*modalfake.Server)
	var entries []modalfake.SeedEntry
	for _, f := range fixtures {
		mode := os.FileMode(f.Mode & 0o777)
		switch f.Mode & syscall.S_IFMT {
		case syscall.S_IFLNK:
			mode |= os.ModeSymlink
		case syscall.S_IFSOCK:
			mode |= os.ModeSocket
		}
		entries = append(entries, modalfake.SeedEntry{Path: f.Path, Data: []byte(f.Data), Target: f.Target, Mode: mode, Mtime: f.Mtime})
	}
	if err := fake.Seed(b.vol.ID, entries...); err != nil {
		t.Fatal(err)
	}
}

func TestFilesystemMetadata(t *testing.T) {
	// It starts Sandboxes: only in the workspaces kept for these tests.
	if p := liveProfile(); live() && p != "topsoil-drakes" && p != "ci" {
		t.Fatalf("profile %s: use topsoil-drakes or ci, the workspaces kept for testing", p)
	}
	for _, encrypted := range []bool{false, true} {
		name := "plain"
		if encrypted {
			name = "encrypted"
		}
		t.Run(name, func(t *testing.T) {
			root, err := os.MkdirTemp("", "mfs-meta-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if mounted(root + "/d-mnt") {
					t.Error("scratch mount remains; retaining " + root)
					return
				}
				os.RemoveAll(root)
			})
			backend := startBackend(t, root, modalfake.Options{Latency: 5 * time.Millisecond, UpBytesPerSec: 12 * MiB, DownBytesPerSec: 12 * MiB})
			filename := "metadata-fake-" + name + ".json"
			if live() {
				filename = "metadata-live-" + name + ".json"
			}
			art := newArtifact(t, filename)
			art.step("scratch volume", backend.vol.Name, map[string]any{"id": backend.vol.ID, "live": live(), "profile": liveProfile() + " for live; isolated fake otherwise"})
			profile, config := "fake", filepath.Join(root, "modal.toml")
			if live() {
				profile, config = liveProfile(), modalapi.ModalConfigPath()
			} else {
				if err := os.WriteFile(config, []byte(fmt.Sprintf("[fake]\ntoken_id = \"ak-fake\"\ntoken_secret = \"as-fake\"\nserver_url = %q\n", backend.net.(*modalfake.Server).URL)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			remote := &volume.Volume{Client: backend.client, ID: backend.vol.ID}
			key := encryption.NewKey()
			if encrypted {
				remote.Cipher = encryption.New(key)
				if err := remote.WriteMarker(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			raw := func(p string) string {
				q, err := remote.Path(p)
				if err != nil {
					t.Fatal(err)
				}
				return q
			}
			if err := remote.WriteFile(context.Background(), "target", []byte("original-target")); err != nil {
				t.Fatal(err)
			}
			if err := remote.WriteFile(context.Background(), "tree/file", []byte("tree-file")); err != nil {
				t.Fatal(err)
			}
			var fixtures []metadataFixture
			for p, target := range map[string]string{"relative": "target", "absolute": "/vol/target", "dangling": "missing", "directory": "tree", "outside": "/etc/hostname", "tree/nested/link": "../../target"} {
				fixtures = append(fixtures, metadataFixture{Path: raw(p), Target: target, Mode: syscall.S_IFLNK | 0o777, Mtime: 1000000000})
			}
			fixtures = append(fixtures, metadataFixture{Path: raw("socket"), Mode: syscall.S_IFSOCK | 0o755, Mtime: 1000000000}, metadataFixture{Path: raw("socket-remove"), Mode: syscall.S_IFSOCK | 0o755, Mtime: 1000000000})
			seedMetadata(t, backend, art, fixtures)
			initial, err := backend.client.List(context.Background(), backend.vol.ID, "", true)
			if err != nil {
				t.Fatal(err)
			}
			for i, f := range fixtures {
				if f.Mode&syscall.S_IFMT == syscall.S_IFSOCK {
					for _, e := range initial {
						if e.Path == f.Path {
							fixtures[i].Mtime = e.Mtime
						}
					}
				}
			}
			art.step("initial raw listing", fmt.Sprint(initial), map[string]any{"entries": initial})
			d := startDaemon(t, modalfsBinary(t), filepath.Join(root, "d"), config)
			d.must("account", "import", profile)
			if encrypted {
				if out, err := encryptionCLI(d, key.Text(), "unlock", backend.vol.Name); err != nil {
					t.Fatalf("unlock: %s %v", out, err)
				}
			}
			d.must("mount", backend.vol.Name, d.mnt)
			watchForStall(t, d.cmd, d.logs, d.mnt)
			type event struct{ Kind, Message string }
			events := func() []event {
				var rows []event
				daemonJSON(t, d, "GET", "/api/events", nil, &rows)
				return rows
			}
			before := len(events())
			entries, err := os.ReadDir(d.mnt)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.Name() == "target" || e.Name() == "tree" {
					continue
				}
				st, err := os.Lstat(filepath.Join(d.mnt, e.Name()))
				if err != nil {
					t.Fatal(err)
				}
				want := os.ModeSymlink
				if strings.HasPrefix(e.Name(), "socket") {
					want = os.ModeSocket
				}
				if st.Mode()&want == 0 || e.Type()&want == 0 || (want == os.ModeSymlink && st.ModTime().Unix() != 1000000000) {
					t.Fatalf("type/mtime %s: %s dirent=%s", e.Name(), st.Mode(), e.Type())
				}
				for _, f := range fixtures {
					if f.Path == raw(e.Name()) && st.ModTime().Unix() != f.Mtime {
						t.Fatalf("mtime differs from API for %s", e.Name())
					}
				}
				if want == os.ModeSymlink {
					for _, f := range fixtures {
						if f.Path == raw(e.Name()) && st.Size() != int64(len(f.Target)) {
							t.Fatalf("link size: %s %d", e.Name(), st.Size())
						}
					}
				}
				art.step("lstat "+e.Name(), fmt.Sprintf("mode=%s size=%d mtime=%d", st.Mode(), st.Size(), st.ModTime().Unix()), nil)
			}
			if len(events()) != before {
				t.Fatal("listing emitted an error event")
			}
			// explained: whether Activity says why. A read or write through a
			// link is a readlink to the kernel, and only the mount's first
			// readlink is explained (a folder of links reads every one).
			refuse := func(op string, explained bool, fn func() error) {
				t.Helper()
				before := len(events())
				err := fn()
				if !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EIO) {
					t.Fatalf("%s: %v; want ENOTSUP, or macOS EIO for wire NFS3ERR_NOTSUPP", op, err)
				}
				after := events()
				if !explained {
					if len(after) != before {
						t.Fatalf("%s added events: %+v", op, after[before:])
					}
					art.step(op, err.Error(), nil)
					return
				}
				if len(after) != before+1 || after[len(after)-1].Kind != "error" || !strings.Contains(after[len(after)-1].Message, "Modal container") {
					t.Fatalf("%s events: %+v -> %+v", op, before, after)
				}
				art.step(op, err.Error(), map[string]any{"event": after[len(after)-1]})
			}
			p := func(name string) string { return filepath.Join(d.mnt, name) }
			refuse("readlink", true, func() error {
				_, err := os.Readlink(p("relative"))
				return err
			})
			refuse("readlink again", false, func() error {
				_, err := os.Readlink(p("absolute"))
				return err
			})
			refuse("read", false, func() error {
				_, err := os.ReadFile(p("relative"))
				return err
			})
			refuse("write", false, func() error { return os.WriteFile(p("relative"), []byte("bad"), 0o600) })
			refuse("truncate", false, func() error { return os.Truncate(p("relative"), 0) })
			refuse("unlink", true, func() error { return syscall.Unlink(p("relative")) })
			refuse("rename link", true, func() error { return os.Rename(p("relative"), p("moved")) })
			refuse("rename over link", true, func() error { return os.Rename(p("target"), p("relative")) })
			refuse("rename tree", true, func() error { return os.Rename(p("tree"), p("moved-tree")) })
			if err := os.Mkdir(p("empty-destination"), 0o751); err != nil {
				t.Fatal(err)
			}
			before = len(events())
			renameErr := os.Rename(p("tree"), p("empty-destination"))
			if !errors.Is(renameErr, syscall.EIO) && !errors.Is(renameErr, syscall.EEXIST) {
				t.Fatalf("rename over empty destination: %v", renameErr)
			}
			art.step("rename tree over empty folder", renameErr.Error(), map[string]any{"events_before": before, "events_after": len(events()), "note": "macOS may reject an existing directory before sending RENAME"})
			if es, err := os.ReadDir(p("empty-destination")); err != nil || len(es) != 0 {
				t.Fatalf("rename changed destination: %+v %v", es, err)
			}
			refuse("symlink", true, func() error { return os.Symlink("target", p("new-link")) })
			refuse("hard link", true, func() error { return os.Link(p("target"), p("new-hard")) })
			if err := syscall.Unlink(p("socket-remove")); err != nil {
				t.Fatalf("socket unlink supported by API was refused: %v", err)
			}
			art.step("unlink socket", "succeeded; public API supports socket removal", nil)
			for _, name := range []string{"moved", "moved-tree", "new-link", "new-hard"} {
				if _, err := os.Lstat(p(name)); !os.IsNotExist(err) {
					t.Fatalf("refusal left %s: %v\n%s", name, err, d.logs.String())
				}
			}
			if got, err := os.ReadFile(p("target")); err != nil || string(got) != "original-target" {
				t.Fatal("target changed")
			}
			// Owner's explicit compatibility choice: metadata setters still succeed.
			if err := os.Chmod(p("target"), 0o751); err != nil {
				t.Fatal(err)
			}
			if err := os.Chown(p("target"), os.Getuid(), os.Getgid()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(p("target"), time.Unix(946684800, 0), time.Unix(946684800, 0)); err != nil {
				t.Fatal(err)
			}
			// An editor's save (a new file renamed over the old) next to links.
			writeEncryptedFile(t, d.mnt, ".editor-temp", []byte("atomic-save"))
			if err := os.Rename(p(".editor-temp"), p("target")); err != nil {
				t.Fatal(err)
			}
			d.must("sync", backend.vol.Name)
			d.must("unmount", backend.vol.Name)
			listing, err := backend.client.List(context.Background(), backend.vol.ID, "", true)
			if err != nil {
				t.Fatal(err)
			}
			art.step("final raw listing", fmt.Sprint(listing), map[string]any{"entries": listing})
			byPath := map[string]modalapi.Entry{}
			for _, e := range listing {
				byPath[e.Path] = e
			}
			for _, f := range fixtures {
				if f.Path == raw("socket-remove") {
					if _, ok := byPath[f.Path]; ok {
						t.Fatal("removed socket still exists")
					}
					continue
				}
				e, ok := byPath[f.Path]
				want := os.ModeSymlink
				if f.Mode&syscall.S_IFMT == syscall.S_IFSOCK {
					want = os.ModeSocket
				}
				if !ok || e.Mode()&want == 0 || e.Mtime != f.Mtime {
					t.Fatalf("remote fixture changed: %+v %+v", f, e)
				}
			}
			data, err := remote.ReadFile(context.Background(), "target", 100)
			if err != nil || string(data) != "atomic-save" {
				t.Fatalf("committed target: %q %v", data, err)
			}
			art.step("committed target checksum", sum(data), nil)

			if live() {
				observed := metadataContainer(t, backend, art, `import os,stat,json,hashlib
rows=[]
for root,dirs,files in os.walk('/vol',followlinks=False):
 for name in sorted(dirs+files):
  p=os.path.join(root,name)
  s=os.lstat(p)
  r=dict(path=os.path.relpath(p,'/vol'),mode=oct(s.st_mode),size=s.st_size,mtime_ns=s.st_mtime_ns,nlink=s.st_nlink,ino=s.st_ino)
  if stat.S_ISLNK(s.st_mode): r['target']=os.readlink(p)
  if stat.S_ISREG(s.st_mode): r['sha256']=hashlib.sha256(open(p,'rb').read()).hexdigest()
  rows.append(r)
print(json.dumps(rows))
`)
				var rows []struct {
					Path   string `json:"path"`
					Target string `json:"target"`
				}
				if err := json.Unmarshal([]byte(observed), &rows); err != nil {
					t.Fatal(err)
				}
				targets := map[string]string{}
				for _, r := range rows {
					targets[r.Path] = r.Target
				}
				for _, f := range fixtures {
					if f.Mode&syscall.S_IFMT == syscall.S_IFLNK && targets[f.Path] != f.Target {
						t.Fatalf("container link changed: %+v", f)
					}
				}
			} else {
				rows, err := backend.net.(*modalfake.Server).Inspect(backend.vol.ID)
				if err != nil {
					t.Fatal(err)
				}
				art.step("final fake inspection", fmt.Sprint(rows), map[string]any{"entries": rows})
			}
			art.step("daemon log", d.logs.String(), nil)
			art.pass()
		})
	}
}

// A journal from before a container put a link at its path: kept, said why
// once, not retried, and never committed over the link or as a copy.
func TestFilesystemMetadataJournal(t *testing.T) {
	name := "metadata-journal-fake.json"
	if live() {
		name = "metadata-journal-live.json"
	}
	art := newArtifact(t, name)
	e := setup(t, modalfake.Options{Latency: 5 * time.Millisecond}, []string{"-write-back", "1h"}, func(c *modalapi.Client, id string) {
		putRemote(t, c, id, "pending", []byte("base"))
		putRemote(t, c, id, "target", []byte("target"))
	})
	art.step("scratch volume", e.vol.Name, map[string]any{"id": e.vol.ID, "live": live()})
	e.writeFile("pending", []byte("retained-write"))
	e.crash()
	dirty := filepath.Join(e.cache, e.vol.ID, "dirty")
	journals := map[string]string{}
	err := filepath.WalkDir(dirty, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		journals[p] = sum(data)
		return nil
	})
	if err != nil || len(journals) < 2 {
		t.Fatalf("journal fixture: %v %+v", err, journals)
	}
	b := backend{client: e.client, vol: e.vol, net: e.net}
	seedMetadata(t, b, art, []metadataFixture{{Path: "pending", Target: "target", Mode: syscall.S_IFLNK | 0o777, Mtime: 1000000000}})
	// Its pending changes still show at that path, like any not yet
	// uploaded; the commit recovery tries says why it can't upload them.
	e.launch()
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(e.logs.String(), "remount to upload") {
		if time.Now().After(deadline) {
			t.Fatalf("journal was not explained: %s", e.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The former retry delay starts at 10 s. A blocked state stays quiet.
	time.Sleep(11 * time.Second)
	logs := e.logs.String()
	if strings.Count(logs, "remount to upload") != 1 || !strings.Contains(logs, dirty) {
		t.Fatalf("blocked journal explanation: %s", logs)
	}
	for p, want := range journals {
		data, err := os.ReadFile(p)
		if err != nil || sum(data) != want {
			t.Fatalf("journal or dirty chunk changed: %s %v", p, err)
		}
	}
	listing, err := e.client.List(context.Background(), e.vol.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(listing) != 2 {
		t.Fatalf("unexpected committed conflict copy: %+v", listing)
	}
	data, err := e.remote("target")
	if err != nil || string(data) != "target" {
		t.Fatal("target changed")
	}
	art.step("old symlink journal retained", logs, map[string]any{"journal_and_chunk_sha256": journals, "remote": listing, "target_sha256": sum(data)})
	art.pass()
}

// The file can become a link after the first Put requested missing blocks.
// The final commit must recheck even with conflict-overwrite selected.
func TestFilesystemMetadataCommitRace(t *testing.T) {
	if live() {
		t.Skip("deterministic block-upload boundary uses fake seeding; real type/API contract is tested separately")
	}
	for _, policy := range []string{"keep-both", "overwrite"} {
		t.Run(policy, func(t *testing.T) {
			art := newArtifact(t, "metadata-commit-race-"+policy+".json")
			e := setup(t, modalfake.Options{Latency: 5 * time.Millisecond, UpBytesPerSec: MiB}, []string{"-write-back", "100ms", "-max-pending", "-1s", "-sidecar-min", "0", "-on-conflict", policy}, func(c *modalapi.Client, id string) { putRemote(t, c, id, "target", []byte("original-target")) })
			art.step("scratch volume", e.vol.Name, map[string]any{"id": e.vol.ID, "live": false})
			start := e.net.Received()
			data := randomBytes(t, 8*MiB)
			e.writeFile("file", data)
			deadline := time.Now().Add(20 * time.Second)
			for e.net.Received() == start {
				if time.Now().After(deadline) {
					t.Fatal("upload did not start")
				}
				time.Sleep(20 * time.Millisecond)
			}
			fake := e.net.(*modalfake.Server)
			if err := fake.Seed(e.vol.ID, modalfake.SeedEntry{Path: "file", Target: "target", Mode: os.ModeSymlink | 0o777}); err != nil {
				t.Fatal(err)
			}
			for !strings.Contains(e.logs.String(), "remount to upload") {
				if time.Now().After(deadline) {
					t.Fatalf("late replacement was not explained: %s", e.logs.String())
				}
				time.Sleep(50 * time.Millisecond)
			}
			rows, err := fake.Inspect(e.vol.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 2 {
				t.Fatalf("late link was copied or lost: %+v", rows)
			}
			found := false
			for _, r := range rows {
				if r.Path == "file" {
					found = r.Type == "SYMLINK" && r.Target == "target"
				}
			}
			if !found {
				t.Fatal("late link was overwritten")
			}
			journals := 0
			err = filepath.WalkDir(filepath.Join(e.cache, e.vol.ID, "dirty"), func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.Name() == "0" {
					block, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					if sum(block[:len(data)]) != sum(data) {
						return fmt.Errorf("dirty data changed")
					}
					journals++
				}
				return nil
			})
			if err != nil || journals != 1 {
				t.Fatalf("pending bytes missing: %d %v", journals, err)
			}
			art.step("link appeared during block upload", e.logs.String(), map[string]any{"policy": policy, "remote": rows, "retained_sha256": sum(data)})
			art.pass()
		})
	}
}
