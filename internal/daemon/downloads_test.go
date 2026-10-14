package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/downloads"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/vfs"
)

// fakeRunner stands in for Modal: the test decides how each call ends and
// writes the agent's records into a folder standing in for the volume.
type fakeRunner struct {
	root string

	mu        sync.Mutex
	calls     map[string]*fakeCall
	n         int
	cancelled []string
	startErr  error // Modal refusing to start a call
}

type fakeCall struct {
	job   downloads.Job
	sum   map[string]any
	err   error
	ended bool
}

func (f *fakeRunner) Start(_ context.Context, j downloads.Job) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.n++
	id := fmt.Sprintf("fc-%d", f.n)
	f.calls[id] = &fakeCall{job: j}
	return id, nil
}

func (f *fakeRunner) Result(_ context.Context, id string) (map[string]any, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls[id]
	if c == nil {
		return nil, false, fmt.Errorf("no call %s", id)
	}
	return c.sum, c.ended, c.err
}

func (f *fakeRunner) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	if c := f.calls[id]; c != nil && !c.ended {
		c.ended, c.err = true, errors.New("the call was cancelled")
	}
	return nil
}

func (f *fakeRunner) Volume(downloads.Job) downloads.Volume { return downloads.DirVolume(f.root) }

func (f *fakeRunner) end(id string, sum map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls[id] == nil {
		f.calls[id] = &fakeCall{}
	}
	f.calls[id].sum, f.calls[id].ended = sum, true
}

// record writes one of the agent's files for a job.
func (f *fakeRunner) record(t *testing.T, id, name string, v any) {
	t.Helper()
	dir := filepath.Join(f.root, ".modalfs", "downloads", id)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	switch v := v.(type) {
	case string:
		raw = []byte(v)
	default:
		raw, _ = json.Marshal(v)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

type dlTest struct {
	t    *testing.T
	d    *Daemon
	r    *fakeRunner
	api  http.Handler
	quit chan struct{}
}

func newDLTest(t *testing.T, enabled bool) *dlTest {
	t.Setenv("MODALFS_HOME", t.TempDir())
	store, err := config.Open()
	if err != nil {
		t.Fatal(err)
	}
	err = store.Update(func(f *config.File) error {
		f.Accounts = append(f.Accounts, config.Account{ID: "acct-1", Label: "me", TokenID: "ak-1"})
		f.Settings.RemoteDownloads = enabled
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d := testDaemon(t)
	d.store = store
	if d.cache, err = vfs.NewBlockCache(t.TempDir(), 1<<30); err != nil {
		t.Fatal(err)
	}
	x := &dlTest{t: t, d: d, r: &fakeRunner{root: t.TempDir(), calls: map[string]*fakeCall{}}, quit: make(chan struct{})}
	t.Cleanup(func() { x.stop() })
	d.dl = newTestManager(t, d, config.Dir(), x.quit, x.r)
	x.api = d.routes()
	return x
}

// stop stops the manager's goroutines, as quitting the daemon does.
func (x *dlTest) stop() {
	select {
	case <-x.quit:
	default:
		close(x.quit)
	}
}

func newTestManager(t *testing.T, d *Daemon, dir string, quit chan struct{}, r downloads.Runner) *dlManager {
	m, err := newDLManager(d, dir, quit)
	if err != nil {
		t.Fatal(err)
	}
	m.poll, m.stopAfter = 20*time.Millisecond, 2*time.Second
	m.checkVolume = func(context.Context, string, string, string) error { return nil }
	m.runnerFor = func(string, string) (downloads.Runner, error) { return r, nil }
	m.volumeFor = func(_ context.Context, _, _, name string) (modalapi.VolumeInfo, error) {
		return modalapi.VolumeInfo{ID: "vo-1", Name: name}, nil
	}
	return m
}

func (x *dlTest) do(method, path string, in any, out any) (int, string) {
	x.t.Helper()
	var body *bytes.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	w := httptest.NewRecorder()
	x.api.ServeHTTP(w, httptest.NewRequest(method, "http://modalfsd"+path, body))
	if out != nil && w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			x.t.Fatalf("%s %s: %v: %s", method, path, err, w.Body.String())
		}
	}
	return w.Code, w.Body.String()
}

func (x *dlTest) add(url string) DownloadView {
	x.t.Helper()
	var out []DownloadView
	code, body := x.do("POST", "/api/downloads", map[string]any{
		"account": "acct-1", "volume": "media", "folder": "In", "urls": []string{url}, "cookies": "SECRET-COOKIE",
	}, &out)
	if code != 200 || len(out) != 1 {
		x.t.Fatalf("add: %d %s", code, body)
	}
	return out[0]
}

func (x *dlTest) view(id string) DownloadView {
	x.t.Helper()
	var out DownloadDetail
	if code, body := x.do("GET", "/api/downloads/"+id, nil, &out); code != 200 {
		x.t.Fatalf("detail: %d %s", code, body)
	}
	return out.Download
}

// waitState waits for the job's state as the API shows it.
func (x *dlTest) waitState(id, want string) DownloadView {
	x.t.Helper()
	var v DownloadView
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if v = x.view(id); v.State == want {
			return v
		}
	}
	x.t.Fatalf("download %s is %s (%s), not %s", id, v.State, v.Error, want)
	return v
}

func (x *dlTest) job(id string) downloads.Job {
	j, _ := x.d.dl.store.Get(id)
	return j
}

func (x *dlTest) events(code string) []Event {
	var out []Event
	for _, e := range x.d.Events(0) {
		if e.Code == code {
			out = append(out, e)
		}
	}
	return out
}

// waitEvents waits until n events with this code were sent: a download's
// state is saved before its event goes out.
func (x *dlTest) waitEvents(code string, n int) []Event {
	x.t.Helper()
	var ev []Event
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ev = x.events(code); len(ev) >= n {
			break
		}
	}
	return ev
}

func TestDownloadsAreOffUntilTurnedOn(t *testing.T) {
	x := newDLTest(t, false)
	code, body := x.do("POST", "/api/downloads", map[string]any{"account": "acct-1", "volume": "v", "urls": []string{"https://example.com/a"}}, nil)
	if code != 400 || !strings.Contains(body, "off") {
		t.Fatalf("add with downloads off: %d %s", code, body)
	}
	if code, body := x.do("PATCH", "/api/settings", map[string]any{"remote_downloads": true}, nil); code != 200 {
		t.Fatalf("turning them on: %d %s", code, body)
	}
	// Other settings changes leave it alone; false turns it off.
	x.do("PATCH", "/api/settings", map[string]any{"language": "en"}, nil)
	if !x.d.store.Snapshot().Settings.RemoteDownloads {
		t.Fatal("an unrelated settings change turned remote downloads off")
	}
	x.do("PATCH", "/api/settings", map[string]any{"remote_downloads": false}, nil)
	if x.d.store.Snapshot().Settings.RemoteDownloads {
		t.Fatal("remote_downloads: false didn't turn them off")
	}
	if code, _ := x.do("PATCH", "/api/settings", map[string]any{"download_size": "huge"}, nil); code != 400 {
		t.Fatal("an unknown download size was accepted")
	}
}

// A live channel like the agent's: history, a status stream, and pause.
type fakeLive struct {
	srv    *httptest.Server
	token  string
	paused chan struct{}
	once   sync.Once
}

func newFakeLive(t *testing.T, token string) *fakeLive {
	l := &fakeLive{token: token, paused: make(chan struct{})}
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Modalfs-Token") != l.token {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/history":
			_, _ = w.Write([]byte(`[[100, 0, 0], [101, 1000, 1000]]`))
		case "/live":
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; ; i++ {
				fmt.Fprintf(w, "data: {\"state\":\"running\",\"done\":%d,\"total\":10000,\"speed\":1000,\"t\":%d,\"live_url\":\"secret-url\"}\n\n", 2000+i*1000, 102+i)
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		case "/pause":
			l.once.Do(func() { close(l.paused) })
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(func() { l.srv.CloseClientConnections(); l.srv.Close() })
	return l
}

func TestDownloadLifecycle(t *testing.T) {
	x := newDLTest(t, true)
	v := x.add("https://user:pass@example.com/files/big.iso?sig=abc")
	if v.State != downloads.StatePreparing || v.Name != "big.iso" || v.Tool != "gopeed" || v.Folder != "In" || v.VolumeName != "media" {
		t.Fatalf("new download %+v", v)
	}
	x.waitState(v.ID, "starting")
	j := x.job(v.ID)
	if len(j.Calls) != 1 {
		t.Fatalf("calls %v", j.Calls)
	}
	live := newFakeLive(t, j.LiveToken)
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "running", "done": 1000, "total": 10000, "speed": 1000, "t": 101, "live_url": live.srv.URL, "attempt": 1, "name": "big.iso"})
	x.r.record(t, v.ID, "events.jsonl", "{\"ev\":\"start\",\"t\":100,\"attempt\":1}\n")
	x.r.record(t, v.ID, "speed/000000.jsonl", "[100, 0, 0]\n[101, 1000, 1000]\n")
	for deadline := time.Now().Add(5 * time.Second); x.view(v.ID).Status == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the status was never read")
		}
	}

	// Nothing secret leaves the daemon.
	for _, p := range []string{"/api/downloads", "/api/downloads?live=1", "/api/downloads/" + v.ID, "/api/downloads/" + v.ID + "/live"} {
		_, body := x.do("GET", p, nil, nil)
		for _, secret := range []string{j.LiveToken, "SECRET-COOKIE", "user:pass", "sig=abc", live.srv.URL, "secret-url"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s shows %q: %s", p, secret, body)
			}
		}
	}
	var detail DownloadDetail
	x.do("GET", "/api/downloads/"+v.ID+"?after=100", nil, &detail)
	if len(detail.Events) != 1 || detail.Events[0].Kind != "start" || len(detail.History) != 1 || detail.History[0][1] != 1000 {
		t.Fatalf("detail %+v", detail)
	}

	// The list, while on screen, follows running downloads live too.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var list []DownloadView
		x.do("GET", "/api/downloads?live=1", nil, &list)
		if len(list) == 1 && list[0].Status != nil && list[0].Status.Done >= 2000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the list never showed live numbers: %+v", list)
		}
	}

	// The live channel: this run's history, then what the container sends.
	var lv LiveView
	for deadline := time.Now().Add(5 * time.Second); !lv.Live || len(lv.Samples) < 4; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("live: %+v", lv)
		}
		x.do("GET", "/api/downloads/"+v.ID+"/live", nil, &lv)
	}
	if lv.Samples[0][0] != 100 || lv.Status.Done < 2000 {
		t.Fatalf("live: %+v", lv)
	}
	last := lv.Samples[len(lv.Samples)-1][0]
	x.do("GET", fmt.Sprintf("/api/downloads/%s/live?after=%v", v.ID, last), nil, &lv)
	for _, s := range lv.Samples {
		if s[0] <= last {
			t.Fatalf("?after=%v returned %v", last, s)
		}
	}

	// Resuming or forgetting a running download is refused.
	if code, _ := x.do("POST", "/api/downloads/"+v.ID+"/resume", nil, nil); code != 400 {
		t.Fatal("resumed a running download")
	}
	if code, _ := x.do("DELETE", "/api/downloads/"+v.ID, nil, nil); code != 400 {
		t.Fatal("forgot a running download")
	}

	// Pause goes through the live channel; the call then ends "paused".
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/pause", nil, nil); code != 200 {
		t.Fatalf("pause: %d %s", code, body)
	}
	select {
	case <-live.paused:
	case <-time.After(5 * time.Second):
		t.Fatal("pause never reached the container")
	}
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "paused", "done": 5000, "total": 10000, "t": 110, "attempt": 1, "name": "big.iso"})
	x.r.end(j.Calls[0], map[string]any{"state": "paused", "done": 5000})
	p := x.waitState(v.ID, "paused")
	if p.Status == nil || p.Status.Done != 5000 || len(x.r.cancelled) != 0 {
		t.Fatalf("paused: %+v, cancelled %v", p.Status, x.r.cancelled)
	}

	// Resume starts a second call; it hands over at the time limit to a
	// third, which finishes.
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/resume", nil, nil); code != 200 {
		t.Fatalf("resume: %d %s", code, body)
	}
	// The second call; the view may already show it running, from the
	// container's live status.
	for deadline := time.Now().Add(5 * time.Second); len(x.job(v.ID).Calls) < 2 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	j = x.job(v.ID)
	if len(j.Calls) != 2 {
		t.Fatalf("calls after resume: %v", j.Calls)
	}
	x.r.end(j.Calls[1], map[string]any{"state": "continued", "next_call": "fc-99"})
	for deadline := time.Now().Add(5 * time.Second); len(x.job(v.ID).Calls) != 3; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the successor call wasn't kept: %v", x.job(v.ID).Calls)
		}
	}
	if x.view(v.ID).State == "done" || downloads.Ended(x.job(v.ID).State) {
		t.Fatal("a handed-over download was taken as ended")
	}
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "done", "done": 10000, "total": 10000, "t": 200, "attempt": 3, "name": "big.iso",
		"files": []map[string]any{{"path": "In/big.iso", "size": 10000}}})
	x.r.end("fc-99", map[string]any{"state": "done", "done": 10000})
	done := x.waitState(v.ID, "done")
	if done.Status == nil || len(done.Status.Files) != 1 || done.Status.Files[0].Path != "In/big.iso" {
		t.Fatalf("done: %+v", done.Status)
	}
	if done.Ended == 0 || done.Ended < done.Created || done.Ran < 0 {
		t.Fatalf("done: ended %d, ran %d s; the list needs when it ended and how long it ran", done.Ended, done.Ran)
	}
	if len(x.events("dl_done")) != 1 || len(x.events("dl_paused")) != 1 || len(x.events("dl_continued")) != 1 {
		t.Fatalf("events: %+v", x.d.Events(0))
	}

	// Forgetting it deletes its records; the finished file stays.
	_ = os.MkdirAll(filepath.Join(x.r.root, "In"), 0o755)
	_ = os.WriteFile(filepath.Join(x.r.root, "In", "big.iso"), []byte("data"), 0o644)
	if code, body := x.do("DELETE", "/api/downloads/"+v.ID, nil, nil); code != 200 {
		t.Fatalf("forget: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(x.r.root, ".modalfs", "downloads", v.ID)); !os.IsNotExist(err) {
		t.Fatalf("its records are still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(x.r.root, "In", "big.iso")); err != nil {
		t.Fatal("forgetting a download deleted the file it downloaded")
	}
	var list []DownloadView
	x.do("GET", "/api/downloads", nil, &list)
	if len(list) != 0 {
		t.Fatalf("still listed: %+v", list)
	}
}

// When the container can't be asked to pause, the call is cancelled, and
// that reads as paused, not failed.
func TestDownloadPauseFallsBackToCancel(t *testing.T) {
	x := newDLTest(t, true)
	v := x.add("https://example.com/a.bin")
	x.waitState(v.ID, "starting")
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "running", "done": 10, "t": 1, "live_url": "http://127.0.0.1:1"})
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/pause", nil, nil); code != 200 {
		t.Fatalf("pause: %d %s", code, body)
	}
	if s := x.view(v.ID).State; s != downloads.StatePausing {
		t.Fatalf("right after pause it's %s", s)
	}
	p := x.waitState(v.ID, "paused")
	if len(x.r.cancelled) != 1 || p.Error != "" || p.Status == nil || p.Status.State != "paused" {
		t.Fatalf("paused %+v (status %+v), cancelled %v", p, p.Status, x.r.cancelled)
	}
	if len(x.events("dl_stopped_hard")) != 1 {
		t.Fatalf("events: %+v", x.d.Events(0))
	}
}

// "interrupted" in status.json while the call goes on is Modal moving it
// to another machine, not the end of it.
func TestDownloadInterruptionIsNotTheEnd(t *testing.T) {
	x := newDLTest(t, true)
	v := x.add("https://example.com/a.bin")
	x.waitState(v.ID, "starting")
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "interrupted", "done": 10, "t": 1, "attempt": 1})
	x.waitState(v.ID, "interrupted")
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "running", "done": 20, "t": 40, "attempt": 2})
	x.waitState(v.ID, "running")
	if downloads.Ended(x.job(v.ID).State) || len(x.events("dl_interrupted")) != 1 || len(x.events("dl_resumed")) != 1 {
		t.Fatalf("job %+v, events %+v", x.job(v.ID), x.d.Events(0))
	}
	// A call that fails without our asking is a failure.
	x.r.mu.Lock()
	x.r.calls[x.job(v.ID).Calls[0]].ended = true
	x.r.calls[x.job(v.ID).Calls[0]].err = errors.New("container crashed")
	x.r.mu.Unlock()
	f := x.waitState(v.ID, "failed")
	if !strings.Contains(f.Error, "crashed") || f.Status.State != "failed" {
		t.Fatalf("failed: %+v", f)
	}
}

func TestDownloadCancel(t *testing.T) {
	x := newDLTest(t, true)
	partial := func(id string) bool {
		_, err := os.Stat(filepath.Join(x.r.root, ".modalfs", "downloads", id, "work", "a.bin"))
		return err == nil
	}
	// A running one: paused through the container, then its partial files go.
	v := x.add("https://example.com/a.bin")
	x.waitState(v.ID, "starting")
	x.r.record(t, v.ID, "work/a.bin", "partial")
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "running", "done": 7, "t": 1})
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/cancel", nil, nil); code != 200 {
		t.Fatalf("cancel: %d %s", code, body)
	}
	x.waitState(v.ID, downloads.StateCancelled)
	if partial(v.ID) {
		t.Fatal("cancel kept the partial file")
	}
	// A paused one with keep: nothing is deleted.
	w := x.add("https://example.com/b.bin")
	x.waitState(w.ID, "starting")
	x.r.record(t, w.ID, "work/a.bin", "partial")
	x.r.end(x.job(w.ID).Calls[0], map[string]any{"state": "paused"})
	x.waitState(w.ID, "paused")
	if code, body := x.do("POST", "/api/downloads/"+w.ID+"/cancel", map[string]any{"keep": true}, nil); code != 200 {
		t.Fatalf("cancel: %d %s", code, body)
	}
	if x.view(w.ID).State != downloads.StateCancelled || !partial(w.ID) {
		t.Fatal("cancel with keep deleted the partial file, or didn't cancel")
	}
	if code, _ := x.do("POST", "/api/downloads/"+w.ID+"/resume", nil, nil); code != 400 {
		t.Fatal("resumed a cancelled download")
	}
}

// A daemon that starts again follows the downloads that were running.
func TestDownloadsSurviveARestart(t *testing.T) {
	x := newDLTest(t, true)
	v := x.add("https://example.com/a.bin")
	x.waitState(v.ID, "starting")
	// Another, cut off while preparing.
	j, _ := downloads.NewJob("acct-1", "", "vo-1", "media", "", "https://example.com/b.bin", "", "")
	j.State = downloads.StatePreparing
	_ = x.d.dl.store.Put(j)

	x.stop()
	time.Sleep(50 * time.Millisecond) // let its last poll finish
	x.quit = make(chan struct{})
	x.d.dl = newTestManager(t, x.d, config.Dir(), x.quit, x.r)
	x.api = x.d.routes()
	x.d.dl.resume()
	if s := x.view(j.ID); s.State != "failed" || !strings.Contains(s.Error, "resume") {
		t.Fatalf("the one cut off while preparing: %+v", s)
	}
	x.r.end(x.job(v.ID).Calls[0], map[string]any{"state": "done"})
	x.waitState(v.ID, "done")
}

// Modal may forget a call's result long after it ended; the agent's
// status says how it ended.
func TestDownloadEndsByItsRecordWhenTheResultIsGone(t *testing.T) {
	x := newDLTest(t, true)
	old := float64(time.Now().Add(-5 * time.Minute).Unix())
	// The result can't be read at all: the call is unknown.
	v := x.add("https://example.com/a.bin")
	x.waitState(v.ID, "starting")
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "done", "done": 10, "total": 10, "t": old})
	x.r.mu.Lock()
	delete(x.r.calls, x.job(v.ID).Calls[0])
	x.r.mu.Unlock()
	x.waitState(v.ID, "done")
	// The result reads as an error.
	w := x.add("https://example.com/b.bin")
	x.waitState(w.ID, "starting")
	x.r.record(t, w.ID, "status.json", map[string]any{"state": "done", "done": 10, "total": 10, "t": old})
	x.r.mu.Lock()
	x.r.calls[x.job(w.ID).Calls[0]].ended = true
	x.r.calls[x.job(w.ID).Calls[0]].err = errors.New("result expired")
	x.r.mu.Unlock()
	if s := x.waitState(w.ID, "done"); s.Error != "" {
		t.Fatalf("done with an error: %+v", s)
	}
	if n := len(x.events("dl_done")); n != 2 {
		t.Fatalf("%d dl_done events", n)
	}
}

// The real agent and gopeed, driven through the API: add, pause, resume,
// done, with the live channel relayed on the way.
func TestDownloadWithTheLocalAgent(t *testing.T) {
	gopeed := os.Getenv("GOPEED_BIN")
	if gopeed == "" {
		gopeed, _ = exec.LookPath("gopeed")
	}
	if gopeed == "" {
		t.Skip("gopeed isn't installed (just agent-tools)")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 isn't installed")
	}
	data := make([]byte, 48<<20)
	_, _ = rand.Read(data)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(slowWriter{w}, r, "file.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer origin.Close()

	x := newDLTest(t, true)
	local := &downloads.Local{Root: t.TempDir(), Env: []string{"GOPEED_BIN=" + gopeed},
		Extra: map[string]any{"status_every": 0.3, "speed_chunk": 2, "connections": 4}}
	x.d.dl.runnerFor = func(string, string) (downloads.Runner, error) { return local, nil }
	x.d.dl.stopAfter = 30 * time.Second
	v := x.add(origin.URL + "/file.bin")
	t.Cleanup(func() {
		for _, c := range x.job(v.ID).Calls {
			_ = local.Cancel(context.Background(), c)
		}
	})
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(100 * time.Millisecond) {
		s := x.view(v.ID).Status
		if s != nil && s.State == "running" && s.Done > 8<<20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never got going: %+v", x.view(v.ID))
		}
	}
	var lv LiveView
	for deadline := time.Now().Add(30 * time.Second); !lv.Live || len(lv.Samples) < 3; time.Sleep(250 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("live: %+v", lv)
		}
		x.do("GET", "/api/downloads/"+v.ID+"/live", nil, &lv)
	}
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/pause", nil, nil); code != 200 {
		t.Fatalf("pause: %d %s", code, body)
	}
	x.waitStateFor(v.ID, "paused", time.Minute)
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/resume", nil, nil); code != 200 {
		t.Fatalf("resume: %d %s", code, body)
	}
	done := x.waitStateFor(v.ID, "done", 3*time.Minute)
	got, err := os.ReadFile(filepath.Join(local.Root, "In", "file.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatal("the downloaded file differs from the origin's")
	}
	var detail DownloadDetail
	x.do("GET", "/api/downloads/"+v.ID, nil, &detail)
	var kinds []string
	for _, e := range detail.Events {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, " ") != "start paused start finished" || len(detail.History) < 3 || done.Status.Attempt != 2 {
		t.Fatalf("events %v, %d samples, status %+v", kinds, len(detail.History), done.Status)
	}
	if code, body := x.do("GET", "/api/downloads/"+v.ID+"/log", nil, nil); code != 200 || body == "" {
		t.Fatalf("log: %d %s", code, body)
	}
}

func (x *dlTest) waitStateFor(id, want string, limit time.Duration) DownloadView {
	x.t.Helper()
	var v DownloadView
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if v = x.view(id); v.State == want {
			return v
		}
	}
	x.t.Fatalf("download %s is %s (%s), not %s", id, v.State, v.Error, want)
	return v
}

type slowWriter struct{ http.ResponseWriter }

func (w slowWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), 64<<10)
		m, err := w.ResponseWriter.Write(p[:k])
		n += m
		if err != nil {
			return n, err
		}
		p = p[k:]
		time.Sleep(time.Duration(k) * time.Second / (1 << 20)) // 1 MiB/s per connection
	}
	return n, nil
}

// Modal's refusals are told apart: one that names the spend limit is
// believed, one without a reason says so and points at billing as a cause.
func TestDownloadRefusedByModal(t *testing.T) {
	x := newDLTest(t, true)
	x.r.startErr = errors.New("RemoteError: Image build for im-AmHP9P7tCZr2GkFtM90o5A terminated due to external shut-down. Please try again.")
	v := x.add("https://example.com/a.bin")
	f := x.waitState(v.ID, "failed")
	if f.ErrorKind != downloads.WhyUnexplained || len(x.waitEvents("dl_failed_unexplained", 1)) != 1 {
		t.Fatalf("refused without a reason: %+v, events %+v", f, x.d.Events(0))
	}

	x.r.startErr = errors.New("FailedPrecondition: workspace has reached its spend limit")
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/resume", map[string]any{}, nil); code != 200 {
		t.Fatalf("resume: %d %s", code, body)
	}
	f = x.waitState(v.ID, "failed")
	if f.ErrorKind != downloads.WhyBilling || len(x.waitEvents("dl_failed_billing", 1)) != 1 {
		t.Fatalf("refused for money: %+v", f)
	}

	// Resuming once Modal starts it again clears the reason.
	x.r.startErr = nil
	if code, body := x.do("POST", "/api/downloads/"+v.ID+"/resume", map[string]any{}, nil); code != 200 {
		t.Fatalf("resume: %d %s", code, body)
	}
	if r := x.waitState(v.ID, "starting"); r.ErrorKind != "" || r.Error != "" {
		t.Fatalf("after a resume: %+v", r)
	}

	// A container gone without the agent's last word, and no other started:
	// Modal stopped it, reason unknown.
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "running", "done": 20, "t": float64(time.Now().Unix()), "attempt": 1})
	x.waitState(v.ID, "running")
	x.r.mu.Lock()
	c := x.r.calls[x.job(v.ID).Calls[len(x.job(v.ID).Calls)-1]]
	c.ended, c.err = true, errors.New("RemoteError: some message")
	x.r.mu.Unlock()
	if f = x.waitState(v.ID, "failed"); f.ErrorKind != downloads.WhyUnexplained {
		t.Fatalf("container gone: %+v", f)
	}
}

// A download that should be running but hasn't been heard from says for how
// long, so the UI can tell a slow start or a move from Modal not starting it.
func TestDownloadSilence(t *testing.T) {
	x := newDLTest(t, true)
	v := x.add("https://example.com/a.bin")
	x.waitState(v.ID, "starting")
	if _, err := x.d.dl.store.Update(v.ID, func(j *downloads.Job) { j.Started = time.Now().Unix() - 600 }); err != nil {
		t.Fatal(err)
	}
	if s := x.view(v.ID).Silent; s < 600 || s > 660 {
		t.Fatalf("silent %d, want about 600", s)
	}
	x.r.record(t, v.ID, "status.json", map[string]any{"state": "running", "done": 20, "t": float64(time.Now().Unix() - 5), "attempt": 1})
	x.waitState(v.ID, "running")
	if s := x.view(v.ID).Silent; s > 60 {
		t.Fatalf("silent %d after a fresh status", s)
	}
}

func TestBilling(t *testing.T) {
	x := newDLTest(t, true)
	calls := 0
	x.d.dl.billingFor = func(_ context.Context, account, env string) (Billing, error) {
		calls++
		cost := 0.25
		return Billing{Metered: 10.5, Billed: 1.5, Downloads: &cost, UsageURL: "https://modal.com/settings/ws/usage"}, nil
	}
	var b Billing
	for range 2 {
		if code, body := x.do("GET", "/api/accounts/acct-1/billing", nil, &b); code != 200 {
			t.Fatalf("billing: %d %s", code, body)
		}
	}
	if calls != 1 || b.Billed != 1.5 || b.Downloads == nil || *b.Downloads != 0.25 || b.At == 0 {
		t.Fatalf("calls %d, billing %+v", calls, b)
	}
	x.do("GET", "/api/accounts/acct-1/billing?fresh=1", nil, &b)
	if calls != 2 {
		t.Fatalf("fresh=1 didn't ask again (%d)", calls)
	}
}

func TestBillingHelpers(t *testing.T) {
	h := hourly(map[string]float64{"cpu_hour_cost": 0.0473, "mem_gib_hour_cost": 0.008})
	if got := h["small"]; got < 0.00990 || got > 0.00992 {
		t.Fatalf("small costs %f an hour, want about 0.0099", got)
	}
	if hourly(map[string]float64{"cpu_hour_cost": 1}) != nil {
		t.Fatal("hourly without a memory rate")
	}
	for in, want := range map[string]string{
		"https://modal.com/apps/example-workspace/main": "https://modal.com/settings/example-workspace/usage",
		"https://modal.com/apps/ws":                     "https://modal.com/settings/ws/usage",
		"https://example.com/":                          "https://modal.com/settings/usage",
		"::":                                            "https://modal.com/settings/usage",
	} {
		if got := usageURL(in); got != want {
			t.Errorf("usageURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// YouTube links go through Cloudflare WARP unless asked otherwise; a WARP
// failure is told apart, and resuming can turn WARP off.
func TestDownloadWARP(t *testing.T) {
	x := newDLTest(t, true)
	yt := x.add("https://www.youtube.com/watch?v=aqz-KE-bpKQ")
	plain := x.add("https://example.com/a.bin")
	if !yt.WARP || plain.WARP {
		t.Fatalf("defaults: youtube %v, plain %v", yt.WARP, plain.WARP)
	}
	var out []DownloadView
	code, body := x.do("POST", "/api/downloads", map[string]any{
		"account": "acct-1", "volume": "media", "urls": []string{"https://example.com/b.bin"}, "warp": true,
	}, &out)
	if code != 200 || len(out) != 1 || !out[0].WARP {
		t.Fatalf("asked for WARP: %d %s", code, body)
	}
	code, body = x.do("POST", "/api/downloads", map[string]any{
		"account": "acct-1", "volume": "media", "urls": []string{"magnet:?xt=urn:btih:" + strings.Repeat("0", 40)}, "warp": true,
	}, nil)
	if code == 200 || !strings.Contains(body, "BitTorrent") {
		t.Fatalf("a torrent through WARP: %d %s", code, body)
	}

	x.waitState(yt.ID, "starting")
	calls := x.job(yt.ID).Calls
	x.r.mu.Lock()
	sent := x.r.calls[calls[0]].job
	x.r.mu.Unlock()
	if !sent.WARP {
		t.Fatalf("the Function isn't asked for WARP: %+v", sent)
	}
	x.r.end(calls[0], map[string]any{"state": "failed", "error": "Cloudflare WARP: couldn't connect (no handshake)"})
	if f := x.waitState(yt.ID, "failed"); f.ErrorKind != downloads.WhyWARP {
		t.Fatalf("a WARP failure: %+v", f)
	}
	if code, body := x.do("POST", "/api/downloads/"+yt.ID+"/resume", map[string]any{"warp": false}, nil); code != 200 {
		t.Fatalf("resume without WARP: %d %s", code, body)
	}
	if v := x.waitState(yt.ID, "starting"); v.WARP || v.ErrorKind != "" {
		t.Fatalf("after resuming without WARP: %+v", v)
	}
	calls = x.job(yt.ID).Calls
	x.r.mu.Lock()
	sent = x.r.calls[calls[len(calls)-1]].job
	x.r.mu.Unlock()
	if sent.WARP {
		t.Fatal("the new run still asks for WARP")
	}
}
