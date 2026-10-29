//go:build desktop_smoke

// Package desktoptest runs the real AppKit/WebKit app against an isolated
// daemon. It checks the native properties and reads mutations back from the
// daemon: a screenshot or HTTP 200 alone cannot prove that either works.
package desktoptest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/daemon"
)

type inspector struct {
	endpoint string
	token    string
	client   *http.Client
}

func (m inspector) call(name string, args map[string]any, result any) error {
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, m.endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.token)
	r, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	var reply struct {
		Error  any `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reply); err != nil {
		return err
	}
	if reply.Error != nil || reply.Result.IsError || len(reply.Result.Content) == 0 {
		return fmt.Errorf("inspection %s failed: %+v", name, reply)
	}
	if result != nil {
		return json.Unmarshal([]byte(reply.Result.Content[0].Text), result)
	}
	return nil
}

type windowState struct {
	Saved  string `json:"saved"`
	Dark   bool   `json:"dark"`
	Marker bool   `json:"marker"`
	Page   string `json:"page"`
	Native struct {
		SystemAppearance string `json:"systemAppearance"`
		RegularPolicy    bool   `json:"regularPolicy"`
		QuitAcknowledged bool   `json:"quitAcknowledged"`
		Quitting         bool   `json:"quitting"`
		Windows          []struct {
			Visible         bool   `json:"visible"`
			Opaque          bool   `json:"opaque"`
			DrawsBackground *bool  `json:"drawsBackground"`
			Appearance      string `json:"appearance"`
			Materials       []struct {
				FullScreenUI bool `json:"fullScreenUI"`
				BehindWindow bool `json:"behindWindow"`
			} `json:"materials"`
		} `json:"windows"`
	} `json:"native"`
}

const inspectJS = `
const state = await (await fetch('/api/state')).json();
const response = await fetch('/wails/runtime', {
  method:'POST', headers:{'Content-Type':'application/json','x-wails-client-id':window._wails.clientId},
  body:JSON.stringify({object:0,method:0,args:{'call-id':'native-smoke',methodName:'main.App.NativeAudit',args:[]}})
});
if (!response.ok) throw new Error(await response.text());
return {saved:state.settings.appearance, dark:document.documentElement.classList.contains('dark'),
  marker:document.querySelector('meta[name="modalfs-desktop"]')?.content==='1',
  page:location.search, native:await response.json()};`

func (m inspector) state() (windowState, error) {
	var state windowState
	err := m.call("js_eval", map[string]any{"js": inspectJS, "timeout_ms": 5000}, &state)
	return state, err
}

func (m inspector) appCall(method string) error {
	payload, err := json.Marshal(map[string]any{"object": 0, "method": 0,
		"args": map[string]any{"call-id": "smoke-" + method, "methodName": "main.App." + method, "args": []any{}}})
	if err != nil {
		return err
	}
	js := `const r = await fetch('/wails/runtime', {
  method:'POST', headers:{'Content-Type':'application/json','x-wails-client-id':window._wails.clientId},
  body:` + strconv.Quote(string(payload)) + `
});
if (!r.ok) throw new Error(await r.text());
return null;`
	return m.call("js_eval", map[string]any{"js": js}, nil)
}

func runningDaemonPID(t *testing.T) int {
	t.Helper()
	if !daemon.NewClient().Alive() {
		t.Fatal("the isolated daemon is not answering")
	}
	raw, err := os.ReadFile(daemon.InfoPath())
	if err != nil {
		t.Fatal(err)
	}
	var info daemon.Info
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatal(err)
	}
	return info.PID
}

func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func run(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir, c.Env = dir, env
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", name, err, out)
	}
}

func TestNativeDesktop(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.MkdirTemp("/tmp", "mfs-app-") // short Unix socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("MODALFS_HOME", home)
	t.Setenv("MODALFS_NO_KEYCHAIN", "1")
	t.Setenv("MODAL_CONFIG_PATH", filepath.Join(home, "modal.toml"))
	if err := os.WriteFile(filepath.Join(home, "modal.toml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"settings": map[string]any{"appearance": "light", "language": "en",
		"mount_root": filepath.Join(home, "volumes"), "cache_dir": filepath.Join(home, "cache"), "cache_size_gb": 2}}
	raw, _ := json.Marshal(settings)
	if err := os.WriteFile(filepath.Join(home, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "ARCHS="+runtime.GOARCH, "MODALFS_BUILD_DIR="+home,
		"WAILS_MCP_HOST=127.0.0.1", "WAILS_MCP_PORT=0", "WAILS_MCP_TOKEN="+hex.EncodeToString(token), "WAILS_MCP_HIDE_CURSOR=1",
		"CGO_CFLAGS=-O2 -g -mmacosx-version-min=13.0", "CGO_LDFLAGS=-mmacosx-version-min=13.0")
	run(t, root, env, "sh", "packaging/build-macos-app.sh")
	app := filepath.Join(home, "modalfs.app")
	binary := filepath.Join(app, "Contents/MacOS/modalfs")
	diagnostic := filepath.Join(home, "modalfs-inspected")
	run(t, root, env, "go", "build", "-race", "-trimpath", "-tags", "desktop,production,mcp,private_mac_apis", "-o", diagnostic, "./cmd/modalfs-gui")
	if err := os.Rename(diagnostic, binary); err != nil {
		t.Fatal(err)
	}
	info := filepath.Join(app, "Contents/Info.plist")
	run(t, root, env, "/usr/libexec/PlistBuddy", "-c", fmt.Sprintf("Set :CFBundleIdentifier dev.modalfs.smoke-%d", os.Getpid()), info)
	run(t, root, env, "codesign", "--force", "--sign", "-", app)
	log, err := os.Create(filepath.Join(home, "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	var process *exec.Cmd
	m := inspector{token: hex.EncodeToString(token), client: &http.Client{Timeout: 8 * time.Second}}
	start := func() {
		process = exec.Command(binary)
		process.Env, process.Stdout, process.Stderr = env, log, log
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		m.endpoint = ""
		waitFor(t, "inspection server", func() bool {
			out, _ := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(process.Process.Pid), "-iTCP", "-sTCP:LISTEN", "-Fn").Output()
			for _, line := range strings.Split(string(out), "\n") {
				if address, ok := strings.CutPrefix(line, "n127.0.0.1:"); ok {
					m.endpoint = "http://127.0.0.1:" + address + "/mcp"
					return true
				}
			}
			return false
		})
	}
	t.Cleanup(func() {
		if t.Failed() {
			state, err := m.state()
			t.Logf("last native state: %+v (err %v)", state, err)
		}
		if process != nil && process.Process != nil {
			process.Process.Kill()
			process.Wait()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://modalfsd/api/shutdown?now=1", nil)
		req.Header.Set("X-Modalfs-Request", "1")
		client := http.Client{Transport: daemon.NewClient().Transport()}
		if response, err := client.Do(req); err == nil {
			response.Body.Close()
		}
		if t.Failed() {
			if raw, err := os.ReadFile(filepath.Join(home, "app.log")); err == nil {
				t.Log(string(raw))
			}
		}
	})
	start()
	waitFor(t, "native UI", func() bool { s, err := m.state(); return err == nil && s.Marker && len(s.Native.Windows) == 1 })
	daemonPID := runningDaemonPID(t)
	if err := m.call("emit_event", map[string]any{"name": "nav", "data": []string{"settings", ""}}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "menu navigation", func() bool { s, err := m.state(); return err == nil && strings.Contains(s.Page, "page=settings") })
	for _, mode := range []struct {
		name   string
		button int
	}{{"dark", 3}, {"light", 2}, {"system", 1}, {"dark", 3}} {
		if err := m.call("mouse_click", map[string]any{"selector": fmt.Sprintf(`[aria-label="Appearance"] button:nth-child(%d)`, mode.button)}, nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "saved "+mode.name+" appearance", func() bool {
			s, err := m.state()
			if err != nil || s.Saved != mode.name || len(s.Native.Windows) != 1 {
				return false
			}
			w := s.Native.Windows[0]
			appearance := "NSAppearanceNameAqua"
			if mode.name == "dark" {
				appearance = "NSAppearanceNameDarkAqua"
			}
			if mode.name == "system" {
				appearance = s.Native.SystemAppearance
			}
			return s.Dark == (appearance == "NSAppearanceNameDarkAqua") &&
				w.Appearance == appearance && !w.Opaque &&
				w.DrawsBackground != nil && !*w.DrawsBackground &&
				len(w.Materials) > 0 && w.Materials[0].FullScreenUI && w.Materials[0].BehindWindow
		})
	}
	_ = m.appCall("CloseWindow") // the app may exit before replying
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	process = nil
	if runningDaemonPID(t) != daemonPID {
		t.Fatal("closing only the app replaced its daemon")
	}
	start()
	waitFor(t, "dark appearance after restart", func() bool {
		s, err := m.state()
		return err == nil && s.Saved == "dark" && s.Dark && len(s.Native.Windows) == 1 && s.Native.Windows[0].Appearance == "NSAppearanceNameDarkAqua"
	})
	hideWindow := func() {
		if err := m.call("window_control", map[string]any{"action": "close", "window": "main"}, nil); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "window and Dock icon hidden without quitting", func() bool {
			s, err := m.state()
			return err == nil && len(s.Native.Windows) == 1 && !s.Native.Windows[0].Visible && !s.Native.RegularPolicy
		})
	}
	hideWindow()
	if err := m.appCall("NativeAuditReopen"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "native reopen restores window and Dock icon", func() bool {
		s, err := m.state()
		return err == nil && len(s.Native.Windows) == 1 && s.Native.Windows[0].Visible && s.Native.RegularPolicy
	})
	hideWindow()
	if s, err := m.state(); err != nil || s.Native.QuitAcknowledged || s.Native.Quitting {
		t.Fatalf("quit flags were already set before native termination: %+v, %v", s.Native, err)
	}
	// Hold the UI's state request after QuitAck. The inspector's bare fetch
	// omits X-Modalfs-Request, so we can observe the initial veto and ack.
	const holdQuitJS = `
const originalFetch = window.fetch.bind(window);
let release;
const held = new Promise(resolve => { release = resolve; });
window.__releaseQuit = () => { window.fetch = originalFetch; release(); };
window.fetch = async (input, init) => {
  if (new URL(input, location.href).pathname === '/api/state' &&
      new Headers(init?.headers).get('X-Modalfs-Request') === '1') await held;
  return originalFetch(input, init);
};
return null;`
	if err := m.call("js_eval", map[string]any{"js": holdQuitJS}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.appCall("NativeAuditQuit"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "native termination veto, window shown and UI QuitAck", func() bool {
		s, err := m.state()
		return err == nil && s.Native.QuitAcknowledged && !s.Native.Quitting &&
			len(s.Native.Windows) == 1 && s.Native.Windows[0].Visible && s.Native.RegularPolicy
	})
	if runningDaemonPID(t) != daemonPID {
		t.Fatal("the native termination request replaced its daemon before the UI could stop it")
	}
	// Resume the real UI quit flow. It sets Quitting, stops the daemon and
	// requests final termination; the app may exit before inspection replies.
	_ = m.call("js_eval", map[string]any{"js": "window.__releaseQuit(); return null;"}, nil)
	waitFor(t, "daemon shutdown", func() bool { _, err := os.Stat(daemon.InfoPath()); return os.IsNotExist(err) })
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	process = nil
}
