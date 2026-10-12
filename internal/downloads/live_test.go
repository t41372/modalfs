//go:build live

// Remote downloads against real Modal, in a scratch volume and a test app
// that are created for the run and removed afterwards. Costs a few cents.
//
//	MODALFS_E2E_PROFILE=<profile> go test -tags live -run TestLive -v -timeout 60m ./internal/downloads
//	(or: just test-downloads-live <profile>)
package downloads

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

// datapacket's 1,000 MB test file, as downloaded uninterrupted in the spike.
const (
	bigURL    = "https://nyc.download.datapacket.com/1000mb.bin"
	bigSHA256 = "bc17f06f9d9b5f6f79ca189a1772b1a3a38d6e40c45bec50f9c4f28144efddca"
	hugeURL   = "https://nyc.download.datapacket.com/10000mb.bin"
	smallURL  = "https://speedtest.newark.linode.com/100MB-newark.bin"
)

type live struct {
	t   *testing.T
	m   *Modal
	api *modalapi.Client
	vol modalapi.VolumeInfo
}

func TestLive(t *testing.T) {
	profile := os.Getenv("MODALFS_E2E_PROFILE")
	if profile == "" {
		t.Skip("set MODALFS_E2E_PROFILE to the Modal profile to test with")
	}
	cfg, err := modalapi.LoadConfig(profile)
	if err != nil {
		t.Fatal(err)
	}
	api, err := modalapi.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	ctx := context.Background()
	stamp := time.Now().Unix()
	vol, err := api.CreateVolume(ctx, fmt.Sprintf("modalfs-dl-test-%d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scratch volume %s (%s)", vol.Name, vol.ID)
	m, err := NewModal(cfg, api, fmt.Sprintf("modalfs-downloads-test-%d", stamp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Stop the test apps and delete the volume; check they're gone.
		if id, _, err := api.DeployedApp(ctx, m.App); err == nil {
			if err := api.StopApp(ctx, id); err != nil {
				t.Errorf("couldn't stop app %s: %v", m.App, err)
			}
		}
		if m.DeployAppID != "" {
			if err := api.StopApp(ctx, m.DeployAppID); err != nil {
				t.Errorf("couldn't stop the deploy app: %v", err)
			}
		}
		if err := api.DeleteVolume(ctx, vol.ID); err != nil {
			t.Errorf("couldn't delete the scratch volume %s: %v", vol.Name, err)
		}
		vols, _ := api.ListVolumes(ctx)
		for _, v := range vols {
			if v.ID == vol.ID {
				t.Errorf("the scratch volume %s is still there", vol.Name)
			}
		}
		m.Close()
	})
	l := &live{t: t, m: m, api: api, vol: vol}

	t0 := time.Now()
	if err := m.Deploy(ctx, func(s string) { t.Log(s) }); err != nil {
		t.Fatal(err)
	}
	t.Logf("deployed in %v", time.Since(t0).Round(time.Second))
	t0 = time.Now()
	if err := m.Deploy(ctx, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("an unchanged agent isn't deployed again (checked in %v)", time.Since(t0).Round(time.Millisecond))

	t.Run("live stream", func(t *testing.T) { l.liveStream(t, "gopeed", hugeURL) })
	t.Run("live stream aria2", func(t *testing.T) { l.liveStream(t, "aria2", hugeURL) })
	t.Run("gopeed survives preemption", l.preemption)
	t.Run("pause and resume", l.pauseResume)
	t.Run("continues past the time limit", l.respawn)
	t.Run("aria2", func(t *testing.T) { l.simple(t, "aria2", bigURL, bigSHA256, 1000000000) })
	t.Run("yt-dlp", func(t *testing.T) { l.simple(t, "yt-dlp", smallURL, "", 104857600) })
}

func (l *live) job(t *testing.T, url, tool string) Job {
	j, err := NewJob("test", "", l.vol.ID, l.vol.Name, "Downloads", url, tool, "")
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func (l *live) start(t *testing.T, j Job) string {
	call, err := l.m.Start(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("call %s", call)
	return call
}

// wait polls status.json (as the daemon will) until ok, logging progress.
func (l *live) wait(t *testing.T, j Job, limit time.Duration, ok func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(limit)
	var s Status
	var err error
	last := ""
	for time.Now().Before(deadline) {
		s, err = ReadStatus(context.Background(), l.m.Volume(j), j)
		if err == nil {
			line := fmt.Sprintf("%s attempt %d: %d/%d MB at %.1f MB/s", s.State, s.Attempt, s.Done/1e6, s.Total/1e6, float64(s.Speed)/1e6)
			if line != last {
				t.Logf("  %s (status %.1f s old)", line, float64(time.Now().UnixNano())/1e9-s.T)
				last = line
			}
			if ok(s) {
				return s
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("status never matched; last %+v (%v)", s, err)
	return s
}

func (l *live) result(t *testing.T, call string, limit time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		sum, done, err := l.m.Result(context.Background(), call)
		if err != nil {
			t.Fatalf("call %s: %v", call, err)
		}
		if done {
			return sum
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("call %s didn't end within %v", call, limit)
	return nil
}

func (l *live) events(t *testing.T, j Job) []string {
	ev, err := ReadEvents(context.Background(), l.m.Volume(j), j)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range ev {
		k := e.Kind
		switch e.Kind {
		case "resumed", "interrupted":
			k += fmt.Sprintf("(gap %v, warned %v)", e.Fields["gap"], e.Fields["warned"])
		case "error", "fallback", "failed":
			k += fmt.Sprintf("(%v%v)", e.Fields["message"], e.Fields["reason"])
		}
		kinds = append(kinds, k)
	}
	return kinds
}

func (l *live) preemption(t *testing.T) {
	j := l.job(t, bigURL, "gopeed")
	call := l.start(t, j)
	s := l.wait(t, j, 5*time.Minute, func(s Status) bool { return s.State == "running" && s.Done > 150e6 || s.State == "done" })
	if s.State == "done" {
		t.Skip("finished before it could be interrupted")
	}
	t.Logf("stopping container %s at %d MB, as preemption does", s.Task, s.Done/1e6)
	if err := l.api.StopContainer(context.Background(), s.Task); err != nil {
		t.Fatal(err)
	}
	s = l.wait(t, j, 10*time.Minute, func(s Status) bool { return !s.Running() && s.State != "interrupted" })
	t.Logf("events: %s", strings.Join(l.events(t, j), " "))
	t.Logf("sync of the volume during the download: avg %.2f s, max %.2f s", s.SyncAvg, s.SyncMax)
	if s.State != "done" {
		t.Fatalf("ended %s: %s %v", s.State, s.Error, s.LogTail)
	}
	if s.Attempt < 2 {
		t.Errorf("finished in attempt %d: the interruption didn't happen", s.Attempt)
	}
	if len(s.Files) != 1 || s.Files[0].SHA256 != bigSHA256 {
		t.Fatalf("files %+v, want sha256 %s", s.Files, bigSHA256)
	}
	sum := l.result(t, call, time.Minute)
	t.Logf("the call's result: %v", sum)
	if h, err := ReadHistory(context.Background(), l.m.Volume(j), j); err != nil || len(h) < 10 {
		t.Errorf("speed history: %d samples, %v", len(h), err)
	}
}

// liveStream reads the container's live channel through its tunnel for 15 s:
// each sample as it's taken, four a second, with a steady speed.
func (l *live) liveStream(t *testing.T, tool, url string) {
	j := l.job(t, url, tool)
	call := l.start(t, j)
	s := l.wait(t, j, 5*time.Minute, func(s Status) bool { return s.State == "running" && s.Done > 100e6 })
	req, err := http.NewRequest("GET", s.LiveURL+"/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Modalfs-Token", j.LiveToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got []Status
	t0 := time.Now()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() && time.Since(t0) < 15*time.Second {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			var st Status
			if err := json.Unmarshal([]byte(data), &st); err != nil {
				t.Fatal(err)
			}
			got = append(got, st)
		}
	}
	resp.Body.Close()
	var times, dones []string
	repeats, lo, hi := 0, int64(1<<62), int64(0)
	for i, st := range got {
		times = append(times, fmt.Sprintf("%.2f", st.T-got[0].T))
		dones = append(dones, fmt.Sprintf("%d@%.0f", st.Done/1e6, float64(st.Speed)/1e6))
		if i > 0 && st.T == got[i-1].T {
			repeats++
		}
		if i >= len(got)/2 {
			lo, hi = min(lo, st.Speed), max(hi, st.Speed)
		}
	}
	t.Logf("%d samples in 15 s", len(got))
	t.Logf("sample times %v", times)
	t.Logf("MB done @ MB/s %v", dones)
	if len(got) < 45 || repeats > len(got)/10 {
		t.Errorf("%d samples in 15 s with %d repeated: want about 60, each new", len(got), repeats)
	}
	if lo > 0 {
		t.Logf("speed over the last 2.5 s: %.1f to %.1f MB/s", float64(lo)/1e6, float64(hi)/1e6)
	}
	if err := Pause(context.Background(), got[len(got)-1], j); err != nil {
		t.Fatal(err)
	}
	sum := l.result(t, call, 2*time.Minute)
	t.Logf("paused: %v", sum)
	if sum["state"] != "paused" || asInt(sum["done"]) < got[len(got)-1].Done {
		t.Errorf("after pausing at %d bytes: %v", got[len(got)-1].Done, sum)
	}
}

func (l *live) pauseResume(t *testing.T) {
	j := l.job(t, hugeURL, "gopeed")
	call := l.start(t, j)
	s := l.wait(t, j, 5*time.Minute, func(s Status) bool { return s.State == "running" && s.Done > 300e6 })
	if err := Pause(context.Background(), s, j); err != nil {
		t.Fatal(err)
	}
	sum := l.result(t, call, 2*time.Minute)
	if sum["state"] != "paused" {
		t.Fatalf("after pause: %v", sum)
	}
	paused := asInt(sum["done"])
	t.Logf("paused at %d MB", paused/1e6)

	call = l.start(t, j)
	s = l.wait(t, j, 5*time.Minute, func(s Status) bool { return s.State == "running" && s.Attempt == 2 && s.Done > paused+200e6 })
	if err := Pause(context.Background(), s, j); err != nil {
		t.Fatal(err)
	}
	sum = l.result(t, call, 2*time.Minute)
	t.Logf("events: %s; second pause at %v", strings.Join(l.events(t, j), " "), sum["done"])
}

func (l *live) respawn(t *testing.T) {
	l.m.Timeout = 2 * time.Minute // the agent hands over 30 s before
	defer func() { l.m.Timeout = 0 }()
	j := l.job(t, hugeURL, "gopeed")
	call := l.start(t, j)
	sum := l.result(t, call, 5*time.Minute)
	if sum["state"] != "continued" {
		t.Fatalf("at the time limit: %v", sum)
	}
	next, _ := sum["next_call"].(string)
	t.Logf("handed over at %v bytes to call %s", sum["done"], next)
	s := l.wait(t, j, 5*time.Minute, func(s Status) bool { return s.State == "running" && s.Attempt == 2 })
	if err := Pause(context.Background(), s, j); err != nil {
		t.Fatal(err)
	}
	t.Logf("successor: %v", l.result(t, next, 2*time.Minute))
	t.Logf("events: %s", strings.Join(l.events(t, j), " "))
}

func asInt(v any) int64 {
	switch v := v.(type) {
	case int64:
		return v
	case uint64:
		return int64(v)
	case int:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}

func (l *live) simple(t *testing.T, tool, url, sha string, size int64) {
	j := l.job(t, url, tool)
	call := l.start(t, j)
	sum := l.result(t, call, 15*time.Minute)
	s, err := ReadStatus(context.Background(), l.m.Volume(j), j)
	if err != nil {
		t.Fatal(err)
	}
	if sum["state"] != "done" {
		t.Fatalf("%s: %v; %s %v", tool, sum, s.Error, s.LogTail)
	}
	if len(s.Files) != 1 || s.Files[0].Size != size || sha != "" && s.Files[0].SHA256 != sha {
		t.Fatalf("files %+v", s.Files)
	}
	t.Logf("%s: %s, events %s", tool, s.Files[0].Path, strings.Join(l.events(t, j), " "))
}
