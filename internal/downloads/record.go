package downloads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
)

// Status is the latest status.json the agent wrote (every 5 s while it
// runs, and once more when it stops).
type Status struct {
	ID      string   `json:"id"`
	State   string   `json:"state"` // starting, running, retrying, pausing, finishing; then done, paused, interrupted, stalled, failed, continued, time limit
	Tool    string   `json:"tool"`
	URL     string   `json:"url"` // without credentials
	Name    string   `json:"name,omitempty"`
	Note    string   `json:"note,omitempty"`
	Error   string   `json:"error,omitempty"`
	Done    int64    `json:"done"`
	Total   int64    `json:"total"` // -1 when unknown
	Speed   int64    `json:"speed"` // bytes per second
	ETA     *int64   `json:"eta,omitempty"`
	Attempt int      `json:"attempt,omitempty"`
	Task    string   `json:"task,omitempty"` // the Modal container
	LiveURL string   `json:"live_url,omitempty"`
	Files   []File   `json:"files,omitempty"`
	Next    string   `json:"next_call,omitempty"`
	LogTail []string `json:"log_tail,omitempty"`
	SyncAvg float64  `json:"sync_avg,omitempty"` // seconds a `sync` of the volume took
	SyncMax float64  `json:"sync_max,omitempty"`
	// WARP is where the download leaves Cloudflare's network, when it goes
	// through WARP: the data center (colo) and the address sites see.
	WARP *WARPExit `json:"warp,omitempty"`
	T    float64   `json:"t"` // when it was written (unix seconds)
}

type WARPExit struct {
	Colo string `json:"colo,omitempty"`
	IP   string `json:"ip,omitempty"`
	Loc  string `json:"loc,omitempty"`
}

type File struct {
	Path   string `json:"path"` // volume-relative
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Running reports whether the agent was still working when it wrote this.
func (s Status) Running() bool {
	switch s.State {
	case "starting", "running", "retrying", "pausing", "finishing":
		return true
	}
	return false
}

// Event is one line of events.jsonl: start, interrupted, resumed, signal,
// error, fallback, stalled, paused, continued, failed, finished.
type Event struct {
	Kind   string         `json:"ev"`
	T      float64        `json:"t"`
	Fields map[string]any `json:"-"`
}

func (e *Event) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &e.Fields); err != nil {
		return err
	}
	e.Kind, _ = e.Fields["ev"].(string)
	e.T, _ = e.Fields["t"].(float64)
	if e.Kind == "" {
		return errors.New("an event without a kind")
	}
	return nil
}

func (e Event) MarshalJSON() ([]byte, error) { return json.Marshal(e.Fields) }

// Sample is one second of the speed history: time, bytes done, bytes per second.
type Sample [3]float64

// Volume reads a job's records from the volume it downloads into.
type Volume interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	List(ctx context.Context, dir string) ([]string, error) // file names in dir
	// Remove deletes a file or a folder and everything in it. A path that
	// isn't there is not an error.
	Remove(ctx context.Context, path string) error
}

const maxRecord = 16 << 20

// ModalVolume reads through Modal's volume API, as the mounts do.
type ModalVolume struct {
	Client   *modalapi.Client
	VolumeID string
}

func (v ModalVolume) ReadFile(ctx context.Context, p string) ([]byte, error) {
	b, err := v.Client.ReadFile(ctx, v.VolumeID, p, maxRecord)
	if modalapi.IsNotFound(err) {
		return nil, fmt.Errorf("%s: %w", p, os.ErrNotExist)
	}
	return b, err
}

func (v ModalVolume) List(ctx context.Context, dir string) ([]string, error) {
	es, err := v.Client.List(ctx, v.VolumeID, dir, false)
	if modalapi.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range es {
		if !e.IsDir {
			names = append(names, filepath.Base(e.Path))
		}
	}
	return names, nil
}

func (v ModalVolume) Remove(ctx context.Context, p string) error {
	err := v.Client.Remove(ctx, v.VolumeID, p, true)
	if modalapi.IsNotFound(err) {
		return nil
	}
	return err
}

// DirVolume is a local folder standing in for a volume (the local runner).
type DirVolume string

func (d DirVolume) ReadFile(_ context.Context, p string) ([]byte, error) {
	return os.ReadFile(filepath.Join(string(d), filepath.FromSlash(p)))
}

func (d DirVolume) List(_ context.Context, dir string) ([]string, error) {
	es, err := os.ReadDir(filepath.Join(string(d), filepath.FromSlash(dir)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var names []string
	for _, e := range es {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, err
}

func (d DirVolume) Remove(_ context.Context, p string) error {
	return os.RemoveAll(filepath.Join(string(d), filepath.FromSlash(p)))
}

// A file the agent is rewriting can read back torn on a Modal volume (zeros,
// or cut short) for a few seconds; reads are checked and tried again.
const readTries = 5

func readValid(ctx context.Context, v Volume, p string, valid func([]byte) error) ([]byte, error) {
	var err error
	for i := 0; i < readTries; i++ {
		var b []byte
		b, err = v.ReadFile(ctx, p)
		if err == nil {
			if err = valid(b); err == nil {
				return b, nil
			}
			err = fmt.Errorf("%s reads back incomplete (%w)", p, err)
		} else if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(300*(i+1)) * time.Millisecond):
		}
	}
	return nil, err
}

func checkJSON(b []byte) error {
	if bytes.IndexByte(b, 0) >= 0 {
		return errors.New("zero bytes")
	}
	if !json.Valid(b) {
		return errors.New("not JSON")
	}
	return nil
}

// ReadStatus reads a job's status.json. It returns an error wrapping
// os.ErrNotExist before the agent has written one.
func ReadStatus(ctx context.Context, v Volume, j Job) (Status, error) {
	var s Status
	b, err := readValid(ctx, v, j.Dir()+"/status.json", checkJSON)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// ReadEvents reads a job's events.jsonl. A last line still being written
// is left out.
func ReadEvents(ctx context.Context, v Volume, j Job) ([]Event, error) {
	b, err := readValid(ctx, v, j.Dir()+"/events.jsonl", func(b []byte) error {
		if bytes.IndexByte(b, 0) >= 0 {
			return errors.New("zero bytes")
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	var out []Event
	for i, line := range lines {
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			if i == len(lines)-1 && !strings.HasSuffix(string(b), "\n") {
				break // being appended
			}
			return nil, fmt.Errorf("events.jsonl line %d: %w", i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// ReadHistory reads the speed history the agent has written so far, in
// order.
func ReadHistory(ctx context.Context, v Volume, j Job) ([]Sample, error) {
	return new(History).Read(ctx, v, j)
}

// History reads one job's speed history and remembers the chunks it has
// read: they're written once and never changed, so following a download
// reads only the new ones.
type History struct {
	mu     sync.Mutex
	chunks map[string][]Sample
}

func (h *History) Read(ctx context.Context, v Volume, j Job) ([]Sample, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.chunks == nil {
		h.chunks = map[string][]Sample{}
	}
	dir := j.Dir() + "/speed"
	names, err := v.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []Sample
	for _, name := range names {
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		rows, ok := h.chunks[name]
		if !ok {
			if rows, err = readChunk(ctx, v, dir+"/"+name); err != nil {
				return nil, err
			}
			h.chunks[name] = rows
		}
		out = append(out, rows...)
	}
	return out, nil
}

func readChunk(ctx context.Context, v Volume, p string) ([]Sample, error) {
	b, err := readValid(ctx, v, p, func(b []byte) error {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if err := checkJSON([]byte(line)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var rows []Sample
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var s Sample
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		rows = append(rows, s)
	}
	return rows, nil
}
