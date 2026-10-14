//go:build e2e && darwin

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// Volume labels, as two people on two machines would see them: two daemons,
// each with its own home and its own mount of the same volume on the fake
// Modal. The label lives in the volume (.modalfs/volume.json), so what one
// sets, the other shows.
//
// What could go wrong, and where it's checked:
//   - the label stays on the machine that set it                  → B sees A's label
//   - B never picks up a change (cached forever)                   → B sees A's second label and the clear
//   - a restart forgets it until the network answers, or loses it  → B after restarting
//   - text is mangled (Unicode, emoji, spaces) or not trimmed       → the exact bytes on Modal
//   - junk is accepted (too long, a newline)                        → both refused, Modal unchanged
//   - a read-only mount writes into the volume anyway               → refused, Modal unchanged
//   - writing the label clobbers other records under .modalfs/      → a download record survives byte for byte
//   - .modalfs/ shows up in the user's folder                       → the mount's root listing
//   - the CLI doesn't show it                                       → status and info
//
// The scenario's steps and what each client saw are written to
// labels.json in MODALFS_E2E_ARTIFACTS (default: $TMPDIR/modalfs-e2e).
func TestVolumeLabelSharedThroughTheVolume(t *testing.T) {
	if live() {
		t.Skip("runs on the fake: it starts two daemons with their own homes")
	}
	bin := modalfsBinary(t)
	root, err := os.MkdirTemp("", "mfs-lbl-") // short: socket paths are limited to 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	fake, err := modalfake.Start(filepath.Join(root, "blocks"), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	api, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	ctx := context.Background()
	vol, err := api.LookupVolume(ctx, "e2e", true)
	if err != nil {
		t.Fatal(err)
	}
	// Another client's record under .modalfs/, which a label must not touch.
	record := []byte(`{"state":"done","note":"written by a remote download"}`)
	putFile(t, api, vol.ID, ".modalfs/downloads/dl-1/status.json", record)

	toml := filepath.Join(root, "modal.toml")
	if err := os.WriteFile(toml, []byte(fmt.Sprintf("[fake]\ntoken_id = \"ak-fake\"\ntoken_secret = \"as-fake\"\nserver_url = %q\nactive = true\n", fake.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	art := newArtifact(t, "labels.json")
	a := startDaemon(t, bin, filepath.Join(root, "a"), toml)
	b := startDaemon(t, bin, filepath.Join(root, "b"), toml)
	for _, c := range []*daemonProc{a, b} {
		c.must("account", "import", "fake")
		c.must("mount", "e2e", c.mnt)
	}
	onModal := func() map[string]any {
		data, err := api.ReadFile(ctx, vol.ID, ".modalfs/volume.json", 1<<20)
		if err != nil {
			if modalapi.IsNotFound(err) {
				return nil
			}
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatalf("volume.json on Modal isn't JSON: %v: %q", err, data)
		}
		return v
	}

	first := "  Training data · 2024 🚀  "
	a.must("volume", "label", "e2e", first)
	want := strings.TrimSpace(first)
	if got := onModal(); got == nil || got["label"] != want {
		t.Fatalf("on Modal after A set the label: %v, want label %q", got, want)
	}
	art.step("A sets the label", a.must("volume", "label", "e2e"), onModal())
	b.waitForLabel(want, "", art, "B shows A's label")

	second := "Vision datasets"
	a.must("volume", "label", "e2e", second)
	b.waitForLabel(second, want, art, "B shows A's new label")

	for _, bad := range []string{strings.Repeat("x", 81), "two\nlines"} {
		out, err := a.cli("volume", "label", "e2e", bad)
		if err == nil {
			t.Fatalf("A set the label %q; want it refused", bad)
		}
		art.step(fmt.Sprintf("A tries the label %q", bad), out, onModal())
	}
	if got := onModal(); got["label"] != second {
		t.Fatalf("a refused label changed Modal: %v", got)
	}

	b.restart()
	if out := b.must("status"); !strings.Contains(out, second) {
		t.Fatalf("B's status right after a restart doesn't show %q:\n%s", second, out)
	}
	art.step("B restarted", b.must("status"), onModal())

	// A read-only mount doesn't write to the volume, its label included.
	b.must("forget", "e2e")
	b.must("mount", "e2e", b.mnt, "-ro")
	out, err := b.cli("volume", "label", "e2e", "from a read-only mount")
	if err == nil || !strings.Contains(out, "read-only") {
		t.Fatalf("B set a label through a read-only mount (err %v):\n%s", err, out)
	}
	if got := onModal(); got["label"] != second {
		t.Fatalf("the read-only refusal changed Modal: %v", got)
	}
	art.step("B (read-only) tries to set the label", out, onModal())

	a.must("volume", "label", "e2e", "--clear")
	b.waitForLabel("e2e", second, art, "A clears the label; B shows none")

	got, err := api.ReadFile(ctx, vol.ID, ".modalfs/downloads/dl-1/status.json", 1<<20)
	if err != nil || string(got) != string(record) {
		t.Fatalf("the download record under .modalfs/ changed: %q, %v", got, err)
	}
	entries, err := os.ReadDir(a.mnt)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == ".modalfs" {
			t.Fatal(".modalfs shows in the mounted folder")
		}
	}
	art.step("download record and folder checked", "record intact; .modalfs hidden", onModal())
	art.pass()
}

// daemonProc is a modalfs daemon with a home of its own, and its CLI.
type daemonProc struct {
	t    *testing.T
	bin  string
	home string
	mnt  string // where it mounts the volume: outside its home, which modalfs refuses
	env  []string
	cmd  *exec.Cmd
	logs *syncBuffer
}

func startDaemon(t *testing.T, bin, home, modalConfig string) *daemonProc {
	t.Helper()
	if err := os.MkdirAll(home+"-mnt", 0o700); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "MODALFS_HOME="+home, "MODALFS_NO_KEYCHAIN=1", "MODAL_CONFIG_PATH="+modalConfig,
		"MODAL_TOKEN_ID=", "MODAL_TOKEN_SECRET=", "MODAL_SERVER_URL=", "MODAL_PROFILE=")
	p := &daemonProc{t: t, bin: bin, home: home, mnt: home + "-mnt", env: env}
	p.start()
	t.Cleanup(p.quit)
	return p
}

func (p *daemonProc) start() {
	p.t.Helper()
	p.logs = &syncBuffer{}
	p.cmd = exec.Command(p.bin, "daemon")
	p.cmd.Env, p.cmd.Dir = p.env, "/"
	p.cmd.Stdout, p.cmd.Stderr = p.logs, p.logs
	if err := p.cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	for i := 0; ; i++ {
		if _, err := os.Stat(filepath.Join(p.home, "modalfsd.sock")); err == nil {
			if _, err := p.cli("status"); err == nil {
				return
			}
		}
		if i == 300 {
			p.t.Fatalf("daemon in %s didn't start:\n%s", p.home, p.logs.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// restart stops the daemon (unmounting) and starts it again with the same home.
func (p *daemonProc) restart() {
	p.t.Helper()
	p.stop()
	p.start()
}

func (p *daemonProc) stop() {
	if p.cmd == nil {
		return
	}
	_, _ = p.cli("quit")
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		_ = p.cmd.Process.Signal(syscall.SIGKILL)
		<-done
		p.t.Errorf("daemon in %s didn't quit within 2 minutes", p.home)
	}
	if out := p.logs.String(); strings.Contains(out, "DATA RACE") || strings.Contains(out, "panic") {
		p.t.Errorf("daemon in %s reported a data race or a panic:\n%s", p.home, out)
	}
	p.cmd = nil
}

func (p *daemonProc) quit() {
	p.stop()
	if mounted(p.mnt) {
		_ = exec.Command("/sbin/umount", "-f", p.mnt).Run()
	}
}

func (p *daemonProc) cli(args ...string) (string, error) {
	cmd := exec.Command(p.bin, args...)
	cmd.Env, cmd.Dir = p.env, "/"
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *daemonProc) must(args ...string) string {
	p.t.Helper()
	out, err := p.cli(args...)
	if err != nil {
		p.t.Fatalf("modalfs %s (home %s): %v\n%s", strings.Join(args, " "), p.home, err, out)
	}
	return out
}

// waitForLabel waits until the volume's row in `modalfs status` shows want
// and no longer shows gone, as someone watching the list would see it
// change. Asking for the status is also what the app and the menu bar do
// while they're open.
func (p *daemonProc) waitForLabel(want, gone string, art *artifact, step string) {
	p.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		out := p.must("status")
		row := ""
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "e2e ") {
				row = line
			}
		}
		if strings.Contains(row, want) && (gone == "" || !strings.Contains(row, gone)) {
			art.step(step, out, nil)
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("%s: after 30 s the status row is %q; want %q there and %q gone", step, row, want, gone)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// artifact records a scenario's steps as JSON, to check by hand or diff between runs.
type artifact struct {
	t     *testing.T
	path  string
	start time.Time
	Steps []artifactStep `json:"steps"`
	Pass  bool           `json:"pass"`
}

type artifactStep struct {
	Result  string         `json:"result"`
	AfterMs int64          `json:"after_ms"`
	Step    string         `json:"step"`
	Output  string         `json:"output"`
	OnModal map[string]any `json:"on_modal,omitempty"`
}

func newArtifact(t *testing.T, name string) *artifact {
	dir := os.Getenv("MODALFS_E2E_ARTIFACTS")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "modalfs-e2e")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &artifact{t: t, path: filepath.Join(dir, name), start: time.Now(), Steps: []artifactStep{}}
	t.Cleanup(a.write)
	return a
}

func (a *artifact) step(name, output string, onModal map[string]any) {
	a.Steps = append(a.Steps, artifactStep{Result: "pass", AfterMs: time.Since(a.start).Milliseconds(), Step: name, Output: strings.TrimSpace(output), OnModal: onModal})
}

func (a *artifact) pass() { a.Pass = true }

func (a *artifact) write() {
	a.Pass = a.Pass && !a.t.Failed()
	b, _ := json.MarshalIndent(a, "", "  ")
	if err := os.WriteFile(a.path, b, 0o644); err != nil {
		a.t.Error(err)
		return
	}
	a.t.Logf("scenario written to %s", a.path)
}

// A volume mounted in the mount root is in a folder named after its label,
// so Finder shows that name, and it follows a new label the next time the
// volume is mounted. A label that isn't a valid file name is made one, and
// two volumes with the same label get two folders.
func TestMountFolderFollowsTheLabel(t *testing.T) {
	if live() {
		t.Skip("runs on the fake: it starts a daemon with its own home")
	}
	bin := modalfsBinary(t)
	root, err := os.MkdirTemp("", "mfs-fol-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	fake, err := modalfake.Start(filepath.Join(root, "blocks"), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	api, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	for _, name := range []string{"e2e", "e2e-two"} {
		if _, err := api.LookupVolume(context.Background(), name, true); err != nil {
			t.Fatal(err)
		}
	}
	toml := filepath.Join(root, "modal.toml")
	if err := os.WriteFile(toml, []byte(fmt.Sprintf("[fake]\ntoken_id = \"ak-fake\"\ntoken_secret = \"as-fake\"\nserver_url = %q\nactive = true\n", fake.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	// The mount root in this test's own folder, never the user's ~/ModalVolumes.
	home, mounts := filepath.Join(root, "h"), filepath.Join(root, "m")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(fmt.Sprintf(`{"accounts":[],"mounts":[],"settings":{"mount_root":%q}}`, mounts)), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startDaemon(t, bin, home, toml)
	d.must("account", "import", "fake")
	waitMounted := func(p string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !mounted(p) {
			if time.Now().After(deadline) {
				t.Fatalf("not mounted at %s:\n%s", p, d.must("status"))
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	d.must("mount", "e2e")
	waitMounted(filepath.Join(mounts, "e2e"))

	// Labelled while mounted: it stays where it is until it's mounted again.
	d.must("volume", "label", "e2e", "Data: 2024/raw")
	if !mounted(filepath.Join(mounts, "e2e")) {
		t.Fatal("setting the label moved a mounted volume")
	}
	d.must("unmount", "e2e")
	d.must("mount", "e2e")
	labelled := filepath.Join(mounts, "Data- 2024-raw")
	waitMounted(labelled)
	if _, err := os.Stat(filepath.Join(mounts, "e2e")); !os.IsNotExist(err) {
		t.Fatalf("the old, empty folder is still there (err %v)", err)
	}

	// The same label on another volume: a second folder.
	d.must("mount", "e2e-two")
	waitMounted(filepath.Join(mounts, "e2e-two"))
	d.must("volume", "label", "e2e-two", "Data: 2024/raw")
	d.must("unmount", "e2e-two")
	d.must("mount", "e2e-two")
	waitMounted(labelled + " 2")
	if !mounted(labelled) {
		t.Fatal("mounting the second volume moved the first")
	}
}
