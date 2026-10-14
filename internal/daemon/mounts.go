package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/fusefront"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/nfsfront"
	"github.com/t41372/modalfs/internal/vfs"
)

// Mount states.
const (
	StateStopped  = "stopped"
	StateStarting = "starting"
	StateMounted  = "mounted"
	StateStopping = "stopping"
	StateError    = "error"
)

// Steps of a mount that is starting, for the UI.
const (
	StepConnect = "connect" // reaching Modal, looking up the volume
	StepOpen    = "open"    // opening the local cache and journal
	StepMount   = "mount"   // asking the OS to mount the folder
)

type runMount struct {
	id         string
	state      string
	err        string
	mode       string
	mountpoint string
	tablePath  string // mountpoint as the mount table names it
	startedAt  time.Time
	fs         *vfs.FS
	client     *modalapi.Client
	fuseSrv    *fuse.Server
	nfsL       net.Listener
	volID      string
	closing    bool // a stop got past unmount and is closing fs

	step    string    // starting: what it's doing
	retryAt time.Time // error: an auto-mount tries again then

	// Set when a stop begins, to show its upload progress.
	stopSince   time.Time
	stopBytesUp int64
	stopPending int64

	// Why the last unmount failed (an app has files open), and who.
	unmountErr string
	inUseBy    []string
}

// MountView is a saved mount plus its live state, as the UI sees it.
type MountView struct {
	config.Mount
	Encrypted     bool `json:"encrypted,omitempty"`
	Locked        bool `json:"locked,omitempty"`
	KeyRemembered bool `json:"key_remembered,omitempty"`
	// UpSpeed and DownSpeed are bytes per second uploaded and read, over the
	// last few seconds, while mounted.
	UpSpeed      int64       `json:"up_speed,omitempty"`
	DownSpeed    int64       `json:"down_speed,omitempty"`
	AccountLabel string      `json:"account_label"`
	Workspace    string      `json:"workspace"`
	State        string      `json:"state"`
	Error        string      `json:"error,omitempty"`
	ActiveMode   string      `json:"active_mode,omitempty"`
	StartedAt    int64       `json:"started_at,omitempty"`
	Status       *vfs.Status `json:"status,omitempty"` // while mounted or stopping
	// Starting: what it's doing now (connect, open, mount).
	Step string `json:"step,omitempty"`
	// Error: an auto-mount tries again at this time (Unix seconds).
	RetryAt int64 `json:"retry_at,omitempty"`
	// Stopping: when it began, and the upload counter and pending bytes then,
	// so a client can show what's left and how fast it goes.
	StoppingSince    int64 `json:"stopping_since,omitempty"`
	StopBytesUp      int64 `json:"stop_bytes_up,omitempty"`
	StopPendingBytes int64 `json:"stop_pending_bytes,omitempty"`
	// Stopping: the folder is already unmounted; uploads finish in the background.
	Unmounted bool `json:"unmounted,omitempty"`
	// Mounted: the last unmount failed, and the apps that had files open in it.
	UnmountError string   `json:"unmount_error,omitempty"`
	InUseBy      []string `json:"in_use_by,omitempty"`
	// Conflicts not marked handled yet (the vfs counter in Status counts them all).
	OpenConflicts int `json:"open_conflicts,omitempty"`
	// The volume's size from its last listing, and when that was.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	SizeAt    int64 `json:"size_at,omitempty"`
	// The volume's label (kept in the volume), "" when it has none.
	Label string `json:"label,omitempty"`
}

func defaultMode() string {
	if runtime.GOOS == "darwin" {
		return "nfs"
	}
	return "fuse"
}

func (d *Daemon) setState(id, state, msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.running[id]
	if r == nil {
		r = &runMount{id: id}
		d.running[id] = r
	}
	r.state, r.err = state, msg
}

func (d *Daemon) setStep(id string, ph *runMount, step string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running[id] == ph && ph.state == StateStarting {
		ph.step = step
	}
}

// StartMount mounts a saved mount. It returns once mounted or failed.
func (d *Daemon) StartMount(id string) error {
	ph, err := d.claimStart(id)
	if err != nil || ph == nil {
		return err
	}
	return d.runStart(id, ph, false)
}

// StartMountAsync marks a mount as starting and mounts it in the
// background; failures go to the mount's state and to the events.
func (d *Daemon) StartMountAsync(id string) error {
	ph, err := d.claimStart(id)
	if err != nil || ph == nil {
		return err
	}
	go func() { _ = d.runStart(id, ph, false) }()
	return nil
}

// claimStart marks a mount as starting and returns its placeholder, or nil
// when it's already mounted.
func (d *Daemon) claimStart(id string) (*runMount, error) {
	d.mu.Lock()
	if d.closing {
		d.mu.Unlock()
		return nil, errors.New("modalfs is shutting down; start it again to mount")
	}
	if r := d.running[id]; r != nil && (r.state == StateMounted || r.state == StateStarting || r.state == StateStopping) {
		state := r.state
		d.mu.Unlock()
		if state == StateMounted {
			return nil, nil
		}
		return nil, fmt.Errorf("mount is %s", state)
	}
	ph := &runMount{id: id, state: StateStarting, step: StepConnect, startedAt: time.Now()}
	d.running[id] = ph
	d.mu.Unlock()

	// Read the config only after claiming the mount: removing a mount checks
	// for "starting" under the config lock, so it either sees this claim or
	// has already removed the mount we look up here.
	if _, ok := d.store.Mount(id); !ok {
		d.mu.Lock()
		if d.running[id] == ph {
			delete(d.running, id)
		}
		d.mu.Unlock()
		return nil, fmt.Errorf("mount %q not found", id)
	}
	return ph, nil
}

// runStart mounts a claimed mount. quiet leaves reporting a failure to the
// caller (auto-mount retries).
func (d *Daemon) runStart(id string, ph *runMount, quiet bool) error {
	m, ok := d.store.Mount(id)
	if !ok {
		d.mu.Lock()
		if d.running[id] == ph {
			delete(d.running, id)
		}
		d.mu.Unlock()
		return fmt.Errorf("mount %q not found", id)
	}
	r, err := d.doStart(m, ph)
	if errors.Is(err, errLocked) && !quiet {
		// Not a failure to report: the volume shows as locked, with Unlock.
		d.mu.Lock()
		if d.running[id] == ph {
			delete(d.running, id)
		}
		d.mu.Unlock()
		return err
	}
	if err != nil {
		d.mu.Lock()
		if d.running[id] == ph {
			ph.state, ph.err, ph.step = StateError, err.Error(), ""
		}
		d.mu.Unlock()
		if !quiet {
			d.emitC("error", "mount_failed", id, m.VolumeName, "mount failed: "+err.Error(), err.Error())
		}
		return err
	}
	d.mu.Lock()
	d.running[id] = r
	d.mu.Unlock()
	d.saveRuntime()
	d.emitC("mounted", "mounted", id, m.VolumeName, r.mountpoint, r.mountpoint)
	return nil
}

func (d *Daemon) doStart(m config.Mount, ph *runMount) (_ *runMount, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, acct, err := d.newClient(m.AccountID, m.Environment)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			closeClient(c)
		}
	}()
	vol, err := d.resolveVolume(ctx, c, m)
	if err != nil {
		return nil, err
	}
	if !vol.V2() {
		return nil, fmt.Errorf("volume %s is %s; modalfs supports Volume v2 only", vol.Name, strings.TrimPrefix(vol.Version.String(), "VOLUME_FS_VERSION_"))
	}
	d.setStep(m.ID, ph, StepOpen)
	s := d.store.Snapshot().Settings
	if m.ReadOnly && hasJournals(s.CacheDir, vol.ID) {
		// vfs only commits a previous session's changes on a writable mount.
		d.emitC("info", "pending_kept_ro", m.ID, m.VolumeName, "changes from an earlier session are kept on this computer and are uploaded the next time this volume is mounted with write access")
	}
	wb := 3 * time.Second
	if m.WriteBackMS > 0 {
		wb = time.Duration(m.WriteBackMS) * time.Millisecond
	}
	mid, label := m.ID, m.VolumeName
	remote, err := d.openVolume(context.Background(), c, vol.ID)
	if err != nil {
		return nil, err
	}
	fsys, err := vfs.New(c, vol, vfs.Options{
		Cipher:   remote.Cipher,
		CacheDir: s.CacheDir, Cache: d.cache, DirCacheTTL: 10 * time.Second, WriteBack: wb,
		ReadAhead: s.ReadAhead, SidecarMin: 64 << 20, OnConflict: m.OnConflict, ReadOnly: m.ReadOnly,
		FilterJunk: m.FilterJunk, Logger: d.log.With("mount", m.VolumeName),
		OnEvent: func(kind, p, detail string) {
			switch kind {
			case "conflict":
				d.conflicts.add(mid, Conflict{Path: p, Copy: detail, At: time.Now().Unix()})
				d.emitC("conflict", "conflict", mid, label, fmt.Sprintf("%s changed remotely; your version saved as %s", p, detail), p, detail)
			case "error":
				d.emitC("error", "commit_failed", mid, label, p+": "+detail, p, detail)
			case "unreadable":
				d.emitC("error", "unreadable", mid, label, p+": "+detail, p, detail)
			case "recovered":
				d.emitC("recovered", "recovered", mid, label, fmt.Sprintf("recovered unsaved changes to %s (%s)", p, detail), p)
			case "commit":
				d.emitC("commit", "commit", mid, label, p+" · "+detail, p, detail)
			}
		},
	})
	if err != nil {
		return nil, err
	}
	d.setStep(m.ID, ph, StepMount)
	m = d.followLabel(m, vol.ID)
	mp := expandHome(m.Mountpoint)
	if err := prepareMountpoint(mp); err != nil {
		_ = fsys.Close()
		return nil, err
	}
	mode := m.Mode
	if mode == "" || mode == "auto" {
		mode = defaultMode()
	}
	r := &runMount{id: m.ID, state: StateMounted, mode: mode, mountpoint: mp, tablePath: tablePath(mp), startedAt: time.Now(), fs: fsys, client: c, volID: vol.ID}
	name := vol.Name
	if acct.Workspace != "" {
		name = acct.Workspace + "/" + vol.Name
	}
	switch mode {
	case "fuse":
		srv, err := fusefront.Mount(mp, fsys, name, m.ReadOnly, false)
		if err != nil {
			_ = fsys.Close()
			if runtime.GOOS == "darwin" {
				return nil, fmt.Errorf("FUSE mount failed (is macFUSE installed? NFS mode needs nothing extra): %w", err)
			}
			return nil, fmt.Errorf("FUSE mount failed: %w", err)
		}
		r.fuseSrv = srv
		go func() {
			srv.Wait()
			d.onExternalUnmount(m.ID, r)
		}()
	case "nfs":
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = fsys.Close()
			return nil, err
		}
		r.nfsL = l
		go func() { _ = nfsfront.Serve(l, fsys, m.ReadOnly) }()
		port := l.Addr().(*net.TCPAddr).Port
		if out, err := nfsMount(port, mp, m.ReadOnly); err != nil {
			_ = l.Close()
			_ = fsys.Close()
			return nil, fmt.Errorf("NFS mount failed: %v: %s", err, strings.TrimSpace(out))
		}
	default:
		_ = fsys.Close()
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	return r, nil
}

// hasJournals reports whether a volume has uncommitted changes from an
// earlier session in its dirty folder (see vfs/recover.go).
func hasJournals(cacheDir, volID string) bool {
	ents, err := os.ReadDir(filepath.Join(cacheDir, volID, "dirty"))
	return err == nil && len(ents) > 0
}

// resolveVolume finds the volume by name, following renames via the pinned
// ID and refusing to silently mount a different volume that reused the name.
func (d *Daemon) resolveVolume(ctx context.Context, c *modalapi.Client, m config.Mount) (modalapi.VolumeInfo, error) {
	vol, err := c.LookupVolume(ctx, m.VolumeName, false)
	if err == nil && (m.VolumeID == "" || vol.ID == m.VolumeID) {
		if m.VolumeID == "" {
			d.updateMount(m.ID, func(x *config.Mount) { x.VolumeID = vol.ID })
		}
		return vol, nil
	}
	if err != nil && (!modalapi.IsNotFound(err) || m.VolumeID == "") {
		return vol, friendly(err)
	}
	// The name is gone or now points elsewhere: follow the pinned volume ID,
	// wherever it went (renamed), and never mount a stranger by name.
	vs, lerr := c.ListVolumes(ctx)
	if lerr != nil {
		return vol, friendly(lerr)
	}
	for _, v := range vs {
		if v.ID == m.VolumeID {
			d.emitC("info", "follow_rename", m.ID, v.Name, fmt.Sprintf("volume was renamed %s → %s; mount follows it", m.VolumeName, v.Name), m.VolumeName, v.Name)
			d.updateMount(m.ID, func(x *config.Mount) { x.VolumeName = v.Name })
			return v, nil
		}
	}
	if err == nil {
		return vol, fmt.Errorf("the volume this mount was pinned to (%s) was deleted, and the name %q now belongs to a different volume (%s); re-pin the mount if that is intended", m.VolumeID, m.VolumeName, vol.ID)
	}
	return vol, fmt.Errorf("volume %q (%s) no longer exists", m.VolumeName, m.VolumeID)
}

func (d *Daemon) updateMount(id string, fn func(*config.Mount)) {
	_ = d.store.Update(func(f *config.File) error {
		for i := range f.Mounts {
			if f.Mounts[i].ID == id {
				fn(&f.Mounts[i])
			}
		}
		return nil
	})
}

// stopOpts says how a mount is stopped.
type stopOpts struct {
	// now unmounts without waiting for pending uploads. They go on in the
	// background, and stay journaled for the next mount if modalfs stops first.
	now bool
	// force unmounts even when the folder is in use, once a clean unmount failed.
	force bool
	// patient runs sync and retries a clean unmount for a few seconds
	// before giving up (or forcing).
	patient bool
	// quit: modalfs is quitting. A failed commit doesn't stop the unmount
	// (the changes stay journaled), and a forced unmount is reported as a problem.
	quit bool
}

// StopMount commits pending writes and unmounts. force unmounts right away
// instead: uploads go on in the background, and a busy folder is forced.
// A mount that isn't running is a no-op; one that is starting or stopping
// is an error, except that force goes ahead while another stop is running.
func (d *Daemon) StopMount(id string, force bool) error {
	return d.stopMount(id, stopOpts{now: force, force: force})
}

func (d *Daemon) stopMount(id string, o stopOpts) error {
	label := id
	if m, ok := d.store.Mount(id); ok {
		label = m.VolumeName
	}
	d.mu.Lock()
	r := d.running[id]
	switch {
	case r == nil:
		d.mu.Unlock()
		return nil
	case r.state == StateError:
		delete(d.running, id) // also ends an auto-mount's retries
		d.mu.Unlock()
		return nil
	case r.state == StateStarting:
		d.mu.Unlock()
		return fmt.Errorf("%s is still mounting; wait for it to finish mounting, then try again", label)
	case r.closing && (o.now || o.quit):
		d.mu.Unlock()
		return nil // already unmounted; its uploads finish in the background
	case r.closing:
		d.mu.Unlock()
		return fmt.Errorf("%s is already unmounted and saving its last changes; wait for it to finish", label)
	case r.state == StateStopping && !o.now:
		d.mu.Unlock()
		return fmt.Errorf("%s is already unmounting; wait for it to finish, or unmount it now", label)
	}
	if r.state != StateStopping {
		r.state = StateStopping
		r.stopSince = time.Now()
		r.stopBytesUp = r.client.BytesUp.Load()
		r.stopPending = -1 // filled in below, outside the lock
	}
	r.unmountErr, r.inUseBy = "", nil
	fill := r.stopPending < 0
	d.mu.Unlock()
	if fill {
		st := r.fs.Status()
		d.mu.Lock()
		r.stopPending = st.PendingBytes
		d.mu.Unlock()
	}

	if !o.now {
		if err := r.fs.FlushAll(); err != nil {
			if o.quit {
				d.log.Warn("could not commit everything before quitting; the rest stays journaled for the next mount", "mount", label, "err", err)
			} else {
				if !d.setStateKeep(id, r, StateMounted, "") {
					return nil // a forced stop finished it meanwhile
				}
				err = fmt.Errorf("could not commit pending changes (nothing was unmounted): %w", err)
				d.emitC("error", "stop_failed", id, label, err.Error(), err.Error())
				return err
			}
		}
	}
	if err := d.unmountOS(r, label, o); err != nil {
		if !d.keepMounted(id, r, err) {
			return nil
		}
		return err
	}
	// Claim the teardown, so a normal and a forced stop racing on the same
	// mount close it only once. It stays "stopping" until fs is closed.
	d.mu.Lock()
	if d.running[id] != r || r.closing {
		d.mu.Unlock()
		return nil
	}
	r.closing = true
	d.mu.Unlock()
	if r.nfsL != nil {
		_ = r.nfsL.Close()
	}
	d.saveRuntime() // the folder is gone; nothing to clean up after a crash
	if o.now {
		go func() { _ = d.finishStop(id, r, label) }()
		return nil
	}
	return d.finishStop(id, r, label)
}

// finishStop closes an unmounted mount's filesystem, which commits what's
// still pending, and forgets it.
func (d *Daemon) finishStop(id string, r *runMount, label string) error {
	err := r.fs.Close()
	closeClient(r.client)
	d.mu.Lock()
	if d.running[id] == r {
		delete(d.running, id)
	}
	d.mu.Unlock()
	d.saveRuntime()
	d.updateMount(id, func(m *config.Mount) { m.LastUsedAt = time.Now().Unix() })
	d.emitC("unmounted", "unmounted", id, label, r.mountpoint, r.mountpoint)
	if err != nil {
		d.emitC("error", "upload_left", id, label, "some changes couldn't be uploaded yet; they're kept on this computer and uploaded the next time it's mounted: "+err.Error(), err.Error())
	}
	return err
}

// unmountOS unmounts the folder: cleanly, then (patient) after sync and a
// few retries, then (force) forcibly.
func (d *Daemon) unmountOS(r *runMount, label string, o stopOpts) error {
	err := unmount(r, false)
	if err == nil {
		return nil
	}
	if o.patient {
		for i := 0; i < 4 && err != nil; i++ {
			syscall.Sync() // push the NFS client's unsent writes
			time.Sleep(2 * time.Second)
			err = unmount(r, false)
		}
		if err == nil {
			return nil
		}
	}
	if !o.force {
		return err
	}
	apps := inUseBy(r.tablePath)
	syscall.Sync()
	who := strings.Join(apps, ", ")
	d.log.Warn("folder is in use; forcing the unmount", "mount", label, "mountpoint", r.mountpoint, "in_use_by", who, "err", err)
	kind := "info"
	if o.quit {
		kind = "error"
	}
	if who == "" {
		d.emitC(kind, "unmounted_forced_noapps", r.id, label, r.mountpoint+" was in use, so it was unmounted by force", r.mountpoint)
	} else {
		d.emitC(kind, "unmounted_forced", r.id, label, fmt.Sprintf("%s was in use (%s), so it was unmounted by force", r.mountpoint, who), r.mountpoint, who)
	}
	return unmount(r, true)
}

// keepMounted puts r back to mounted after a failed unmount and records
// why, with the apps that have files open in it. It reports false when
// another (forced) stop is closing r or already has.
func (d *Daemon) keepMounted(id string, r *runMount, err error) bool {
	d.mu.Lock()
	gone := d.running[id] != r || r.closing
	d.mu.Unlock()
	if gone {
		return false
	}
	apps := inUseBy(r.tablePath)
	d.mu.Lock()
	if d.running[id] != r || r.closing {
		d.mu.Unlock()
		return false
	}
	r.state, r.err = StateMounted, ""
	r.unmountErr, r.inUseBy = err.Error(), apps
	r.stopSince = time.Time{}
	d.mu.Unlock()
	label := id
	if m, ok := d.store.Mount(id); ok {
		label = m.VolumeName
	}
	who := strings.Join(apps, ", ")
	if who == "" {
		d.emitC("error", "unmount_busy_noapps", id, label, fmt.Sprintf("couldn't unmount %s: %v", r.mountpoint, err), r.mountpoint)
	} else {
		d.emitC("error", "unmount_busy", id, label, fmt.Sprintf("couldn't unmount %s: it's open in %s", r.mountpoint, who), r.mountpoint, who)
	}
	return true
}

// inUseBy names the apps with files or folders open in a mount, as far as
// lsof can tell in a few seconds; nil when it can't. mp must be the path the
// mount table shows (tablePath): with -b, lsof matches it there instead of
// calling stat(), which a stalled NFS mount would block.
func inUseBy(mp string) []string {
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	// -b: don't block on stat(); +c 0: whole command names; -F c: one "c<name>" line per process.
	out, _ := exec.CommandContext(ctx, "lsof", "-b", "-w", "+c", "0", "-F", "c", "+f", "--", mp).Output()
	seen := map[string]bool{}
	var apps []string
	for _, line := range strings.Split(string(out), "\n") {
		name, ok := strings.CutPrefix(line, "c")
		if !ok || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		apps = append(apps, name)
	}
	sort.Strings(apps)
	return apps
}

// setStateKeep puts r back in the given state after a failed stop. It
// reports false when another (forced) stop is closing r or already has.
func (d *Daemon) setStateKeep(id string, r *runMount, state, msg string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running[id] != r || r.closing {
		return false
	}
	r.state, r.err = state, msg
	r.stopSince = time.Time{}
	return true
}

// onExternalUnmount handles a mount that went away outside modalfs.
func (d *Daemon) onExternalUnmount(id string, r *runMount) {
	d.mu.Lock()
	cur := d.running[id]
	if cur != r || r.state != StateMounted {
		d.mu.Unlock()
		return
	}
	r.state = StateStopping
	r.closing = true
	r.stopSince = time.Now()
	r.stopBytesUp = r.client.BytesUp.Load()
	d.mu.Unlock()
	_ = r.fs.Close()
	closeClient(r.client)
	d.mu.Lock()
	delete(d.running, id)
	d.mu.Unlock()
	d.saveRuntime()
	d.emitC("unmounted", "unmounted_ext", id, "", r.mountpoint+" (unmounted outside modalfs)", r.mountpoint)
}

// SyncMount commits pending writes now.
func (d *Daemon) SyncMount(id string) error {
	d.mu.Lock()
	r := d.running[id]
	d.mu.Unlock()
	if r == nil || r.fs == nil {
		return errors.New("not mounted")
	}
	return r.fs.FlushAll()
}

// watch notices NFS mounts that were unmounted behind our back. Only the
// mount table counts: a stat of a slow NFS mount can time out, and that
// doesn't mean it's gone.
func (d *Daemon) watch(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		d.mu.Lock()
		var nfs []*runMount
		for _, r := range d.running {
			if r.state == StateMounted && r.mode == "nfs" {
				nfs = append(nfs, r)
			}
		}
		d.mu.Unlock()
		if len(nfs) == 0 {
			continue
		}
		table, err := mountedPaths()
		if err != nil {
			d.log.Warn("can't read the mount table", "err", err)
			continue
		}
		for _, r := range nfs {
			if !table[r.tablePath] {
				if r.nfsL != nil {
					_ = r.nfsL.Close()
				}
				d.onExternalUnmount(r.id, r)
			}
		}
	}
}

// ---------- OS helpers ----------

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func prepareMountpoint(mp string) error {
	if mp == "" || mp == "/" {
		return errors.New("invalid mountpoint")
	}
	if err := os.MkdirAll(mp, 0o755); err != nil {
		return err
	}
	if mounted, _ := isMountpoint(mp); mounted {
		return fmt.Errorf("%s is already a mount point", mp)
	}
	ents, err := os.ReadDir(mp)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.Name() != ".DS_Store" && e.Name() != ".localized" {
			return fmt.Errorf("%s is not empty; refusing to hide its contents", mp)
		}
	}
	return nil
}

func nfsMount(port int, mp string, ro bool) (string, error) {
	var args []string
	if runtime.GOOS == "darwin" {
		opts := fmt.Sprintf("port=%d,mountport=%d,vers=3,tcp,locallocks,soft,intr,timeo=600,retrans=2,rsize=1048576,wsize=1048576,readahead=16,actimeo=1,noquota", port, port)
		if ro {
			opts += ",rdonly"
		}
		args = []string{"/sbin/mount", "-t", "nfs", "-o", opts, "localhost:/", mp}
	} else {
		opts := fmt.Sprintf("port=%d,mountport=%d,vers=3,tcp,nolock,soft,timeo=600,retrans=2,rsize=1048576,wsize=1048576,readahead=16,actimeo=1", port, port)
		if ro {
			opts += ",ro"
		}
		args = []string{"mount", "-t", "nfs", "-o", opts, "127.0.0.1:/", mp}
		if os.Geteuid() != 0 {
			args = append([]string{"sudo", "-n"}, args...)
		}
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	return string(out), err
}

func unmount(r *runMount, force bool) error {
	if r.fuseSrv != nil {
		err := r.fuseSrv.Unmount()
		if err != nil && force {
			forceUnmount(r.mountpoint)
			return nil
		}
		if err != nil {
			return fmt.Errorf("unmount failed (files still open?): %w", err)
		}
		return nil
	}
	args := []string{"umount", r.mountpoint}
	if force {
		args = []string{"umount", "-f", r.mountpoint}
	}
	if runtime.GOOS != "darwin" && os.Geteuid() != 0 {
		args = append([]string{"sudo", "-n"}, args...)
	}
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil && force && runtime.GOOS == "darwin" {
		out2, err2 := exec.Command("diskutil", "unmount", "force", r.mountpoint).CombinedOutput()
		if err2 == nil {
			return nil
		}
		out = append(out, out2...)
	}
	if err != nil {
		return fmt.Errorf("unmount failed (files still open?): %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// forceUnmount is used for stale mounts left by a crashed daemon. It never
// stats the mountpoint (a dead NFS server would hang the call).
func forceUnmount(mp string) {
	switch runtime.GOOS {
	case "darwin":
		if exec.Command("umount", "-f", mp).Run() != nil {
			_ = exec.Command("diskutil", "unmount", "force", mp).Run()
		}
	default:
		for _, c := range [][]string{{"fusermount3", "-uz", mp}, {"fusermount", "-uz", mp}, {"umount", "-l", mp}} {
			if exec.Command(c[0], c[1:]...).Run() == nil {
				return
			}
		}
		if os.Geteuid() != 0 {
			_ = exec.Command("sudo", "-n", "umount", "-l", mp).Run()
		}
	}
}

// ---------- runtime state (stale mount cleanup) ----------

func runtimePath() string { return filepath.Join(config.Dir(), "runtime.json") }

// saveRuntime records the live mountpoints, or removes the file when there
// are none. rtMu keeps an older snapshot from overwriting a newer one.
func (d *Daemon) saveRuntime() {
	d.rtMu.Lock()
	defer d.rtMu.Unlock()
	d.mu.Lock()
	var mps []string
	for _, r := range d.running {
		// A closing mount's folder is already unmounted.
		if r.mountpoint != "" && !r.closing && (r.state == StateMounted || r.state == StateStopping) {
			mps = append(mps, r.mountpoint)
		}
	}
	d.mu.Unlock()
	if len(mps) == 0 {
		if err := os.Remove(runtimePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			d.log.Warn("could not remove runtime.json", "err", err)
		}
		return
	}
	raw, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "mountpoints": mps})
	if err := config.WriteAtomic(runtimePath(), raw); err != nil {
		d.log.Warn("could not save runtime.json", "err", err)
	}
}

func (d *Daemon) cleanupStale() {
	raw, err := os.ReadFile(runtimePath())
	if err != nil {
		return
	}
	var st struct {
		Mountpoints []string `json:"mountpoints"`
	}
	if json.Unmarshal(raw, &st) != nil {
		return
	}
	for _, mp := range st.Mountpoints {
		d.log.Warn("cleaning up stale mount from a previous run", "mountpoint", mp)
		forceUnmount(mp)
		d.emitC("info", "stale_cleaned", "", "", "cleaned up stale mount "+mp, mp)
	}
	_ = os.Remove(runtimePath())
}

func friendly(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "Unauthenticated") || strings.Contains(s, "token") && strings.Contains(s, "invalid"):
		return fmt.Errorf("Modal rejected this account's token: %w", err)
	case strings.Contains(s, "NotFound"):
		return fmt.Errorf("not found: %w", err)
	}
	return err
}
