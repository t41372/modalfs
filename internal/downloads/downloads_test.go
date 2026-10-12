package downloads

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewJob(t *testing.T) {
	j, err := NewJob("ac-1", "main", "vo-1", "vol", "/Movies/../Downloads/", "https://example.com/a.iso", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Folder != "Downloads" || j.Tool != "gopeed" || j.Size != "small" || len(j.LiveToken) < 32 {
		t.Fatalf("unexpected job %+v", j)
	}
	if NewJobTool(t, "https://www.youtube.com/watch?v=x") != "yt-dlp" || NewJobTool(t, "https://notyoutube.com/x") != "gopeed" {
		t.Fatal("wrong default tool")
	}
	for _, bad := range []struct{ url, tool, size, folder string }{
		{"example.com/a", "", "", ""},
		{"https://example.com/a", "wget", "", ""},
		{"https://example.com/a", "", "huge", ""},
		{"https://example.com/a", "", "", ".modalfs/x"},
	} {
		if _, err := NewJob("ac", "", "vo", "v", bad.folder, bad.url, bad.tool, bad.size); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	a := j.args("app", MaxRun)
	if a["volume_id"] != "vo-1" || a["cpu"] != 0.125 || a["timeout"] != 86400 {
		t.Fatalf("args %v", a)
	}
}

func NewJobTool(t *testing.T, url string) string {
	j, err := NewJob("ac", "", "vo", "v", "", url, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return j.Tool
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := NewJob("ac", "", "vo", "v", "", "https://example.com/a", "", "")
	if err := s.Put(j); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(j.ID, func(j *Job) { j.Calls = append(j.Calls, "fc-1") }); err != nil {
		t.Fatal(err)
	}
	s2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s2.Get(j.ID)
	if !ok || len(got.Calls) != 1 || got.LiveToken != j.LiveToken {
		t.Fatalf("reopened store has %+v", got)
	}
	st, _ := os.Stat(filepath.Join(dir, "downloads.json"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("downloads.json is %v; it holds tokens", st.Mode().Perm())
	}
}

// flaky returns zeros for the first reads of a file, as a Modal volume
// does while the agent rewrites it.
type flaky struct {
	DirVolume
	torn int
}

func (f *flaky) ReadFile(ctx context.Context, p string) ([]byte, error) {
	b, err := f.DirVolume.ReadFile(ctx, p)
	if err == nil && f.torn > 0 {
		f.torn--
		return make([]byte, len(b)), nil
	}
	return b, err
}

func TestRecordsSurviveTornReads(t *testing.T) {
	root := t.TempDir()
	j := Job{ID: "dl-1"}
	dir := filepath.Join(root, ".modalfs", "downloads", "dl-1")
	_ = os.MkdirAll(filepath.Join(dir, "speed"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"id":"dl-1","state":"running","done":5,"total":10,"t":1}`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte("{\"ev\":\"start\",\"t\":1,\"attempt\":1}\n{\"ev\":\"sig"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "speed", "000000.jsonl"), []byte("[1, 0, 0]\n[2, 5, 5]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "speed", "000001.jsonl"), []byte("[3, 10, 5]\n"), 0o644)

	v := &flaky{DirVolume: DirVolume(root), torn: 2}
	s, err := ReadStatus(context.Background(), v, j)
	if err != nil || s.Done != 5 || !s.Running() {
		t.Fatalf("status %+v, %v", s, err)
	}
	ev, err := ReadEvents(context.Background(), v, j)
	if err != nil || len(ev) != 1 || ev[0].Kind != "start" || ev[0].Fields["attempt"] != 1.0 {
		t.Fatalf("events %+v, %v", ev, err)
	}
	h, err := ReadHistory(context.Background(), v, j)
	if err != nil || len(h) != 3 || h[2][1] != 10 {
		t.Fatalf("history %v, %v", h, err)
	}
	if _, err := ReadStatus(context.Background(), v, Job{ID: "missing"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing status: %v", err)
	}
	always := &flaky{DirVolume: DirVolume(root), torn: 100}
	if _, err := ReadStatus(context.Background(), always, j); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("a status that never reads whole: %v", err)
	}
}

// The local runner drives the real agent and gopeed against a local origin:
// start, pause through the live channel, resume, finish.
func TestLocalRunner(t *testing.T) {
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
	data := make([]byte, 64<<20)
	_, _ = rand.Read(data)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(slowWriter{w}, r, "file.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer origin.Close()

	root := t.TempDir()
	r := &Local{Root: root, Env: []string{"GOPEED_BIN=" + gopeed}, Extra: map[string]any{"status_every": 0.3, "speed_chunk": 2, "connections": 4}}
	j, err := NewJob("ac", "", "vo-local", "v", "In", origin.URL+"/file.bin", "gopeed", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	call, err := r.Start(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Cancel(ctx, call) })
	s := waitStatus(t, r, j, func(s Status) bool { return s.State == "running" && s.Done > 8<<20 })
	if err := Pause(ctx, s, j); err != nil {
		t.Fatal(err)
	}
	sum := waitResult(t, r, call)
	if sum["state"] != "paused" {
		t.Fatalf("after pause: %v", sum)
	}

	call, err = r.Start(ctx, j)
	if err != nil {
		t.Fatal(err)
	}
	sum = waitResult(t, r, call)
	if sum["state"] != "done" {
		t.Fatalf("after resume: %v", sum)
	}
	got, err := os.ReadFile(filepath.Join(root, "In", "file.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if sha(got) != sha(data) {
		t.Fatal("the downloaded file differs from the origin's")
	}
	ev, err := ReadEvents(ctx, r.Volume(j), j)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range ev {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, " ") != "start paused start finished" {
		t.Fatalf("events: %v", kinds)
	}
	if h, err := ReadHistory(ctx, r.Volume(j), j); err != nil || len(h) < 3 {
		t.Fatalf("history: %d samples, %v", len(h), err)
	}
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

func waitStatus(t *testing.T, r Runner, j Job, ok func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	var s Status
	var err error
	for time.Now().Before(deadline) {
		if s, err = ReadStatus(context.Background(), r.Volume(j), j); err == nil && ok(s) {
			return s
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("status never matched; last %+v (%v)", s, err)
	return s
}

func waitResult(t *testing.T, r Runner, call string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		sum, done, err := r.Result(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			return sum
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("call %s didn't end", call)
	return nil
}

func sha(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
