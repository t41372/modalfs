package downloads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/t41372/modalfs/internal/downloads/remote"
)

// Runner runs the agent for a job: on Modal (Modal) or, for tests, as a
// local process over a folder (Local).
type Runner interface {
	// Start runs one call of the job (a new download or a resume) and
	// returns its call ID. Whatever the job's folder on the volume holds
	// from earlier calls is picked up.
	Start(ctx context.Context, j Job) (string, error)
	// Result returns a call's summary once it has ended; ok is false while
	// it runs, and while that can't be told (err says why: a poll failed).
	Result(ctx context.Context, callID string) (summary map[string]any, ok bool, err error)
	// Cancel stops a call at once. The agent gets no chance to save, so ask
	// it to pause first (Pause).
	Cancel(ctx context.Context, callID string) error
	// Volume reads the job's records.
	Volume(j Job) Volume
}

// Pause asks a running agent, through its live channel, to pause: it saves
// the downloader's state and ends the call with "paused". The call's
// Result says when it has.
func Pause(ctx context.Context, s Status, j Job) error {
	if s.LiveURL == "" || !s.Running() {
		return fmt.Errorf("download %s isn't running (it's %s)", j.ID, s.State)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.LiveURL+"/pause", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Modalfs-Token", j.LiveToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the download's container: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("the download's container answered %s to pause", resp.Status)
	}
	return nil
}

// Local runs the agent as a local process (python3 app.py run) with a
// folder standing in for every volume. It's for tests: everything but
// Modal itself.
type Local struct {
	Root   string // the "volume"
	Python string // python3 by default
	Env    []string
	Extra  map[string]any // added to each call's job (tests shorten the agent's intervals)

	mu    sync.Mutex
	app   string
	calls map[string]*localCall
}

type localCall struct {
	cmd  *exec.Cmd
	out  bytes.Buffer
	errb bytes.Buffer
	done chan struct{}
	err  error
}

func (l *Local) Start(ctx context.Context, j Job) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.app == "" {
		dir, err := os.MkdirTemp("", "mfs-agent-")
		if err != nil {
			return "", err
		}
		l.app = filepath.Join(dir, "app.py")
		if err := os.WriteFile(l.app, remote.App, 0o600); err != nil {
			return "", err
		}
		l.calls = map[string]*localCall{}
	}
	args := j.args("local", MaxRun)
	for k, v := range l.Extra {
		args[k] = v
	}
	raw, _ := json.Marshal(args)
	jobFile := filepath.Join(filepath.Dir(l.app), j.ID+"-"+strconv.Itoa(len(l.calls))+".json")
	if err := os.WriteFile(jobFile, raw, 0o600); err != nil {
		return "", err
	}
	py := l.Python
	if py == "" {
		py = "python3"
	}
	c := &localCall{done: make(chan struct{})}
	c.cmd = exec.Command(py, l.app, "run", jobFile, "--root", l.Root)
	c.cmd.Env = append(os.Environ(), l.Env...)
	c.cmd.Stdout, c.cmd.Stderr = &c.out, &c.errb
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.cmd.Start(); err != nil {
		return "", err
	}
	id := "local-" + strconv.Itoa(c.cmd.Process.Pid)
	l.calls[id] = c
	go func() { c.err = c.cmd.Wait(); close(c.done) }()
	return id, nil
}

func (l *Local) call(id string) (*localCall, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.calls[id]
	if !ok {
		return nil, fmt.Errorf("no call %s", id)
	}
	return c, nil
}

func (l *Local) Result(_ context.Context, id string) (map[string]any, bool, error) {
	c, err := l.call(id)
	if err != nil {
		return nil, false, err
	}
	select {
	case <-c.done:
	default:
		return nil, false, nil
	}
	if c.err != nil {
		return nil, true, fmt.Errorf("agent failed: %w: %s", c.err, lastLines(c.errb.String(), 20))
	}
	lines := strings.Split(strings.TrimSpace(c.out.String()), "\n")
	var summary map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
		return nil, true, fmt.Errorf("agent printed no summary: %s", lastLines(c.out.String(), 5))
	}
	return summary, true, nil
}

func (l *Local) Cancel(_ context.Context, id string) error {
	c, err := l.call(id)
	if err != nil {
		return err
	}
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
	select {
	case <-c.done:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("the agent didn't stop")
	}
}

func (l *Local) Volume(Job) Volume { return DirVolume(l.Root) }

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
