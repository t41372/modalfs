package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/t41372/modalfs/internal/config"
)

// "Start at login": a LaunchAgent on macOS, a systemd user unit on Linux.
// Installing only registers it for the next login; it doesn't start a
// second daemon now.
//
// The file records the program by a path that survives upgrades (the
// Homebrew symlink, the app bundle), carries MODALFS_HOME, and gives the
// daemon time to finish uploading when it's stopped at logout. Each start
// checks it and rewrites it when the program it names is gone.

const launchLabel = "dev.modalfs.daemon"

// stopTimeout is how long launchd or systemd waits for the daemon to finish
// uploading and unmount when it stops it (at logout), before killing it.
// Whatever is cut short stays journaled and is uploaded at the next mount.
const stopTimeout = 30 * 60 // seconds

type ServiceView struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	// The program it starts, and whether it's gone (the app was moved or
	// deleted): then start at login does nothing.
	Binary        string `json:"binary,omitempty"`
	BinaryMissing bool   `json:"binary_missing,omitempty"`
}

// serviceID names the service. A daemon with its own MODALFS_HOME gets a
// service of its own, so it never touches the default one.
func serviceID() (label, unit string) {
	if os.Getenv("MODALFS_HOME") == "" {
		return launchLabel, "modalfs.service"
	}
	dir, _ := filepath.Abs(config.Dir())
	sum := sha256.Sum256([]byte(dir))
	h := hex.EncodeToString(sum[:4])
	return launchLabel + "." + h, "modalfs-" + h + ".service"
}

func servicePath() string {
	home, _ := os.UserHomeDir()
	label, unit := serviceID()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	}
	return filepath.Join(home, ".config", "systemd", "user", unit)
}

func serviceStatus() ServiceView {
	v := ServiceView{Supported: runtime.GOOS == "darwin" || runtime.GOOS == "linux", Path: servicePath()}
	raw, err := os.ReadFile(v.Path)
	if err != nil {
		return v
	}
	v.Installed = true
	v.Binary = parseService(runtime.GOOS, raw).exe
	if v.Binary != "" {
		if _, err := os.Stat(v.Binary); errors.Is(err, fs.ErrNotExist) {
			v.BinaryMissing = true
		}
	}
	return v
}

// selfExe is this program's actual file, for starting a daemon now.
func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// stableExe is the path to record for starting at login: one that keeps
// working when the program is upgraded. Symlinks aren't resolved (Homebrew's
// bin/modalfs outlives each versioned Cellar folder), except to find an app
// bundle, which is recorded through the bundle.
func stableExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe = filepath.Clean(exe)
	// Linux reports the resolved file; the name it was started by is the stable one.
	if a := os.Args[0]; a != "" {
		var p string
		if strings.Contains(a, "/") {
			p, err = filepath.Abs(a)
		} else {
			p, err = exec.LookPath(a)
		}
		if err == nil && sameFile(p, exe) {
			exe = filepath.Clean(p)
		}
	}
	// /usr/local/bin/modalfs → /Applications/modalfs.app/Contents/Resources/bin/modalfs
	if !strings.Contains(exe, ".app/Contents/") {
		if real, err := filepath.EvalSymlinks(exe); err == nil && strings.Contains(real, ".app/Contents/") {
			exe = real
		}
	}
	if strings.Contains(exe, "/AppTranslocation/") {
		return "", errors.New("macOS is running modalfs from a temporary copy; move modalfs.app to the Applications folder, open it from there, and try again")
	}
	return exe, nil
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// bundleID returns the identifier of the app bundle exe is in, or "".
func bundleID(exe string) string {
	i := strings.Index(exe, ".app/Contents/")
	if i < 0 {
		return ""
	}
	raw, err := os.ReadFile(exe[:i+len(".app/Contents/")] + "Info.plist")
	if err != nil {
		return ""
	}
	m := regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`).FindSubmatch(raw)
	if m == nil {
		return ""
	}
	return html.UnescapeString(string(m[1]))
}

// serviceSpec is what a service file says.
type serviceSpec struct {
	exe, home, transport, bundle string
}

func currentSpec(exe string) serviceSpec {
	return serviceSpec{exe: exe, home: os.Getenv("MODALFS_HOME"), transport: os.Getenv("MODALFS_TRANSPORT"), bundle: bundleID(exe)}
}

// serviceFile renders the LaunchAgent (darwin) or systemd unit (linux).
func serviceFile(goos string, s serviceSpec) (string, error) {
	label, _ := serviceID()
	env := [][2]string{}
	if s.home != "" {
		env = append(env, [2]string{"MODALFS_HOME", s.home})
	}
	if s.transport != "" {
		env = append(env, [2]string{"MODALFS_TRANSPORT", s.transport})
	}
	switch goos {
	case "darwin":
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
		fmt.Fprintf(&b, "\t<key>Label</key><string>%s</string>\n", xmlEscape(label))
		fmt.Fprintf(&b, "\t<key>ProgramArguments</key>\n\t<array><string>%s</string><string>daemon</string></array>\n", xmlEscape(s.exe))
		b.WriteString("\t<key>RunAtLoad</key><true/>\n")
		b.WriteString("\t<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>\n")
		b.WriteString("\t<key>ProcessType</key><string>Interactive</string>\n")
		fmt.Fprintf(&b, "\t<key>ExitTimeOut</key><integer>%d</integer>\n", stopTimeout)
		if s.bundle != "" {
			fmt.Fprintf(&b, "\t<key>AssociatedBundleIdentifiers</key><string>%s</string>\n", xmlEscape(s.bundle))
		}
		logPath := LogPath() // s.home is this process's MODALFS_HOME (see serviceID)
		fmt.Fprintf(&b, "\t<key>StandardErrorPath</key><string>%s</string>\n", xmlEscape(logPath))
		fmt.Fprintf(&b, "\t<key>StandardOutPath</key><string>%s</string>\n", xmlEscape(logPath))
		if len(env) > 0 {
			b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
			for _, kv := range env {
				fmt.Fprintf(&b, "\t\t<key>%s</key><string>%s</string>\n", kv[0], xmlEscape(kv[1]))
			}
			b.WriteString("\t</dict>\n")
		}
		b.WriteString("</dict>\n</plist>\n")
		return b.String(), nil
	case "linux":
		var b strings.Builder
		b.WriteString("[Unit]\nDescription=modalfs - Modal Volume mounts\nWants=network-online.target\nAfter=network-online.target\n\n")
		fmt.Fprintf(&b, "[Service]\nExecStart=%s daemon\nRestart=on-failure\nTimeoutStopSec=%d\n", strconv.Quote(s.exe), stopTimeout)
		for _, kv := range env {
			fmt.Fprintf(&b, "Environment=%s\n", strconv.Quote(kv[0]+"="+kv[1]))
		}
		b.WriteString("\n[Install]\nWantedBy=default.target\n")
		return b.String(), nil
	}
	return "", fmt.Errorf("start at login is not supported on %s", goos)
}

var (
	plistExe = regexp.MustCompile(`<key>ProgramArguments</key>\s*<array>\s*<string>([^<]*)</string>`)
	plistEnv = regexp.MustCompile(`<key>(MODALFS_HOME|MODALFS_TRANSPORT)</key>\s*<string>([^<]*)</string>`)
)

// parseService reads back what serviceFile wrote (and older versions of it).
func parseService(goos string, raw []byte) serviceSpec {
	var s serviceSpec
	set := func(k, v string) {
		switch k {
		case "MODALFS_HOME":
			s.home = v
		case "MODALFS_TRANSPORT":
			s.transport = v
		}
	}
	if goos == "darwin" {
		if m := plistExe.FindSubmatch(raw); m != nil {
			s.exe = html.UnescapeString(string(m[1]))
		}
		for _, m := range plistEnv.FindAllSubmatch(raw, -1) {
			set(string(m[1]), html.UnescapeString(string(m[2])))
		}
		return s
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			if q, err := strconv.QuotedPrefix(v); err == nil {
				s.exe, _ = strconv.Unquote(q)
			} else {
				s.exe, _, _ = strings.Cut(v, " ")
			}
		}
		if v, ok := strings.CutPrefix(line, "Environment="); ok {
			if u, err := strconv.Unquote(v); err == nil {
				v = u
			}
			if k, val, ok := strings.Cut(v, "="); ok {
				set(k, val)
			}
		}
	}
	return s
}

func InstallService() error {
	exe, err := stableExe()
	if err != nil {
		return err
	}
	return writeService(currentSpec(exe))
}

func writeService(s serviceSpec) error {
	if _, err := os.Stat(s.exe); err != nil {
		return fmt.Errorf("can't set up start at login: %w", err)
	}
	content, err := serviceFile(runtime.GOOS, s)
	if err != nil {
		return err
	}
	p := servicePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := config.WriteAtomic(p, []byte(content)); err != nil {
		return err
	}
	if err := os.Chmod(p, 0o644); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		_, unit := serviceID()
		systemctl("daemon-reload")
		systemctl("enable", unit)
	}
	return nil
}

// systemctl runs `systemctl --user`; tests replace it.
var systemctl = func(args ...string) {
	_ = exec.Command("systemctl", append([]string{"--user"}, args...)...).Run()
}

func UninstallService() error {
	p := servicePath()
	if runtime.GOOS == "linux" {
		_, unit := serviceID()
		systemctl("disable", unit)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// checkService repairs an installed service file at startup: when the
// program it starts is gone (the app was moved, an old install deleted), it
// is pointed at this one; an older file also gets the newer settings. It
// leaves alone a file that starts another program that still exists, and
// one written for another MODALFS_HOME.
func (d *Daemon) checkService() {
	raw, err := os.ReadFile(servicePath())
	if err != nil {
		return
	}
	got := parseService(runtime.GOOS, raw)
	if got.exe == "" || got.home != os.Getenv("MODALFS_HOME") {
		return
	}
	want := got
	if _, err := os.Stat(got.exe); errors.Is(err, fs.ErrNotExist) {
		exe, err := stableExe()
		if err != nil || isTempBuild(exe) {
			d.log.Warn("start at login runs a program that's gone, and this one can't replace it", "missing", got.exe, "err", err)
			return
		}
		want.exe = exe
	}
	want.bundle = bundleID(want.exe)
	content, err := serviceFile(runtime.GOOS, want)
	if err != nil || content == string(raw) {
		return
	}
	if err := writeService(want); err != nil {
		d.log.Warn("could not update start at login", "err", err)
		return
	}
	d.log.Info("updated start at login", "path", servicePath(), "program", want.exe, "was", got.exe)
	if want.exe != got.exe {
		d.emitC("info", "service_updated", "", "", fmt.Sprintf("start at login ran %s, which is gone; it now runs %s", got.exe, want.exe), got.exe, want.exe)
	}
}

// isTempBuild reports whether exe is a throwaway build (go run, go test).
func isTempBuild(exe string) bool {
	return strings.Contains(exe, "/go-build") || strings.HasPrefix(exe, filepath.Clean(os.TempDir())+string(filepath.Separator))
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
