//go:build e2e && darwin

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// Failure cases, specified before implementation:
//   - overhead shifts chunk boundaries, truncates appends or corrupts partial writes;
//   - rename leaves descendant names bound to the old parent;
//   - a fresh machine depends on a marker, local cache, sidecar or saved key;
//   - labels, empty-directory markers or manifests leak plaintext;
//   - a wrong key mounts ciphertext, or a locked mount accepts plaintext writes;
//   - corruption is returned as bytes, or a crash forgets journaled changes;
//   - remote downloads bypass the encryption refusal and start compute;
//   - a failed recovery overwrites existing local files.
//
// Each completed scenario and the raw Modal listing are saved even on failure.
func TestEncryptedVolume(t *testing.T) {
	root, err := os.MkdirTemp("", "mfs-enc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	backend := startBackend(t, root, modalfake.Options{Latency: 5 * time.Millisecond, UpBytesPerSec: 12 * MiB, DownBytesPerSec: 12 * MiB})
	profile, modalConfig := "fake", filepath.Join(root, "modal.toml")
	if live() {
		profile, modalConfig = "topsoil-drakes", modalapi.ModalConfigPath()
	} else {
		config := fmt.Sprintf("[fake]\ntoken_id = \"ak-fake\"\ntoken_secret = \"as-fake\"\nserver_url = %q\n", strings.TrimPrefix(backend.env[0], "MODAL_SERVER_URL="))
		if err := os.WriteFile(modalConfig, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	artifactName := "encryption-fake.json"
	if live() {
		artifactName = "encryption-live.json"
	}
	art := newArtifact(t, artifactName)
	bin := modalfsBinary(t)
	a := startDaemon(t, bin, filepath.Join(root, "a"), modalConfig)
	a.must("account", "import", profile)
	name := backend.vol.Name + "-enc"
	out := a.must("volume", "create", name, "--encrypt", "--yes")
	var key string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "mfs1-") {
			key = strings.TrimSpace(line)
		}
	}
	if key == "" {
		t.Fatalf("create didn't display the key: %s", out)
	}
	ctx := context.Background()
	vol, err := backend.client.LookupVolume(ctx, name, false)
	if err != nil {
		t.Fatal(err)
	}
	// Stop both daemons before deleting the scratch volume.
	var b, c *daemonProc
	t.Cleanup(func() {
		a.quit()
		if b != nil {
			b.quit()
		}
		if c != nil {
			c.quit()
		}
		if err := backend.client.DeleteVolume(ctx, vol.ID); err != nil {
			t.Errorf("delete encrypted scratch volume %s: %v", name, err)
		} else if _, err := backend.client.LookupVolume(ctx, name, false); !modalapi.IsNotFound(err) {
			t.Errorf("verify scratch volume deletion %s: %v", name, err)
		} else {
			art.step("delete encrypted scratch volume", "deleted and confirmed absent", map[string]any{"id": vol.ID, "name": name})
		}
	})
	rawListing := func() []modalapi.Entry {
		t.Helper()
		es, err := backend.client.List(ctx, vol.ID, "", true)
		if err != nil {
			t.Fatal(err)
		}
		return es
	}
	t.Cleanup(func() {
		es, err := backend.client.List(ctx, vol.ID, "", true)
		if err != nil {
			t.Errorf("final raw listing: %v", err)
		}
		art.step("final raw Modal listing", fmt.Sprint(err), map[string]any{"entries": es})
	})
	art.step("create encrypted volume", "key displayed before creation; volume created", map[string]any{"volume": vol, "profile": profile, "live": live()})
	if got := strings.TrimSpace(a.must("volume", "key", name)); got != key {
		t.Fatal("remembered key differs from the displayed key")
	}
	a.must("mount", name, a.mnt)
	if out, err := a.cli("lock", name); err == nil || !strings.Contains(out, "unmount") {
		t.Fatalf("lock while mounted: %s (%v)", out, err)
	}
	art.step("lock refuses a mounted volume", "key retained while mounted", nil)
	emptyDirs := []string{"empty-folder/sub"}
	if err := os.MkdirAll(filepath.Join(a.mnt, emptyDirs[0]), 0o700); err != nil {
		t.Fatal(err)
	}
	const chunk = modalapi.BlockSize - 48
	expected := map[string][]byte{}
	for _, n := range []int{0, 17, chunk - 1, chunk, chunk + 1, 2*chunk + 113} {
		p := fmt.Sprintf("private-file-%d.bin", n)
		data := randomBytes(t, n)
		writeEncryptedFile(t, a.mnt, p, data)
		expected[p] = data
	}
	longName := strings.Repeat("x", 147)
	writeEncryptedFile(t, a.mnt, longName, randomBytes(t, 64*MiB))
	expected[longName], err = os.ReadFile(filepath.Join(a.mnt, longName))
	if err != nil {
		t.Fatal(err)
	}
	tooLong := filepath.Join(a.mnt, longName+"x")
	if err := os.WriteFile(tooLong, []byte("too long"), 0o600); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("overlength create: %v; want name too long", err)
	}
	if err := os.Mkdir(tooLong, 0o700); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("overlength mkdir: %v; want name too long", err)
	}
	if err := os.Rename(filepath.Join(a.mnt, "private-file-17.bin"), tooLong); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("overlength rename: %v; want name too long", err)
	}
	var nameEvents []struct{ Kind, Message string }
	daemonJSON(t, a, "GET", "/api/events", nil, &nameEvents)
	explained := false
	for _, e := range nameEvents {
		if e.Kind == "error" && strings.Contains(e.Message, "147 bytes") && strings.Contains(e.Message, "shorten") {
			explained = true
		}
	}
	if !explained {
		t.Fatal("encrypted name limit wasn't explained to the user")
	}
	a.must("sync", name)
	art.step("maximum filename and sidecar", "147-byte name synced; create/mkdir/rename of a 148-byte name returned ENAMETOOLONG with visible explanation", nil)
	a.must("sync", name)
	art.step("write chunk boundaries", "0, 17, chunk-1, chunk, chunk+1, 2*chunk+113 synced", nil)
	for p, data := range expected {
		if len(data) == 0 {
			continue
		}
		f, err := os.OpenFile(filepath.Join(a.mnt, p), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		offset := min(len(data)/2, chunk-3)
		patch := []byte("EDIT")
		if _, err := f.WriteAt(patch, int64(offset)); err != nil {
			t.Fatal(err)
		}
		copy(data[offset:], patch)
		tail := []byte("appended private bytes\x00\x00")
		if _, err := f.WriteAt(tail, int64(len(data))); err != nil {
			t.Fatal(err)
		}
		if err := fsync.File(f); err != nil {
			t.Fatal(err)
		}
		f.Close()
		expected[p] = append(data, tail...)
	}
	if err := os.MkdirAll(filepath.Join(a.mnt, "private-folder", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeEncryptedFile(t, a.mnt, "private-folder/nested/秘密.txt", []byte("private contents, never sent in clear"))
	if err := os.Rename(filepath.Join(a.mnt, "private-folder"), filepath.Join(a.mnt, "renamed-folder")); err != nil {
		t.Fatal(err)
	}
	expected["renamed-folder/nested/秘密.txt"] = []byte("private contents, never sent in clear")
	original := "private-file-17.bin"
	if err := os.Rename(filepath.Join(a.mnt, original), filepath.Join(a.mnt, "renamed.bin")); err != nil {
		t.Fatal(err)
	}
	expected["renamed.bin"] = expected[original]
	delete(expected, original)
	writeEncryptedFile(t, a.mnt, "copy.bin", expected["renamed.bin"])
	expected["copy.bin"] = bytes.Clone(expected["renamed.bin"])
	writeEncryptedFile(t, a.mnt, "delete-me.txt", []byte("delete me"))
	a.must("sync", name)
	if err := os.Remove(filepath.Join(a.mnt, "delete-me.txt")); err != nil {
		t.Fatal(err)
	}
	a.must("sync", name)
	verifyEncryptedFiles(t, a.mnt, expected, emptyDirs...)
	art.step("partial overwrite, append, file and folder rename, copy, delete", "exact bytes through mount", nil)
	label := "Secret research label 私密"
	a.must("volume", "label", name, label)
	es := rawListing()
	var damagePath, emptyPath, sidecarPath string
	var sidecarRaw []byte
	var emptyRaw []byte
	rawHashes := map[string]string{}
	rawNames := map[string]string{}
	decoded := map[string]bool{}
	refKey := referenceKey(t, key)
	for _, e := range es {
		if strings.Contains(e.Path, "private") || strings.Contains(e.Path, "renamed") || strings.Contains(e.Path, "秘密") || strings.HasSuffix(e.Path, "volume.json") {
			t.Fatalf("plaintext filename leaked: %s", e.Path)
		}
		if e.IsDir || e.Path == ".modalfs/encryption.json" {
			continue
		}
		data, err := backend.client.ReadFile(ctx, vol.ID, e.Path, 128*MiB)
		if err != nil {
			t.Fatal(err)
		}
		rawHashes[e.Path] = sum(data)
		plainName := referenceName(t, refKey, e.Path)
		rawNames[plainName] = e.Path
		plain := referenceContents(t, refKey, data)
		if plainName == ".modalfs/manifests/"+longName {
			sidecarPath, sidecarRaw = e.Path, bytes.Clone(data)
		}
		if plainName == "private-file-0.bin" {
			emptyPath, emptyRaw = e.Path, bytes.Clone(data)
		}
		if plainName == ".modalfs/volume.json" {
			if !bytes.Contains(plain, []byte(label)) {
				t.Fatal("reference decoder couldn't recover label")
			}
		} else if !strings.HasPrefix(plainName, ".modalfs/") && filepath.Base(plainName) != ".modalfs_keep" {
			want, ok := expected[plainName]
			if !ok || !bytes.Equal(plain, want) {
				t.Fatalf("reference recovery differs for %s", plainName)
			}
			decoded[plainName] = true
		}
		if bytes.Contains(data, []byte(label)) || bytes.Contains(data, []byte("private contents")) {
			t.Fatalf("plaintext content leaked: %s", e.Path)
		}
		if len(data) > modalapi.BlockSize && !strings.HasPrefix(e.Path, ".modalfs/") {
			damagePath = e.Path
		}
	}
	if len(decoded) != len(expected) {
		t.Fatal("independent decoder missed files")
	}
	art.step("independent decoder using only key and raw files", "all file names, contents and label match; no production format helpers", map[string]any{"ciphertext_sha256": rawHashes})
	art.step("raw storage confidentiality and encrypted label", "no plaintext names, contents or label", map[string]any{"entries": es})
	a.must("download", "enable")
	out, err = a.cli("download", "add", name, "https://example.com/encryption-test")
	if err == nil || !strings.Contains(out, "encrypted") {
		t.Fatalf("remote download wasn't refused: %s (%v)", out, err)
	}
	art.step("remote download refused", out, nil)
	a.must("unmount", name)
	a.must("lock", name)
	if out := a.must("status"); strings.Contains(out, label) {
		t.Fatal("locked status exposed cached label")
	}
	secretBytes, err := os.ReadFile(filepath.Join(a.home, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(secretBytes, []byte(key)) {
		t.Fatal("lock left the remembered key in secrets.json")
	}
	art.step("lock forgets remembered key and hides cached label", "secret removed; Modal name shown", nil)
	out, err = a.cli("mount", name, a.mnt)
	if err == nil || !strings.Contains(out, "locked") {
		t.Fatalf("locked mount wasn't refused: %s (%v)", out, err)
	}
	art.step("locked volume refuses mounting", out, nil)
	b = startDaemon(t, bin, filepath.Join(root, "b"), modalConfig)
	b.must("account", "import", profile)
	out, err = encryptionCLI(b, key[:len(key)-1]+"!", "unlock", name)
	if err == nil || !strings.Contains(out, "checksum") {
		t.Fatalf("mistyped key wasn't clearly refused: %s (%v)", out, err)
	}
	art.step("mistyped key refused", out, nil)
	var another struct {
		Key string `json:"key"`
	}
	daemonJSON(t, b, "POST", "/api/encryption/key", nil, &another)
	out, err = encryptionCLI(b, another.Key, "unlock", name)
	if err == nil || !strings.Contains(out, "right key") {
		t.Fatalf("another volume key wasn't refused: %s (%v)", out, err)
	}
	art.step("valid key for another volume refused", out, nil)
	if sidecarPath == "" {
		t.Fatal("large file sidecar missing")
	}
	sidecarRaw = sidecarRaw[:4]
	putRemote(t, backend.client, vol.ID, sidecarPath, sidecarRaw)
	if out, err = encryptionCLI(b, key, "unlock", name, "--remember"); err != nil {
		t.Fatalf("fresh unlock: %s (%v)", out, err)
	}
	b.must("mount", name, b.mnt)
	patchFile, err := os.OpenFile(filepath.Join(b.mnt, longName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	patch := bytes.Repeat([]byte{0x75}, chunk)
	if _, err := patchFile.WriteAt(patch, int64(4*chunk)); err != nil {
		t.Fatal(err)
	}
	if err := fsync.File(patchFile); err != nil {
		t.Fatal(err)
	}
	if err := patchFile.Close(); err != nil {
		t.Fatal(err)
	}
	copy(expected[longName][4*chunk:], patch)
	b.must("sync", name)
	repairedSidecar, err := backend.client.ReadFile(ctx, vol.ID, sidecarPath, 1*MiB)
	if err != nil {
		t.Fatal(err)
	}
	referenceContents(t, refKey, repairedSidecar)
	var sidecarEvents []struct{ Kind, Message string }
	daemonJSON(t, b, "GET", "/api/events", nil, &sidecarEvents)
	reported := false
	for _, e := range sidecarEvents {
		if e.Kind == "error" && strings.Contains(e.Message, "rebuild hashes") {
			reported = true
		}
	}
	if !reported {
		t.Fatal("corrupt sidecar reconstruction was not reported to the user")
	}
	art.step("truncated optional sidecar does not prevent safe writes", "fresh client reports error, edits authenticated source blocks, and replaces sidecar", map[string]any{"rebuild_reported": reported})
	verifyEncryptedFiles(t, b.mnt, expected, emptyDirs...)
	if out := b.must("volume", "label", name); !strings.Contains(out, label) {
		t.Fatalf("fresh client didn't decrypt label: %s", out)
	}
	art.step("fresh client unlocks and reads every file and label", "exact bytes", nil)
	// A durable local write must survive SIGKILL with an upload still pending.
	b.must("unmount", name)
	var state struct {
		Mounts []struct {
			ID string `json:"id"`
		} `json:"mounts"`
	}
	daemonJSON(t, b, "GET", "/api/state", nil, &state)
	daemonJSON(t, b, "PATCH", "/api/mounts/"+state.Mounts[0].ID, map[string]any{"write_back_ms": 3600000}, nil)
	b.must("mount", name, b.mnt)
	ours := bytes.Clone(expected[longName])
	ours[0] ^= 1
	conflictFile, err := os.OpenFile(filepath.Join(b.mnt, longName), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conflictFile.WriteAt(ours[:1], 0); err != nil {
		t.Fatal(err)
	}
	if err := fsync.File(conflictFile); err != nil {
		t.Fatal(err)
	}
	if err := conflictFile.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backend.client.Copy(ctx, vol.ID, []string{rawNames["copy.bin"]}, rawNames[longName], false); err != nil {
		t.Fatal(err)
	}
	b.must("sync", name)
	conflictName := ""
	for _, e := range rawListing() {
		if e.IsDir || strings.HasPrefix(e.Path, ".modalfs/") {
			continue
		}
		plain := referenceName(t, refKey, e.Path)
		if strings.Contains(plain, ".conflict-") {
			conflictName = plain
		}
	}
	if conflictName == "" || len(conflictName) > 147 {
		t.Fatalf("long-name conflict wasn't saved: %q", conflictName)
	}
	expected[longName] = bytes.Clone(expected["copy.bin"])
	expected[conflictName] = ours
	art.step("maximum-length filename conflict keeps both versions", conflictName, nil)
	crashData := randomBytes(t, chunk+71)
	writeEncryptedFile(t, b.mnt, "crash-write.bin", crashData)
	expected["crash-write.bin"] = crashData
	if err := b.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = b.cmd.Wait()
	b.cmd = nil
	if mounted(b.mnt) {
		if err := exec.Command("/sbin/umount", "-f", b.mnt).Run(); err != nil {
			t.Fatal(err)
		}
	}
	b.start()
	b.must("mount", name, b.mnt)
	b.must("sync", name)
	verifyEncryptedFiles(t, b.mnt, expected, emptyDirs...)
	art.step("crash mid-write recovers journal", "exact bytes after SIGKILL and restart", nil)
	b.must("unmount", name)
	b.must("lock", name)
	if err := backend.client.Remove(ctx, vol.ID, ".modalfs", true); err != nil {
		t.Fatal(err)
	}
	putRemote(t, backend.client, vol.ID, rawNames[".modalfs_keep"], []byte("broken sentinel"))
	// Another tool (modal volume put, a Function) can put a plaintext file
	// in an encrypted volume. It must not lock the user out of the rest.
	const stray = "put-by-another-tool.txt"
	putRemote(t, backend.client, vol.ID, stray, []byte("not encrypted"))
	c = startDaemon(t, bin, filepath.Join(root, "c"), modalConfig)
	c.must("account", "import", profile)
	if out, err = encryptionCLI(c, key, "unlock", name); err != nil {
		t.Fatalf("markerless unlock with a stray plaintext file: %s (%v)", out, err)
	}
	c.must("mount", name, c.mnt)
	verifyEncryptedFiles(t, c.mnt, expected, emptyDirs...)
	var strayEvents []struct{ Kind, Message string }
	daemonJSON(t, c, "GET", "/api/events", nil, &strayEvents)
	strayReported := false
	for _, e := range strayEvents {
		if e.Kind == "error" && strings.Contains(e.Message, stray) {
			strayReported = true
		}
	}
	if !strayReported {
		t.Fatalf("stray plaintext file wasn't reported: %+v", strayEvents)
	}
	strayDest := filepath.Join(root, "with-stray")
	out, err = encryptionCLI(c, key, "volume", "decrypt", name, "--to", strayDest)
	if err == nil || !strings.Contains(out, stray) {
		t.Fatalf("recovery didn't report the stray file: %s (%v)", out, err)
	}
	verifyEncryptedFiles(t, strayDest, expected, emptyDirs...)
	if err := backend.client.Remove(ctx, vol.ID, stray, false); err != nil {
		t.Fatal(err)
	}
	// In a folder, such a file keeps the folder from being moved (it would
	// be left behind) or deleted (with it): modalfs can't show it.
	folderRaw := path.Dir(path.Dir(rawNames["renamed-folder/nested/秘密.txt"]))
	subRaw := path.Dir(rawNames["empty-folder/sub/.modalfs_keep"])
	if folderRaw == "." || subRaw == "." {
		t.Fatal("the raw names of the folders weren't found")
	}
	strays := []string{folderRaw + "/" + stray, subRaw + "/" + stray}
	for _, p := range strays {
		putRemote(t, backend.client, vol.ID, p, []byte("not encrypted"))
	}
	if err := os.Rename(filepath.Join(c.mnt, "renamed-folder"), filepath.Join(c.mnt, "moved-folder")); err == nil {
		t.Fatal("moved a folder holding a file that doesn't decrypt")
	}
	if _, err := os.Stat(filepath.Join(c.mnt, "moved-folder")); !os.IsNotExist(err) {
		t.Fatalf("the refused move left a folder behind: %v", err)
	}
	if err := os.Remove(filepath.Join(c.mnt, "empty-folder", "sub")); err == nil {
		t.Fatal("deleted a folder holding a file that doesn't decrypt")
	}
	for _, p := range strays {
		if _, err := backend.client.ReadFile(ctx, vol.ID, p, 100); err != nil {
			t.Fatalf("%s after the refused move and delete: %v", p, err)
		}
	}
	verifyEncryptedFiles(t, c.mnt, expected, emptyDirs...)
	daemonJSON(t, c, "GET", "/api/events", nil, &strayEvents)
	moveExplained, deleteExplained := false, false
	for _, e := range strayEvents {
		moveExplained = moveExplained || e.Kind == "error" && strings.Contains(e.Message, "renamed-folder wasn't moved") && strings.Contains(e.Message, stray)
		deleteExplained = deleteExplained || e.Kind == "error" && strings.Contains(e.Message, "empty-folder/sub wasn't deleted") && strings.Contains(e.Message, stray)
	}
	if !moveExplained || !deleteExplained {
		t.Fatalf("the refused move or delete wasn't explained: %+v", strayEvents)
	}
	for _, p := range strays {
		if err := backend.client.Remove(ctx, vol.ID, p, false); err != nil {
			t.Fatal(err)
		}
	}
	art.step("a folder holding a file that doesn't decrypt", "moving and deleting it are refused and explained; the file and the folder's contents stay", nil)
	art.step("a plaintext file put by another tool", "unlock, mount and recovery still give every encrypted file; the stray file is reported", map[string]any{"recovery": out})
	alias := filepath.Join(root, "mount-alias")
	if err := os.Symlink(c.mnt, alias); err != nil {
		t.Fatal(err)
	}
	out, err = encryptionCLI(c, key, "volume", "decrypt", name, "--to", filepath.Join(alias, "recovered"))
	if err == nil || !strings.Contains(out, "outside modalfs mounts") {
		t.Fatalf("symlink into served mount wasn't refused: %s (%v)", out, err)
	}
	art.step("recovery refuses a symlink into its own mount", out, nil)
	verifyEncryptedFiles(t, c.mnt, expected, emptyDirs...)
	c.must("unmount", name)
	c.must("lock", name)
	art.step("delete all .modalfs metadata and damage root sentinel; fresh unlock and mount", "every file and empty folder recovered using key and data", nil)
	if err := backend.client.Remove(ctx, vol.ID, ".modalfs", true); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "decrypted")
	if out, err = encryptionCLI(c, key, "volume", "decrypt", name, "--to", dest); err != nil {
		t.Fatalf("mountless recovery: %s (%v)", out, err)
	}
	verifyEncryptedFiles(t, dest, expected, emptyDirs...)
	checksums := map[string]string{}
	for p, data := range expected {
		checksums[p] = sum(data)
	}
	art.step("mountless recovery without marker or remembered key", "every file byte-for-byte", map[string]any{"sha256": checksums})
	out, err = encryptionCLI(c, key, "volume", "decrypt", name, "--to", dest)
	if err == nil {
		t.Fatal("decrypt accepted an existing nonempty destination")
	}
	verifyEncryptedFiles(t, dest, expected, emptyDirs...)
	art.step("recovery refuses to overwrite local files", out, nil)
	putRemote(t, backend.client, vol.ID, ".modalfs/encryption.json", []byte("broken marker"))
	c.stop()
	configPath := filepath.Join(c.home, "config.json")
	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var localConfig map[string]any
	if err := json.Unmarshal(rawConfig, &localConfig); err != nil {
		t.Fatal(err)
	}
	for _, record := range localConfig["encryption"].([]any) {
		record.(map[string]any)["secret_store"] = "file"
	}
	rawConfig, err = json.Marshal(localConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	c.start()
	independentDest := filepath.Join(root, "without-state")
	if out, err = encryptionCLI(c, key, "volume", "decrypt", name, "--to", independentDest); err != nil {
		t.Fatalf("recovery depended on marker or stored key: %s (%v)", out, err)
	}
	verifyEncryptedFiles(t, independentDest, expected, emptyDirs...)
	art.step("recovery ignores malformed marker and missing remembered secret", "key and raw data suffice even with unusable metadata", nil)
	if damagePath == "" {
		t.Fatal("no multiblock file to damage")
	}
	damaged, err := backend.client.ReadFile(ctx, vol.ID, damagePath, 128*MiB)
	if err != nil {
		t.Fatal(err)
	}
	damaged[len(damaged)-1] ^= 1
	putRemote(t, backend.client, vol.ID, damagePath, damaged)
	out, err = encryptionCLI(c, key, "volume", "decrypt", name, "--to", filepath.Join(root, "damaged"))
	if err == nil || !strings.Contains(out, "authentication") {
		t.Fatalf("corruption wasn't reported: %s (%v)", out, err)
	}
	art.step("damaged ciphertext is an error", out, nil)
	if err := backend.client.Remove(ctx, vol.ID, ".modalfs/encryption.json", false); err != nil {
		t.Fatal(err)
	}
	if out, err := encryptionCLI(c, key, "unlock", name); err != nil {
		t.Fatalf("unlock with a damaged later block: %s (%v)", out, err)
	}
	c.must("mount", name, c.mnt)
	plainDamagePath := referenceName(t, refKey, damagePath)
	if _, err := os.ReadFile(filepath.Join(c.mnt, plainDamagePath)); err == nil {
		t.Fatal("mount returned damaged block as data")
	}
	c.must("unmount", name)
	art.step("mount refuses damaged block", "read returned an error from a fresh mount", nil)
	if emptyPath == "" {
		t.Fatal("no authenticated empty file found")
	}
	emptyRaw[len(emptyRaw)-1] ^= 1
	putRemote(t, backend.client, vol.ID, emptyPath, emptyRaw)
	out, err = c.cli("mount", name, c.mnt)
	if err == nil {
		if _, err := os.ReadFile(filepath.Join(c.mnt, "private-file-0.bin")); err == nil {
			t.Fatal("mount treated damaged authenticated empty file as empty")
		}
		c.must("unmount", name)
	} else if !strings.Contains(out, "authentication") {
		t.Fatalf("unexpected mount error: %s", out)
	}
	art.step("empty file authentication", "damaged empty envelope was an error, not EOF", nil)
	art.pass()
}

func encryptionCLI(p *daemonProc, input string, args ...string) (string, error) {
	cmd := exec.Command(p.bin, args...)
	cmd.Env, cmd.Dir = p.env, "/"
	cmd.Stdin = strings.NewReader(input + "\n")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeEncryptedFile(t *testing.T, root, name string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, filepath.FromSlash(name)), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := fsync.File(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func verifyEncryptedFiles(t *testing.T, root string, expected map[string][]byte, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		st, err := os.Stat(filepath.Join(root, dir))
		if err != nil || !st.IsDir() {
			t.Fatalf("empty directory %s missing: %v", dir, err)
		}
	}
	for p, want := range expected {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: bytes differ or unreadable: %v (got %d, want %d)", p, err, len(got), len(want))
		}
	}
	var actual []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !strings.HasPrefix(d.Name(), "._") && d.Name() != ".DS_Store" {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			actual = append(actual, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil || len(actual) != len(expected) {
		b, _ := json.Marshal(actual)
		t.Fatalf("file set differs: %s (%v)", b, err)
	}
}

func daemonJSON(t *testing.T, p *daemonProc, method, path string, input, output any) {
	t.Helper()
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", filepath.Join(p.home, "modalfsd.sock"))
	}}}
	defer hc.CloseIdleConnections()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, "http://modalfsd"+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%s %s: %s (%v)", method, path, raw, err)
	}
	if output != nil {
		if err := json.Unmarshal(raw, output); err != nil {
			t.Fatal(err)
		}
	}
}
