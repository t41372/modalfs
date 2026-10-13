package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/downloads"
	"github.com/t41372/modalfs/internal/modalapi"
)

// Remote downloads (docs/design/remote-downloads.md). The daemon keeps the
// list of downloads, starts each in a Modal Function when the user asks,
// follows it through the records the Function keeps on the volume, and
// relays its live channel to the UI. Quitting modalfs never stops a
// download: carrying on without the laptop is the point.
//
// Nothing here starts compute on its own. A Function starts only for a
// download the user adds or resumes, and only with the setting turned on.

type dlManager struct {
	d     *Daemon
	store *downloads.Store
	// runnerFor and volumeFor reach Modal; tests replace them.
	runnerFor  func(accountID, env string) (downloads.Runner, error)
	volumeFor  func(ctx context.Context, accountID, env, name string) (modalapi.VolumeInfo, error)
	billingFor func(ctx context.Context, accountID, env string) (Billing, error)
	poll       time.Duration // how often a running download's records are read
	stopAfter  time.Duration // how long a pause may take before the call is cancelled
	quit       <-chan struct{}

	mu        sync.Mutex
	runners   map[clientKey]*downloads.Modal
	following map[string]bool
	stopping  map[string]bool
	status    map[string]downloads.Status // the latest status.json of a running job
	history   map[string]*downloads.History
	live      map[string]*liveFeed

	bills billingCache
}

func newDLManager(d *Daemon, dir string, quit <-chan struct{}) (*dlManager, error) {
	store, err := downloads.OpenStore(dir)
	if err != nil {
		return nil, err
	}
	m := &dlManager{d: d, store: store, poll: 3 * time.Second, stopAfter: 30 * time.Second, quit: quit,
		runners: map[clientKey]*downloads.Modal{}, following: map[string]bool{}, stopping: map[string]bool{},
		status: map[string]downloads.Status{}, history: map[string]*downloads.History{}, live: map[string]*liveFeed{}}
	m.runnerFor = m.modalRunner
	m.billingFor = m.readBilling
	m.volumeFor = func(ctx context.Context, accountID, env, name string) (modalapi.VolumeInfo, error) {
		c, _, err := d.clientFor(accountID, env)
		if err != nil {
			return modalapi.VolumeInfo{}, err
		}
		v, err := c.LookupVolume(ctx, name, false)
		if modalapi.IsNotFound(err) {
			return v, fmt.Errorf("there's no volume %q", name)
		}
		return v, friendly(err)
	}
	return m, nil
}

// resume picks up after the daemon starts: downloads that were running are
// followed again. One still being prepared when modalfs quit may or may not
// have started; it's marked failed, and resuming it is safe either way
// once its call (if any) has ended.
func (m *dlManager) resume() {
	for _, j := range m.store.List() {
		switch {
		case downloads.Ended(j.State):
		case j.State == downloads.StatePreparing && len(j.Calls) == 0:
			_, _ = m.store.Update(j.ID, func(j *downloads.Job) {
				j.State, j.Error = "failed", "modalfs quit while preparing this download; resume it to try again"
			})
		default:
			if j.Stopping != "" {
				go m.stopCall(j.ID)
			}
			go m.follow(j.ID)
		}
	}
}

// modalRunner returns the runner for an account and environment. It uses
// the shared Modal client, so dropRunners must follow dropClients.
func (m *dlManager) modalRunner(accountID, env string) (downloads.Runner, error) {
	cfg, a, err := m.d.clientConfig(accountID, env)
	if err != nil {
		return nil, err
	}
	key := clientKey{a.ID, cfg.Environment, cfg.ServerURL, cfg.Transport}
	m.mu.Lock()
	r := m.runners[key]
	m.mu.Unlock()
	if r != nil {
		return r, nil
	}
	api, _, err := m.d.clientFor(accountID, env)
	if err != nil {
		return nil, err
	}
	r, err = downloads.NewModal(cfg, api, "")
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old := m.runners[key]; old != nil {
		r.Close()
		return old, nil
	}
	m.runners[key] = r
	return r, nil
}

// dropRunners forgets the runners of one account, or all for "", when
// their shared Modal clients are closed.
func (m *dlManager) dropRunners(accountID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	var drop []*downloads.Modal
	for k, r := range m.runners {
		if accountID == "" || k.account == accountID {
			drop = append(drop, r)
			delete(m.runners, k)
		}
	}
	m.mu.Unlock()
	for _, r := range drop {
		r.Close()
	}
}

func (m *dlManager) runner(j downloads.Job) (downloads.Runner, error) {
	return m.runnerFor(j.AccountID, j.Environment)
}

func (m *dlManager) job(id string) (downloads.Job, error) {
	j, ok := m.store.Get(id)
	if !ok {
		return j, fmt.Errorf("no download %s", id)
	}
	return j, nil
}

// ---------- starting ----------

type addRequest struct {
	Account, Environment, Volume, Folder, Tool, Size, Cookies string
	URLs                                                      []string
	// WARP: through Cloudflare WARP or not; downloads.DefaultWARP decides
	// per link when it's missing.
	WARP *bool `json:"warp"`
}

// Add makes a job for each link and starts it. It returns once the jobs are
// saved; preparing the Function (a deploy the first time, about a minute)
// and spawning it happen in the background.
func (m *dlManager) Add(ctx context.Context, in addRequest) ([]downloads.Job, error) {
	s := m.d.store.Snapshot().Settings
	if !s.RemoteDownloads {
		return nil, errors.New("remote downloads are off; turn them on in Settings (or `modalfs download enable`) first: each download runs in a Modal Function in your workspace, billed to your account")
	}
	a, ok := m.d.store.Account(in.Account)
	if !ok {
		return nil, errors.New("choose an account")
	}
	if len(in.URLs) == 0 {
		return nil, errors.New("give at least one link")
	}
	env := in.Environment
	if env == "" {
		env = a.Environment
	}
	if in.Size == "" {
		in.Size = s.DownloadSize
	}
	vol, err := m.volumeFor(ctx, a.ID, env, in.Volume)
	if err != nil {
		return nil, err
	}
	var jobs []downloads.Job
	for _, u := range in.URLs {
		j, err := downloads.NewJob(a.ID, env, vol.ID, vol.Name, in.Folder, strings.TrimSpace(u), in.Tool, in.Size)
		if err != nil {
			return nil, err
		}
		j.Cookies = in.Cookies
		j.StallLimit = s.DownloadStallMinutes * 60
		j.WARP = downloads.DefaultWARP(j.URL)
		if in.WARP != nil {
			j.WARP = *in.WARP
		}
		if j.WARP && downloads.IsBitTorrent(j.URL) {
			return nil, errNoWARPForTorrents(j.URL)
		}
		j.State = downloads.StatePreparing
		jobs = append(jobs, j)
	}
	for _, j := range jobs {
		if err := m.store.Put(j); err != nil {
			return nil, err
		}
		go m.launch(j.ID)
	}
	return jobs, nil
}

// launch prepares the Function if needed and spawns a call for the job.
func (m *dlManager) launch(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	j, err := m.job(id)
	if err != nil {
		return
	}
	name := displayName(j, nil)
	fail := func(err error) {
		if failed, e := m.store.Update(id, func(j *downloads.Job) {
			j.State, j.Error, j.ErrorKind = "failed", err.Error(), downloads.Why(err.Error())
		}); e == nil {
			j = failed
		}
		m.announce(j)
	}
	r, err := m.runner(j)
	if err != nil {
		fail(err)
		return
	}
	if dep, ok := r.(interface {
		Deploy(context.Context, func(string)) error
	}); ok {
		err := dep.Deploy(ctx, func(msg string) {
			m.d.emitC("info", "dl_preparing", "", j.VolumeName, msg+" (when modalfs is new or updated)")
		})
		if err != nil {
			fail(fmt.Errorf("preparing the downloader: %w", err))
			return
		}
	}
	// The status.json on the volume is the last run's until this run's
	// agent writes its own.
	before := 0.0
	if j.Status != nil {
		before = j.Status.T
	}
	if st, ok := m.cachedStatus(id); ok {
		before = max(before, st.T)
	}
	if st, err := downloads.ReadStatus(ctx, r.Volume(j), j); err == nil {
		before = max(before, st.T)
	}
	m.mu.Lock()
	delete(m.status, id)
	m.mu.Unlock()
	j, err = m.store.Update(id, func(j *downloads.Job) { j.Before = before })
	if err != nil {
		fail(err)
		return
	}
	call, err := r.Start(ctx, j)
	if err != nil {
		fail(fmt.Errorf("starting the download: %w", err))
		return
	}
	_, _ = m.store.Update(id, func(j *downloads.Job) {
		j.Calls = append(j.Calls, call)
		j.State, j.Error, j.ErrorKind, j.Started = downloads.StateRunning, "", "", time.Now().Unix()
	})
	m.d.emitC("info", "dl_started", "", j.VolumeName, "downloading "+name+" into "+j.VolumeName+":/"+j.Folder, name, j.VolumeName+":/"+j.Folder)
	m.follow(id)
}

// ---------- following ----------

// follow reads a running job's records until its call ends. One at a time
// per job; it stops when the daemon quits (the download doesn't).
func (m *dlManager) follow(id string) {
	m.mu.Lock()
	if m.following[id] {
		m.mu.Unlock()
		return
	}
	m.following[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.following, id)
		m.mu.Unlock()
	}()
	for !m.step(id) {
		select {
		case <-m.quit:
			return
		case <-time.After(m.poll):
		}
	}
}

// step reads a job's status and whether its call has ended, once. It
// reports whether the job has come to rest.
func (m *dlManager) step(id string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	j, ok := m.store.Get(id)
	if !ok || downloads.Ended(j.State) || len(j.Calls) == 0 {
		return true
	}
	r, err := m.runner(j)
	if err != nil {
		m.d.log.Warn("download: no runner", "id", id, "err", err)
		return false
	}
	if st, err := downloads.ReadStatus(ctx, r.Volume(j), j); err == nil && st.T > j.Before {
		m.noteStatus(j, st)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		m.d.log.Info("download: reading status", "id", id, "err", err)
	}
	call := j.Calls[len(j.Calls)-1]
	sum, ended, err := r.Result(ctx, call)
	if !ended {
		if err == nil {
			return false
		}
		m.d.log.Info("download: reading its call", "id", id, "call", call, "err", err)
		// Modal may forget a call long after it ended (a laptop closed for
		// a week). The agent's own record says how it ended.
		st, ok := m.cachedStatus(id)
		if !ok || st.Running() || st.State == "interrupted" || float64(time.Now().Unix())-st.T < 60 {
			return false
		}
	}
	return m.ended(ctx, r, j, sum, err)
}

// noteStatus keeps a status read from the volume and reports what changed.
func (m *dlManager) noteStatus(j downloads.Job, st downloads.Status) {
	m.mu.Lock()
	prev, had := m.status[j.ID]
	if had && st.T < prev.T {
		m.mu.Unlock()
		return // an older copy read back
	}
	m.status[j.ID] = st
	m.mu.Unlock()
	name := displayName(j, &st)
	if st.State == "interrupted" && (!had || prev.State != "interrupted" || prev.Attempt != st.Attempt) {
		m.d.emitC("info", "dl_interrupted", "", j.VolumeName, name+" was interrupted by Modal; it continues on a new machine from what it saved", name)
	}
	if st.Attempt > 1 && had && prev.Attempt != st.Attempt && st.State != "interrupted" {
		m.d.emitC("info", "dl_resumed", "", j.VolumeName, fmt.Sprintf("%s continues (attempt %d)", name, st.Attempt), name, fmt.Sprint(st.Attempt))
	}
}

// ended records how a job's last call ended. A call that handed over to a
// successor (the 24 h limit) isn't the end: the successor is followed.
// status.json is the record; the call's result is the second opinion.
func (m *dlManager) ended(ctx context.Context, r downloads.Runner, j downloads.Job, sum map[string]any, callErr error) bool {
	if fresh, err := m.job(j.ID); err == nil {
		j = fresh // a pause or cancel may have come in meanwhile
	}
	// The agent writes its last status before it returns.
	var st *downloads.Status
	if s, err := downloads.ReadStatus(ctx, r.Volume(j), j); err == nil && s.T > j.Before {
		st = &s
	} else if cached, ok := m.cachedStatus(j.ID); ok {
		st = &cached
	}
	// A call that handed over to a successor (the 24 h limit) isn't the
	// end. Its status names the successor too, in case the result is gone.
	next, _ := sum["next_call"].(string)
	if callErr != nil && st != nil && st.State == "continued" {
		next = st.Next
	}
	if (callErr == nil && sum["state"] == "continued" || callErr != nil && st != nil && st.State == "continued") && next != "" {
		_, _ = m.store.Update(j.ID, func(j *downloads.Job) { j.Calls = append(j.Calls, next) })
		name := displayName(j, nil)
		m.d.emitC("info", "dl_continued", "", j.VolumeName, name+" reached the Function's time limit and continues in a new one", name)
		return false
	}
	state, msg, kind := "", "", ""
	switch {
	case callErr != nil && st != nil && !st.Running() && st.State != "interrupted":
		// The call's result is lost, but the agent recorded how it ended.
		state, msg = st.State, st.Error
		if strings.HasPrefix(msg, downloads.WARPError) {
			kind = downloads.WhyWARP
		}
	case callErr == nil:
		state, _ = sum["state"].(string)
		msg, _ = sum["error"].(string)
		if state == "" {
			state, msg = "failed", "the download Function returned no state"
		}
		if strings.HasPrefix(msg, downloads.WARPError) {
			kind = downloads.WhyWARP
		}
	case j.Stopping != "":
		// We cancelled it: the agent couldn't be asked to pause.
		state = "paused"
	default:
		state, msg = "failed", callErr.Error()
		kind = downloads.Why(msg)
		if kind == "" && (st == nil || st.Running()) {
			// The container went without the agent's last word, and Modal
			// didn't start another: Modal stopped it.
			kind = downloads.WhyUnexplained
		}
	}
	if j.Stopping == "cancel" && state != "done" {
		if err := m.discard(ctx, r, j); err != nil {
			state, msg = "paused", "cancelled, but its partial files couldn't be deleted: "+err.Error()
		} else {
			state, msg = downloads.StateCancelled, ""
		}
	}
	if st != nil && st.Running() {
		// Cut short without a final word (a cancel): don't show it running.
		st.State, st.Speed, st.ETA = state, 0, nil
	}
	j, _ = m.store.Update(j.ID, func(j *downloads.Job) {
		j.State, j.Error, j.ErrorKind, j.Stopping, j.Keep = state, msg, kind, "", false
		if st != nil {
			j.Status = st
		}
	})
	m.mu.Lock()
	delete(m.status, j.ID)
	m.mu.Unlock()
	m.announce(j)
	return true
}

func (m *dlManager) announce(j downloads.Job) {
	name := displayName(j, j.Status)
	where := j.VolumeName + ":/" + j.Folder
	switch j.State {
	case "done":
		m.d.emitC("info", "dl_done", "", j.VolumeName, name+" downloaded into "+where, name, where)
	case "paused":
		m.d.emitC("info", "dl_paused", "", j.VolumeName, name+" paused", name)
	case "stalled":
		m.d.emitC("error", "dl_stalled", "", j.VolumeName, name+" stopped: the server sent nothing for too long; resume it to try again", name)
	case "time limit":
		m.d.emitC("info", "dl_time_limit", "", j.VolumeName, name+" paused at the Function's time limit; resume it to continue", name)
	case downloads.StateCancelled:
		m.d.emitC("info", "dl_cancelled", "", j.VolumeName, name+" cancelled", name)
	default:
		switch j.ErrorKind {
		case downloads.WhyBilling:
			m.d.emitC("error", "dl_failed_billing", "", j.VolumeName, "download of "+name+" failed: "+j.Error+". Modal says the workspace is out of credit or over its spend limit; raise the limit or add credit on Modal's Usage & Billing page, then resume it", name, j.Error)
		case downloads.WhyUnexplained:
			m.d.emitC("error", "dl_failed_unexplained", "", j.VolumeName, "download of "+name+" failed: "+j.Error+". Modal stopped it without saying why; one cause is a workspace over its spend limit or out of credit, so check Modal's Usage & Billing page, then resume it", name, j.Error)
		default:
			m.d.emitC("error", "dl_failed", "", j.VolumeName, "download of "+name+" failed: "+j.Error, name, j.Error)
		}
	}
}

func (m *dlManager) cachedStatus(id string) (downloads.Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.status[id]
	return s, ok
}

// discard deletes a job's partial download and the downloader's state.
func (m *dlManager) discard(ctx context.Context, r downloads.Runner, j downloads.Job) error {
	v := r.Volume(j)
	for _, p := range []string{j.Dir() + "/work", j.Dir() + "/state"} {
		if err := v.Remove(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// ---------- pause, resume, cancel, forget ----------

// Pause asks a running download to pause. It returns at once; the job is
// "pausing" until its call ends.
func (m *dlManager) Pause(id string) error { return m.stop(id, "pause", false) }

// Cancel stops a download and deletes its partial files, unless keep.
func (m *dlManager) Cancel(ctx context.Context, id string, keep bool) error {
	j, err := m.job(id)
	if err != nil {
		return err
	}
	switch {
	case j.State == "done":
		return fmt.Errorf("download %s has finished; there's nothing to cancel", id)
	case j.State == downloads.StateCancelled:
		return nil
	case downloads.Ended(j.State):
		r, err := m.runner(j)
		if err != nil {
			return err
		}
		if !keep {
			if err := m.discard(ctx, r, j); err != nil {
				return fmt.Errorf("couldn't delete the partial files: %w", err)
			}
		}
		j, _ = m.store.Update(id, func(j *downloads.Job) { j.State, j.Error = downloads.StateCancelled, "" })
		m.announce(j)
		return nil
	}
	return m.stop(id, "cancel", keep)
}

func (m *dlManager) stop(id, how string, keep bool) error {
	j, err := m.job(id)
	if err != nil {
		return err
	}
	switch {
	case downloads.Ended(j.State):
		return fmt.Errorf("download %s isn't running (it's %s)", id, j.State)
	case j.State == downloads.StatePreparing:
		return fmt.Errorf("download %s is still being prepared; try again once it has started", id)
	case j.Stopping == "cancel" || j.Stopping == how:
		return nil
	}
	_, err = m.store.Update(id, func(j *downloads.Job) {
		j.Stopping, j.Keep = how, keep
		j.State = downloads.StatePausing
		if how == "cancel" {
			j.State = downloads.StateCancelling
		}
	})
	if err != nil {
		return err
	}
	go m.stopCall(id)
	return nil
}

// stopCall asks the job's agent to pause through its live channel, which
// saves the downloader's state. If that doesn't work within stopAfter, it
// cancels the call: the agent gets no chance to save, and the download
// continues later from what it saved last (gopeed and aria2 save often).
// follow sees the call end and records it.
func (m *dlManager) stopCall(id string) {
	m.mu.Lock()
	if m.stopping[id] {
		m.mu.Unlock()
		return
	}
	m.stopping[id] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.stopping, id)
		m.mu.Unlock()
	}()
	deadline := time.Now().Add(m.stopAfter)
	asked := false
	for time.Now().Before(deadline) {
		j, err := m.job(id)
		if err != nil || j.Stopping == "" || downloads.Ended(j.State) {
			return // ended meanwhile, and maybe resumed already
		}
		r, err := m.runner(j)
		if err != nil {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		call := j.Calls[len(j.Calls)-1]
		if _, ended, _ := r.Result(ctx, call); ended {
			cancel()
			return // follow records it
		}
		if !asked {
			st, ok := m.cachedStatus(id)
			if fresh, err := downloads.ReadStatus(ctx, r.Volume(j), j); err == nil && fresh.T > j.Before {
				st, ok = fresh, true
				m.noteStatus(j, st)
			}
			if ok && st.LiveURL != "" && st.Running() {
				if err := downloads.Pause(ctx, st, j); err == nil {
					asked = true
				} else {
					m.d.log.Info("download: pause through the live channel failed", "id", id, "err", err)
				}
			}
		}
		cancel()
		select {
		case <-m.quit:
			return
		case <-time.After(time.Second):
		}
	}
	j, err := m.job(id)
	if err != nil || j.Stopping == "" || downloads.Ended(j.State) {
		return
	}
	r, err := m.runner(j)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err = r.Cancel(ctx, j.Calls[len(j.Calls)-1])
		cancel()
	}
	name := displayName(j, nil)
	if err != nil {
		m.d.emitC("error", "dl_stop_failed", "", j.VolumeName, "couldn't stop "+name+": "+err.Error()+"; it may still be running in your Modal workspace", name, err.Error())
		_, _ = m.store.Update(id, func(j *downloads.Job) {
			j.Stopping, j.Keep = "", false
			j.State = downloads.StateRunning
		})
		return
	}
	m.d.emitC("info", "dl_stopped_hard", "", j.VolumeName, "the container of "+name+" didn't answer, so it was stopped at once; resuming continues from its last saved state", name)
}

func errNoWARPForTorrents(link string) error {
	return fmt.Errorf("%s is a torrent: Cloudflare WARP can't carry BitTorrent (its peers are reached directly), so add it without WARP", downloads.StripCredentials(link))
}

// Resume starts a new call for a paused, stalled or failed download. The
// downloader continues from its state on the volume. warp, when given,
// turns Cloudflare WARP on or off for this and later runs (retrying
// without WARP after it couldn't be reached is the user's call).
func (m *dlManager) Resume(ctx context.Context, id string, warp *bool) error {
	if !m.d.store.Snapshot().Settings.RemoteDownloads {
		return errors.New("remote downloads are off; turn them on in Settings (or `modalfs download enable`) first")
	}
	j, err := m.job(id)
	if err != nil {
		return err
	}
	switch j.State {
	case "paused", "stalled", "failed", "time limit":
	case "done", downloads.StateCancelled:
		return fmt.Errorf("download %s is %s; add it again to download it again", id, j.State)
	default:
		return fmt.Errorf("download %s is %s already", id, j.State)
	}
	// Two agents on one job would share the downloader's state; make sure
	// the last call has ended.
	if len(j.Calls) > 0 {
		r, err := m.runner(j)
		if err != nil {
			return err
		}
		if _, ended, _ := r.Result(ctx, j.Calls[len(j.Calls)-1]); !ended {
			return fmt.Errorf("download %s's last run is still ending; try again in a moment", id)
		}
	}
	if warp != nil && *warp && downloads.IsBitTorrent(j.URL) {
		return errNoWARPForTorrents(j.URL)
	}
	if _, err := m.store.Update(id, func(j *downloads.Job) {
		j.State, j.Error, j.ErrorKind = downloads.StatePreparing, "", ""
		if warp != nil {
			j.WARP = *warp
		}
	}); err != nil {
		return err
	}
	go m.launch(id)
	return nil
}

// Forget removes a download that isn't running from the list, and deletes
// its records and any partial files on the volume. Finished files stay.
func (m *dlManager) Forget(ctx context.Context, id string) error {
	j, err := m.job(id)
	if err != nil {
		return err
	}
	if !downloads.Ended(j.State) {
		return fmt.Errorf("download %s is %s; pause or cancel it first", id, j.State)
	}
	r, err := m.runner(j)
	if err != nil {
		return err
	}
	if len(j.Calls) > 0 {
		if _, ended, _ := r.Result(ctx, j.Calls[len(j.Calls)-1]); !ended {
			return fmt.Errorf("download %s's last run is still ending; try again in a moment", id)
		}
	}
	if err := r.Volume(j).Remove(ctx, j.Dir()); err != nil {
		return fmt.Errorf("couldn't delete its records on %s: %w", j.VolumeName, err)
	}
	m.mu.Lock()
	delete(m.history, id)
	delete(m.status, id)
	m.mu.Unlock()
	return m.store.Remove(id)
}

// ---------- views ----------

// DownloadView is a download as the API shows it: no live token, no
// cookies, and the link without credentials.
type DownloadView struct {
	ID           string `json:"id"`
	AccountID    string `json:"account_id"`
	AccountLabel string `json:"account_label"`
	Environment  string `json:"environment,omitempty"`
	VolumeID     string `json:"volume_id"`
	VolumeName   string `json:"volume_name"`
	Folder       string `json:"folder"`
	URL          string `json:"url"`
	Name         string `json:"name"`
	Tool         string `json:"tool"`
	Size         string `json:"size"`
	Created      int64  `json:"created"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
	ErrorKind    string `json:"error_kind,omitempty"` // see downloads.Why
	// Silent is how many seconds a download that should be running has
	// gone without a word from its container (status.json or the live
	// channel): a minute or so when Modal moves it, longer when Modal
	// doesn't start one.
	Silent  int64             `json:"silent,omitempty"`
	Cookies bool              `json:"cookies,omitempty"`
	WARP    bool              `json:"warp,omitempty"` // through Cloudflare WARP (Status.WARP: where it left)
	Status  *downloads.Status `json:"status,omitempty"`
}

func (m *dlManager) views() []DownloadView {
	out := []DownloadView{}
	if m == nil {
		return out
	}
	accts := map[string]string{}
	for _, a := range m.d.store.Snapshot().Accounts {
		accts[a.ID] = a.Label
	}
	for _, j := range m.store.List() {
		out = append(out, m.view(j, accts[j.AccountID]))
	}
	return out
}

func (m *dlManager) view(j downloads.Job, account string) DownloadView {
	st := j.Status
	if !downloads.Ended(j.State) {
		if s, ok := m.cachedStatus(j.ID); ok {
			st = &s
		}
		if s := m.liveStatus(j.ID); s != nil && (st == nil || s.T > st.T) {
			st = s
		}
	}
	st = withoutLiveURL(st)
	v := DownloadView{ID: j.ID, AccountID: j.AccountID, AccountLabel: account, Environment: j.Environment,
		VolumeID: j.VolumeID, VolumeName: j.VolumeName, Folder: j.Folder, URL: downloads.StripCredentials(j.URL),
		Name: displayName(j, st), Tool: j.Tool, Size: j.Size, Created: j.Created, State: j.State, Error: j.Error,
		ErrorKind: j.ErrorKind, Cookies: j.Cookies != "", WARP: j.WARP, Status: st}
	if j.State == downloads.StateRunning && j.Started > 0 {
		last := float64(j.Started)
		if st != nil && st.T > j.Before {
			last = max(last, st.T)
		}
		v.Silent = max(0, time.Now().Unix()-int64(last))
	}
	if j.State == downloads.StateRunning && (st == nil || st.T <= j.Before) {
		v.State = "starting"
	} else if j.State == downloads.StateRunning {
		v.State = shownState(st.State)
		if st.Tool != "" {
			v.Tool = st.Tool // after a fallback to aria2
		}
	}
	return v
}

// shownState is what a running job's agent state means to the user while
// its call hasn't ended: "paused" in status.json with the call still there
// is a pause in progress, and so on.
func shownState(agent string) string {
	switch agent {
	case "paused", "time limit", "continued":
		return downloads.StatePausing
	case "done":
		return "finishing"
	case "":
		return downloads.StateRunning
	}
	return agent // starting, running, retrying, pausing, finishing, interrupted, stalled, failed
}

// displayName is the file's name once known, else the link's last part.
func displayName(j downloads.Job, st *downloads.Status) string {
	if st != nil && st.Name != "" {
		return st.Name
	}
	if j.Status != nil && j.Status.Name != "" {
		return j.Status.Name
	}
	u := downloads.StripCredentials(j.URL)
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if b := path.Base(strings.TrimRight(u, "/")); b != "" && b != "." && b != "/" {
		return b
	}
	return j.ID
}

// DownloadDetail is one download with what the detail view draws: every
// attempt and interruption, and the speed history as [t, bytes, bytes/s].
type DownloadDetail struct {
	Download     DownloadView       `json:"download"`
	Events       []downloads.Event  `json:"events"`
	History      []downloads.Sample `json:"history"`
	RecordsError string             `json:"records_error,omitempty"`
}

// Detail reads a download's events and its speed history after the time
// after (0 for all of it) from the volume.
func (m *dlManager) Detail(ctx context.Context, id string, after float64) (DownloadDetail, error) {
	j, err := m.job(id)
	if err != nil {
		return DownloadDetail{}, err
	}
	a, _ := m.d.store.Account(j.AccountID)
	out := DownloadDetail{Download: m.view(j, a.Label), Events: []downloads.Event{}, History: []downloads.Sample{}}
	if len(j.Calls) == 0 {
		return out, nil
	}
	r, err := m.runner(j)
	if err != nil {
		out.RecordsError = err.Error()
		return out, nil
	}
	v := r.Volume(j)
	var errs []string
	if ev, err := downloads.ReadEvents(ctx, v, j); err != nil {
		errs = append(errs, "events: "+err.Error())
	} else if ev != nil {
		out.Events = ev
	}
	m.mu.Lock()
	h := m.history[id]
	if h == nil {
		h = new(downloads.History)
		m.history[id] = h
	}
	m.mu.Unlock()
	if hist, err := h.Read(ctx, v, j); err != nil {
		errs = append(errs, "speed history: "+err.Error())
	} else {
		for _, s := range hist {
			if s[0] > after {
				out.History = append(out.History, s)
			}
		}
	}
	out.RecordsError = strings.Join(errs, "; ")
	return out, nil
}

// Log returns the downloader's log, which the agent copies to the volume
// when a run ends.
func (m *dlManager) Log(ctx context.Context, id string) ([]byte, error) {
	j, err := m.job(id)
	if err != nil {
		return nil, err
	}
	r, err := m.runner(j)
	if err != nil {
		return nil, err
	}
	b, err := r.Volume(j).ReadFile(ctx, j.Dir()+"/log.txt")
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("there's no log yet: it's saved when a run ends (the status shows the latest errors meanwhile)")
	}
	return b, err
}

// ---------- the live channel ----------

// The live channel: while someone watches a running download, the daemon
// holds a connection to the agent's tunnel and keeps what it sends, and
// clients poll GET /api/downloads/{id}/live?after=T (4 times a second
// suits it) for what's new.
//
// The daemon relays rather than handing the tunnel's address and token to
// the UI: the agent's server answers no CORS preflight (the token is a
// header), EventSource can't send headers at all, the desktop app reaches
// the daemon only through its socket, and a public URL with its token is
// better kept in one place. Polling instead of a stream also works through
// every client the same way.

type liveFeed struct {
	mu      sync.Mutex
	rows    []downloads.Sample // this run's history, then what the tunnel sends
	status  *downloads.Status
	asked   time.Time
	running bool
	err     string
}

// LiveView is what the live endpoint returns.
type LiveView struct {
	Live    bool               `json:"live"` // connected to the download's container
	Status  *downloads.Status  `json:"status,omitempty"`
	Samples []downloads.Sample `json:"samples"`
	Error   string             `json:"error,omitempty"`
}

// liveIdle is how long a feed stays connected after its last poll.
var liveIdle = 15 * time.Second

// listLive is how many running downloads the list keeps a live feed open
// for (GET /api/downloads?live=1), newest first; the rest show what their
// status.json said, a few seconds behind.
const listLive = 8

// watchList keeps live feeds open for the running downloads in a list.
func (m *dlManager) watchList(views []DownloadView) {
	n := 0
	for i := len(views) - 1; i >= 0 && n < listLive; i-- { // newest first
		if downloads.Ended(views[i].State) || views[i].State == downloads.StatePreparing {
			continue
		}
		n++
		_, _ = m.Live(views[i].ID, math.MaxFloat64)
	}
}

// liveStatus is the latest status a live feed received, if one is open.
func (m *dlManager) liveStatus(id string) *downloads.Status {
	m.mu.Lock()
	f := m.live[id]
	m.mu.Unlock()
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running || f.status == nil {
		return nil
	}
	s := *f.status
	return &s
}

func (m *dlManager) Live(id string, after float64) (LiveView, error) {
	j, err := m.job(id)
	if err != nil {
		return LiveView{}, err
	}
	out := LiveView{Samples: []downloads.Sample{}}
	st, ok := m.cachedStatus(id)
	if downloads.Ended(j.State) || !ok || st.LiveURL == "" || !st.Running() {
		out.Status = j.Status
		if ok && !downloads.Ended(j.State) {
			out.Status = &st
		}
		out.Status = withoutLiveURL(out.Status)
		return out, nil
	}
	m.mu.Lock()
	f := m.live[id]
	if f == nil {
		f = &liveFeed{}
		m.live[id] = f
	}
	m.mu.Unlock()
	f.mu.Lock()
	f.asked = time.Now()
	if !f.running {
		// A new connection, maybe to a new run: start from its history.
		f.running, f.err, f.rows, f.status = true, "", nil, nil
		go m.runLive(j, st.LiveURL, f)
	}
	out.Live = f.status != nil
	out.Status, out.Error = f.status, f.err
	if out.Status == nil {
		out.Status = &st
	}
	for _, s := range f.rows {
		if s[0] > after {
			out.Samples = append(out.Samples, s)
		}
	}
	f.mu.Unlock()
	out.Status = withoutLiveURL(out.Status)
	return out, nil
}

// withoutLiveURL copies a status without the tunnel's address, which the
// daemon keeps to itself.
func withoutLiveURL(st *downloads.Status) *downloads.Status {
	if st == nil {
		return nil
	}
	c := *st
	c.LiveURL, c.URL = "", downloads.StripCredentials(c.URL)
	return &c
}

var liveClient = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 20 * time.Second}}

func (m *dlManager) runLive(j downloads.Job, url string, f *liveFeed) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := func() error {
		// Stop when nobody has asked for a while, or the daemon quits.
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-m.quit:
					cancel()
					return
				case <-t.C:
					f.mu.Lock()
					idle := time.Since(f.asked) > liveIdle
					f.mu.Unlock()
					if idle {
						cancel()
						return
					}
				}
			}
		}()
		get := func(p string) (*http.Response, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", url+p, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("X-Modalfs-Token", j.LiveToken)
			resp, err := liveClient.Do(req)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode != 200 {
				resp.Body.Close()
				return nil, fmt.Errorf("the download's container answered %s", resp.Status)
			}
			return resp, nil
		}
		// This run's history first: the last half minute of it is only in
		// the container (the volume gets it in 30 s chunks).
		resp, err := get("/history")
		if err != nil {
			return err
		}
		var rows []downloads.Sample
		err = json.NewDecoder(resp.Body).Decode(&rows)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("reading the download's history: %w", err)
		}
		f.mu.Lock()
		f.rows = rows
		f.mu.Unlock()
		if resp, err = get("/live"); err != nil {
			return err
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var s downloads.Status
			if json.Unmarshal([]byte(data), &s) != nil {
				continue
			}
			f.mu.Lock()
			f.status = &s
			if n := len(f.rows); n == 0 || s.T > f.rows[n-1][0] {
				f.rows = append(f.rows, downloads.Sample{s.T, float64(s.Done), float64(s.Speed)})
			}
			f.mu.Unlock()
		}
		return sc.Err()
	}()
	f.mu.Lock()
	f.running = false
	if err != nil && ctx.Err() == nil {
		f.err = err.Error()
	}
	f.mu.Unlock()
}

// ---------- API ----------

var errNoDownloads = errors.New("remote downloads aren't available in this process")

func (d *Daemon) downloadRoutes(mux *http.ServeMux) {
	d.billingRoutes(mux)
	ready := func(w http.ResponseWriter) bool {
		if d.dl == nil {
			writeErr(w, errNoDownloads)
			return false
		}
		return true
	}
	// ?live=1 (the UI's list, while it's on screen) also keeps a live feed
	// open for the newest running downloads, so their speed is current.
	mux.HandleFunc("GET /api/downloads", func(w http.ResponseWriter, r *http.Request) {
		views := d.dl.views()
		if d.dl != nil && r.URL.Query().Get("live") == "1" {
			d.dl.watchList(views)
			views = d.dl.views()
		}
		writeJSON(w, 200, views)
	})
	// {account, environment?, volume (name), folder, urls[], tool?, size?, cookies?}
	mux.HandleFunc("POST /api/downloads", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		var in addRequest
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		jobs, err := d.dl.Add(r.Context(), in)
		if err != nil {
			writeErr(w, err)
			return
		}
		a, _ := d.store.Account(in.Account)
		out := []DownloadView{}
		for _, j := range jobs {
			out = append(out, d.dl.view(j, a.Label))
		}
		writeJSON(w, 200, out)
	})
	// ?after=T returns only the speed history after T (unix seconds).
	mux.HandleFunc("GET /api/downloads/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		var after float64
		_, _ = fmt.Sscan(r.URL.Query().Get("after"), &after)
		out, err := d.dl.Detail(r.Context(), r.PathValue("id"), after)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/downloads/{id}/live", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		var after float64
		_, _ = fmt.Sscan(r.URL.Query().Get("after"), &after)
		out, err := d.dl.Live(r.PathValue("id"), after)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	// The folders in a volume's folder, for picking where a download goes.
	// ?env=&path= (volume-relative; "" for the top).
	mux.HandleFunc("GET /api/accounts/{id}/volumes/{vol}/folders", func(w http.ResponseWriter, r *http.Request) {
		c, _, err := d.clientFor(r.PathValue("id"), r.URL.Query().Get("env"))
		if err != nil {
			writeErr(w, err)
			return
		}
		dir := strings.Trim(path.Clean("/"+r.URL.Query().Get("path")), "/")
		es, err := c.List(r.Context(), r.PathValue("vol"), dir, false)
		if err != nil {
			writeErr(w, friendly(err))
			return
		}
		out := []string{}
		for _, e := range es {
			name := path.Base(e.Path)
			if e.IsDir && !(dir == "" && name == ".modalfs") {
				out = append(out, name)
			}
		}
		sort.Strings(out)
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/downloads/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		b, err := d.dl.Log(r.Context(), r.PathValue("id"))
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("POST /api/downloads/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		var in struct {
			Keep bool
			WARP *bool `json:"warp"` // resume: turn WARP on or off
		}
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		id := r.PathValue("id")
		var err error
		switch r.PathValue("action") {
		case "pause":
			err = d.dl.Pause(id)
		case "resume":
			err = d.dl.Resume(r.Context(), id, in.WARP)
		case "cancel":
			err = d.dl.Cancel(r.Context(), id, in.Keep)
		default:
			err = errors.New("unknown action")
		}
		if err != nil {
			writeErr(w, err)
			return
		}
		j, _ := d.dl.job(id)
		a, _ := d.store.Account(j.AccountID)
		writeJSON(w, 200, d.dl.view(j, a.Label))
	})
	mux.HandleFunc("DELETE /api/downloads/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !ready(w) {
			return
		}
		if err := d.dl.Forget(r.Context(), r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}
