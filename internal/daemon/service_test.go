package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The service files are rendered and read back on every OS; only the
// checks that write one need this one's.
func TestServiceFileRoundTrip(t *testing.T) {
	t.Setenv("MODALFS_HOME", "")
	for _, goos := range []string{"darwin", "linux"} {
		in := serviceSpec{exe: "/Applications/mod & fs.app/Contents/MacOS/modalfs", home: "/tmp/my home", transport: "http1", bundle: "dev.modalfs.app"}
		if goos == "linux" {
			in.bundle = ""
		}
		raw, err := serviceFile(goos, in)
		if err != nil {
			t.Fatal(err)
		}
		if got := parseService(goos, []byte(raw)); got.exe != in.exe || got.home != in.home || got.transport != in.transport {
			t.Errorf("%s: read back %+v, want %+v\n%s", goos, got, in, raw)
		}
		for _, want := range map[string][]string{
			"darwin": {"<key>ExitTimeOut</key><integer>1800</integer>", "<key>MODALFS_HOME</key><string>/tmp/my home</string>", "&amp;", "<key>AssociatedBundleIdentifiers</key>"},
			"linux":  {"TimeoutStopSec=1800", `Environment="MODALFS_HOME=/tmp/my home"`, "Wants=network-online.target"},
		}[goos] {
			if !strings.Contains(raw, want) {
				t.Errorf("%s: no %q in\n%s", goos, want, raw)
			}
		}
	}
}

// Files written by modalfs 0.1.0, which had no MODALFS_HOME and no stop timeout.
func TestParseOldServiceFiles(t *testing.T) {
	plist := `<dict>
	<key>Label</key><string>dev.modalfs.daemon</string>
	<key>ProgramArguments</key>
	<array><string>/opt/homebrew/Cellar/modalfs/0.1.0/bin/modalfs</string><string>daemon</string></array>
</dict>`
	if got := parseService("darwin", []byte(plist)); got.exe != "/opt/homebrew/Cellar/modalfs/0.1.0/bin/modalfs" || got.home != "" {
		t.Errorf("plist: %+v", got)
	}
	unit := "[Service]\nExecStart=\"/home/me/bin/modalfs\" daemon\nRestart=on-failure\nEnvironment=MODALFS_TRANSPORT=http1\n"
	if got := parseService("linux", []byte(unit)); got.exe != "/home/me/bin/modalfs" || got.transport != "http1" {
		t.Errorf("unit: %+v", got)
	}
}

func TestServiceIDPerHome(t *testing.T) {
	t.Setenv("MODALFS_HOME", "")
	if l, u := serviceID(); l != launchLabel || u != "modalfs.service" {
		t.Fatalf("default service is %s, %s", l, u)
	}
	t.Setenv("MODALFS_HOME", t.TempDir())
	if l, u := serviceID(); l == launchLabel || u == "modalfs.service" {
		t.Fatalf("a MODALFS_HOME daemon uses the default service %s, %s", l, u)
	}
}

func testDaemon(t *testing.T) *Daemon {
	return &Daemon{log: slog.New(slog.NewTextHandler(io.Discard, nil)), running: map[string]*runMount{}}
}

// checkService rewrites a file whose program is gone, upgrades an old one
// in place, and leaves alone one that runs another program that exists.
func TestCheckService(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no service files here")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MODALFS_HOME", t.TempDir())
	systemctl = func(...string) {}
	d := testDaemon(t)
	p := servicePath()
	if !strings.HasPrefix(p, os.Getenv("HOME")) {
		t.Fatalf("service path %s isn't under the test HOME", p)
	}
	write := func(s serviceSpec) {
		raw, err := serviceFile(runtime.GOOS, s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func() serviceSpec {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return parseService(runtime.GOOS, raw)
	}
	home := os.Getenv("MODALFS_HOME")

	// Another program that exists: kept.
	other := filepath.Join(t.TempDir(), "other-modalfs")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(serviceSpec{exe: other, home: home})
	d.checkService()
	if got := read().exe; got != other {
		t.Fatalf("an existing program was replaced: %s", got)
	}
	if v := serviceStatus(); !v.Installed || v.BinaryMissing || v.Binary != other {
		t.Fatalf("status %+v", v)
	}

	// The program is gone: reported, and repaired with this one (a test
	// binary is a throwaway build, so it's only reported).
	gone := filepath.Join(t.TempDir(), "gone", "modalfs")
	write(serviceSpec{exe: gone, home: home})
	if v := serviceStatus(); !v.BinaryMissing || v.Binary != gone {
		t.Fatalf("a missing program isn't reported: %+v", v)
	}
	d.checkService()
	exe, err := stableExe()
	if err != nil {
		t.Fatal(err)
	}
	if want := gone; isTempBuild(exe) {
		if got := read().exe; got != want {
			t.Fatalf("a throwaway build replaced the program: %s", got)
		}
	} else if got := read().exe; got != exe {
		t.Fatalf("not repaired: %s, want %s", got, exe)
	}

	// Written for another MODALFS_HOME: left alone, even with its program gone.
	write(serviceSpec{exe: gone, home: "/somewhere/else"})
	before, _ := os.ReadFile(p)
	d.checkService()
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Fatal("a service file for another MODALFS_HOME was rewritten")
	}
}

func TestStableExeKeepsSymlink(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "modalfs")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	old := os.Args[0]
	defer func() { os.Args[0] = old }()
	os.Args[0] = link
	got, err := stableExe()
	if err != nil {
		t.Fatal(err)
	}
	if got != link && runtime.GOOS == "linux" {
		t.Fatalf("stableExe = %s, want the symlink %s", got, link)
	}
	if strings.Contains(got, "..") {
		t.Fatalf("not clean: %s", got)
	}
}
