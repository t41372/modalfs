package daemon

// What the UI shows about a volume beyond its state: how big it is, what it
// moved over the last hour, which conflicts are still open, and which files
// are waiting to upload. Modal's API has no volume size, so the size comes
// from one recursive listing, run when asked and kept with its time.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/vfs"
	"github.com/t41372/modalfs/internal/volume"
)

// ---------- traffic ----------

// A mount's upload and read totals, sampled each second for the last hour;
// the UI draws the rates between samples.
const trafficKeep = int(time.Hour/time.Second) + 1

// TrafficSample is a mount's running totals at a moment: [unix seconds, bytes up, bytes down].
type TrafficSample [3]int64

type traffic struct {
	mu sync.Mutex
	m  map[string][]TrafficSample
}

func (t *traffic) add(id string, s TrafficSample) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string][]TrafficSample{}
	}
	list := t.m[id]
	// The totals start again at zero with each mount.
	if n := len(list); n > 0 && (s[1] < list[n-1][1] || s[2] < list[n-1][2]) {
		list = nil
	}
	list = append(list, s)
	if len(list) > trafficKeep {
		list = list[len(list)-trafficKeep:] // append copies what's kept when it grows
	}
	t.m[id] = list
}

// get returns a mount's samples taken after since (unix seconds).
func (t *traffic) get(id string, since int64) []TrafficSample {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.m[id]
	i := sort.Search(len(list), func(i int) bool { return list[i][0] > since })
	return append([]TrafficSample{}, list[i:]...)
}

func (t *traffic) drop(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.m, id)
}

// sampleTraffic records every live mount's totals each second until ctx
// ends, for its current speed and its last hour.
func (d *Daemon) sampleTraffic(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			live := d.liveFS()
			d.speeds.retain(live)
			for id, fs := range live {
				s := fs.Status()
				d.speeds.add(id, now, s.BytesUp, s.BytesDown)
				d.traffic.add(id, TrafficSample{now.Unix(), s.BytesUp, s.BytesDown})
			}
		}
	}
}

// ---------- speed ----------

// speedWindow is how far back a mount's current speed looks: long enough to
// smooth over one slow second, short enough to fall to zero soon after.
const speedWindow = 3 * time.Second

type speedSample struct {
	at       time.Time
	up, down int64
}

// speeds holds each live mount's totals over the last few seconds.
type speeds struct {
	mu sync.Mutex
	m  map[string][]speedSample
}

func (s *speeds) add(id string, at time.Time, up, down int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string][]speedSample{}
	}
	list := s.m[id]
	// The totals start again at zero with each mount.
	if n := len(list); n > 0 && (up < list[n-1].up || down < list[n-1].down) {
		list = nil
	}
	list = append(list, speedSample{at, up, down})
	for len(list) > 1 && at.Sub(list[0].at) > speedWindow {
		list = list[1:]
	}
	s.m[id] = list
}

// get returns a mount's upload and read speed, in bytes per second.
func (s *speeds) get(id string) (up, down int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[id]
	if len(list) < 2 {
		return 0, 0
	}
	first, last := list[0], list[len(list)-1]
	secs := last.at.Sub(first.at).Seconds()
	return int64(float64(last.up-first.up) / secs), int64(float64(last.down-first.down) / secs)
}

// retain forgets the mounts that aren't live any more.
func (s *speeds) retain(live map[string]*vfs.FS) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.m {
		if live[id] == nil {
			delete(s.m, id)
		}
	}
}

// liveFS returns the file systems of the mounts that are mounted or stopping.
func (d *Daemon) liveFS() map[string]*vfs.FS {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]*vfs.FS{}
	for id, r := range d.running {
		if r.fs != nil && (r.state == StateMounted || r.state == StateStopping) {
			out[id] = r.fs
		}
	}
	return out
}

// ---------- conflicts ----------

// Conflict is a file that changed on Modal while it was edited here: the
// local version was saved next to it as Copy.
type Conflict struct {
	Path string `json:"path"`
	Copy string `json:"copy"`
	At   int64  `json:"at"`
}

type conflicts struct {
	mu sync.Mutex
	m  map[string][]Conflict
}

func (c *conflicts) add(id string, x Conflict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]Conflict{}
	}
	c.m[id] = append(c.m[id], x)
}

func (c *conflicts) get(id string) []Conflict {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Conflict{}, c.m[id]...)
}

func (c *conflicts) count(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m[id])
}

// clear marks one conflict handled, or all of them for path "".
func (c *conflicts) clear(id, copyPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if copyPath == "" {
		delete(c.m, id)
		return
	}
	keep := c.m[id][:0]
	for _, x := range c.m[id] {
		if x.Copy != copyPath {
			keep = append(keep, x)
		}
	}
	c.m[id] = keep
}

// ---------- size ----------

// Usage is how much a volume holds, from its last full listing.
type Usage struct {
	Total int64 `json:"total"`
	Files int64 `json:"files"`
	// The largest top-level folders, biggest first; the rest of Total is in
	// smaller folders and in files at the top level.
	Folders []FolderUsage `json:"folders"`
	At      int64         `json:"at"` // when the listing finished
}

type FolderUsage struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Files int64  `json:"files"`
}

// UsageView is a volume's size as the UI sees it: the last result, and a
// listing in progress.
type UsageView struct {
	Last    *Usage `json:"last,omitempty"`
	Running bool   `json:"running"`
	// While running: what's been counted so far.
	Counted      int64  `json:"counted,omitempty"`
	CountedFiles int64  `json:"counted_files,omitempty"`
	Started      int64  `json:"started,omitempty"`
	Error        string `json:"error,omitempty"`
}

const topFolders = 8

type usageJob struct {
	cancel       context.CancelFunc
	bytes, files int64
	started      time.Time
	err          string
}

type usageStore struct {
	mu   sync.Mutex
	path string
	done map[string]Usage // by volume ID
	jobs map[string]*usageJob
	// One listing at a time: a large volume's listing is many requests.
	turn chan struct{}
}

func newUsageStore(dir string) *usageStore {
	u := &usageStore{path: filepath.Join(dir, "usage.json"), done: map[string]Usage{}, jobs: map[string]*usageJob{}, turn: make(chan struct{}, 1)}
	if b, err := os.ReadFile(u.path); err == nil {
		_ = json.Unmarshal(b, &u.done)
	}
	return u
}

func (u *usageStore) last(volID string) (Usage, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	x, ok := u.done[volID]
	return x, ok
}

func (u *usageStore) view(volID string) UsageView {
	u.mu.Lock()
	defer u.mu.Unlock()
	var v UsageView
	if x, ok := u.done[volID]; ok {
		v.Last = &x
	}
	if j := u.jobs[volID]; j != nil {
		v.Running = j.cancel != nil
		v.Counted, v.CountedFiles, v.Error = j.bytes, j.files, j.err
		v.Started = j.started.Unix()
	}
	return v
}

func (u *usageStore) save() {
	b, err := json.Marshal(u.done)
	if err != nil {
		return
	}
	tmp := u.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, err = f.Write(b)
	if err == nil {
		err = fsync.File(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		_ = os.Rename(tmp, u.path)
	}
}

// start begins listing a volume unless it's being listed already.
func (u *usageStore) start(v *volume.Volume) {
	volID := v.ID
	u.mu.Lock()
	if j := u.jobs[volID]; j != nil && j.cancel != nil {
		u.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &usageJob{cancel: cancel, started: time.Now()}
	u.jobs[volID] = j
	u.mu.Unlock()

	go func() {
		defer cancel()
		res, err := u.walk(ctx, v, j)
		u.mu.Lock()
		defer u.mu.Unlock()
		j.cancel = nil
		switch {
		case err == nil:
			u.done[volID] = res
			delete(u.jobs, volID)
			u.save()
		case errors.Is(err, context.Canceled):
			delete(u.jobs, volID)
		default:
			j.err = friendly(err).Error()
		}
	}()
}

func (u *usageStore) walk(ctx context.Context, v *volume.Volume, j *usageJob) (Usage, error) {
	select {
	case u.turn <- struct{}{}:
	case <-ctx.Done():
		return Usage{}, ctx.Err()
	}
	defer func() { <-u.turn }()
	u.mu.Lock()
	j.started = time.Now() // waiting for its turn didn't count
	u.mu.Unlock()
	folders := map[string]*FolderUsage{}
	var total, files int64
	err := v.Walk(ctx, "", func(batch []modalapi.Entry) {
		for _, e := range batch {
			if e.IsDir {
				continue
			}
			total += e.Size
			files++
			if top, _, ok := strings.Cut(e.Path, "/"); ok {
				f := folders[top]
				if f == nil {
					f = &FolderUsage{Name: top}
					folders[top] = f
				}
				f.Bytes += e.Size
				f.Files++
			}
		}
		u.mu.Lock()
		j.bytes, j.files = total, files
		u.mu.Unlock()
	})
	if err != nil {
		return Usage{}, err
	}
	res := Usage{Total: total, Files: files, At: time.Now().Unix(), Folders: []FolderUsage{}}
	for _, f := range folders {
		res.Folders = append(res.Folders, *f)
	}
	sort.Slice(res.Folders, func(a, b int) bool { return res.Folders[a].Bytes > res.Folders[b].Bytes })
	if len(res.Folders) > topFolders {
		res.Folders = res.Folders[:topFolders]
	}
	return res, nil
}

func (u *usageStore) cancel(volID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if j := u.jobs[volID]; j != nil && j.cancel != nil {
		j.cancel()
	}
}

// volumeOf returns a mount's volume ID: the pinned one, or (for a mount never
// mounted yet) the ID its name has now.
func (d *Daemon) volumeOf(ctx context.Context, m config.Mount) (string, *modalapi.Client, error) {
	c, _, err := d.clientFor(m.AccountID, m.Environment)
	if err != nil {
		return "", nil, err
	}
	if m.VolumeID != "" {
		return m.VolumeID, c, nil
	}
	v, err := c.LookupVolume(ctx, m.VolumeName, false)
	if err != nil {
		return "", nil, friendly(err)
	}
	return v.ID, c, nil
}

// ---------- routes ----------

func (d *Daemon) insightRoutes(mux *http.ServeMux) {
	mount := func(w http.ResponseWriter, r *http.Request) (config.Mount, bool) {
		m, ok := d.store.Mount(r.PathValue("id"))
		if !ok {
			writeErr(w, errors.New("mount not found"))
		}
		return m, ok
	}
	mux.HandleFunc("GET /api/mounts/{id}/traffic", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := mount(w, r); ok {
			since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
			writeJSON(w, 200, d.traffic.get(r.PathValue("id"), since))
		}
	})
	mux.HandleFunc("GET /api/mounts/{id}/pending", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := mount(w, r); !ok {
			return
		}
		fs := d.liveFS()[r.PathValue("id")]
		if fs == nil {
			writeJSON(w, 200, []vfs.PendingFile{})
			return
		}
		writeJSON(w, 200, fs.PendingFiles())
	})
	mux.HandleFunc("GET /api/mounts/{id}/conflicts", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := mount(w, r); ok {
			writeJSON(w, 200, d.conflicts.get(r.PathValue("id")))
		}
	})
	// Marks one conflict (by its copy's path) or all of them handled. The
	// files stay as they are: the user decides what to keep.
	mux.HandleFunc("POST /api/mounts/{id}/conflicts/clear", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Copy string }
		_ = readJSON(r, &in)
		if _, ok := mount(w, r); ok {
			d.conflicts.clear(r.PathValue("id"), in.Copy)
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	mux.HandleFunc("GET /api/mounts/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		m, ok := mount(w, r)
		if !ok {
			return
		}
		volID := m.VolumeID
		// Never mounted: its ID isn't pinned yet, so there's nothing saved
		// under it. Locked: what's saved names its folders, which stay hidden.
		if _, locked, _ := d.encryptionFlags(volID); volID == "" || locked {
			writeJSON(w, 200, UsageView{})
			return
		}
		writeJSON(w, 200, d.usage.view(volID))
	})
	// Starts listing the volume to measure it (one listing at a time).
	mux.HandleFunc("POST /api/mounts/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		m, ok := mount(w, r)
		if !ok {
			return
		}
		if m.VolumeID == "" {
			writeErr(w, errors.New("mount it once first, so modalfs knows which volume it is"))
			return
		}
		volID, c, err := d.volumeOf(r.Context(), m)
		if err != nil {
			writeErr(w, err)
			return
		}
		remote, err := d.openVolume(r.Context(), c, volID)
		if err != nil {
			writeErr(w, err)
			return
		}
		d.usage.start(remote)
		writeJSON(w, 200, d.usage.view(volID))
	})
	mux.HandleFunc("DELETE /api/mounts/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		if m, ok := mount(w, r); ok {
			d.usage.cancel(m.VolumeID)
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
}

// revealIn shows a file inside a mounted folder in Finder (or opens its
// folder elsewhere). rel is relative to the mount; it can't leave it.
func revealIn(mp, rel string) error {
	clean := path.Clean("/" + rel)
	if clean == "/" {
		return openPath(mp)
	}
	p := filepath.Join(mp, filepath.FromSlash(clean))
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("%s isn't there any more", clean[1:])
	}
	if runtime.GOOS == "darwin" {
		cmd := exec.Command("open", "-R", p)
		if err := cmd.Start(); err != nil {
			return err
		}
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return openPath(filepath.Dir(p))
}
