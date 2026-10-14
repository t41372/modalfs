// modalfs mounts Modal Volumes (v2) as local folders without starting any
// Modal compute. A background daemon owns the mounts; this CLI and the GUI
// both drive it. The one feature that does start compute, remote downloads,
// is off until the user turns it on, and runs only for downloads they add.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/daemon"
	"golang.org/x/term"
)

const usage = `modalfs — Modal Volumes as local folders; mounting starts no Modal compute

Everyday:
  modalfs ui [--print]                      open the modalfs window (starts the background daemon);
                                            --print shows the browser URL instead
  modalfs status                            mounts and their sync state
  modalfs mount VOLUME [MOUNTPOINT]         mount (default ~/ModalVolumes/VOLUME); saved for next time
        [--account A] [--env E] [--ro] [--auto] [--mode auto|nfs|fuse]
  modalfs unmount VOLUME|PATH [--now]       commit pending writes, then unmount; --now unmounts
                                            at once and uploads the rest in the background
  modalfs sync VOLUME                       commit pending writes now
  modalfs forget VOLUME                     remove a saved mount (the volume is untouched)
  modalfs info VOLUME [--measure]           size, files waiting to upload, open conflicts;
                                            --measure lists the volume to measure its size
  modalfs conflicts VOLUME [--handled]      conflict copies; --handled marks them all handled

Volumes:
  modalfs volume ls [--account A] [--env E]
  modalfs volume create NAME [--label L] [--account A] [--env E] [--mount]
  modalfs volume rename NAME NEWNAME [--account A] [--env E]
  modalfs volume label NAME [LABEL | --clear]   a name shown instead of NAME, kept in the volume
  modalfs volume rm NAME [--account A] [--env E] [--yes]

Encrypted volumes (Modal sees only ciphertext; lose the key and the files are gone):
  modalfs volume create NAME --encrypt      shows the new volume's key; save it before going on
  modalfs unlock VOLUME [--remember=false]  unlock with the key (typed, or from stdin); it's
                                            remembered on this computer unless --remember=false
  modalfs lock VOLUME                       forget the key on this computer (unmount first)
  modalfs volume key NAME                   show the key, where this computer remembers it
  modalfs volume decrypt NAME --to FOLDER   copy every file out, decrypted, with only the key

Remote downloads (off until enabled; each runs in a Modal Function billed to your account):
  modalfs download add VOLUME[:/FOLDER] URL...   download into a volume while this computer sleeps
  modalfs download ls|show|pause|resume|cancel|logs|rm
  modalfs download help                     everything else, and how to turn them on

Accounts (each mount is pinned to one; the Modal CLI's active profile is never used):
  modalfs account add [--label L] [--token-id ID]   token secret is prompted, stored in the keychain
  modalfs account import [PROFILE...]       copy profiles from ~/.modal.toml
  modalfs account ls
  modalfs account check ACCOUNT              ask Modal whether the token still works
  modalfs account token ACCOUNT             replace an expired token (same workspace)
  modalfs account rename ACCOUNT NEWNAME
  modalfs account env ACCOUNT ENVIRONMENT   the environment new mounts use
  modalfs account rm ACCOUNT

Background process:
  modalfs daemon                            run the daemon in the foreground
  modalfs quit [--now]                      commit everything, unmount, stop the daemon; --now
                                            doesn't wait: the rest is uploaded at the next mount
  modalfs service install|uninstall         start at login (LaunchAgent / systemd user unit)
  modalfs cache clear                       delete the local block cache (changes not yet
                                            uploaded are kept)

Without the daemon:
  modalfs run VOLUME MOUNTPOINT [flags]     foreground mount until Ctrl-C (see modalfs run -h)
  modalfs serve VOLUME [flags]              NFS server only
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
	case "version", "--version":
		fmt.Println("modalfs", daemon.VersionBuild(), runtime.GOOS+"/"+runtime.GOARCH)
	case "daemon":
		lvl := slog.LevelInfo
		if os.Getenv("MODALFS_DEBUG") != "" {
			lvl = slog.LevelDebug
		}
		log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
		slog.SetDefault(log)
		if err := daemon.Run(log); errors.Is(err, daemon.ErrAlreadyRunning) {
			// exit 0, so launchd doesn't keep restarting us
			fmt.Fprintln(os.Stderr, "modalfs: another daemon is already running; this one is not needed")
		} else if err != nil {
			fatal(err)
		}
	case "run", "serve":
		runLegacy(cmd, args)
	case "ui":
		cmdUI(args)
	case "status", "ls":
		cmdStatus()
	case "mount":
		cmdMount(args)
	case "unmount", "umount":
		cmdUnmount(args)
	case "sync":
		cmdSimpleMount(args, "sync")
	case "forget":
		cmdForget(args)
	case "info":
		cmdInfo(args)
	case "conflicts":
		cmdConflicts(args)
	case "unlock", "lock":
		cmdEncryption(cmd, args)
	case "volume", "volumes", "vol":
		cmdVolume(args)
	case "download", "downloads", "dl":
		cmdDownload(args)
	case "account", "accounts":
		cmdAccount(args)
	case "quit", "stop":
		cmdQuit(args)
	case "cache":
		if len(args) != 1 || args[0] != "clear" {
			fatal(errors.New("usage: modalfs cache clear"))
		}
		must(client().Do("POST", "/api/cache/clear", nil, nil))
		fmt.Println("cache cleared")
	case "service":
		if len(args) != 1 {
			fatal(errors.New("usage: modalfs service install|uninstall"))
		}
		switch args[0] {
		case "install":
			must(daemon.InstallService())
			fmt.Println("modalfs will start at login")
		case "uninstall":
			must(daemon.UninstallService())
			fmt.Println("removed start-at-login")
		default:
			fatal(errors.New("usage: modalfs service install|uninstall"))
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "modalfs:", err)
	os.Exit(1)
}

func must(err error) {
	if err != nil {
		fatal(err)
	}
}

func client() *daemon.Client {
	c := daemon.NewClient()
	must(c.Ensure())
	return c
}

func state(c *daemon.Client) daemon.StateView {
	var st daemon.StateView
	must(c.Do("GET", "/api/state", nil, &st))
	return st
}

// parseFlags lets flags appear after positional args.
func parseFlags(fl *flag.FlagSet, args []string) []string {
	var pos []string
	for len(args) > 0 {
		must(fl.Parse(args))
		args = fl.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos
}

func pickAccount(st daemon.StateView, want string) config.Account {
	if len(st.Accounts) == 0 {
		fatal(errors.New("no accounts yet: run `modalfs account add` or `modalfs account import`"))
	}
	if want == "" {
		if len(st.Accounts) == 1 {
			return st.Accounts[0]
		}
		var names []string
		for _, a := range st.Accounts {
			names = append(names, a.Label)
		}
		fatal(fmt.Errorf("several accounts; pick one with --account (%s)", strings.Join(names, ", ")))
	}
	for _, a := range st.Accounts {
		if a.ID == want || strings.EqualFold(a.Label, want) || a.Workspace == want {
			return a
		}
	}
	fatal(fmt.Errorf("no account %q", want))
	return config.Account{}
}

func findMount(st daemon.StateView, key string) daemon.MountView {
	abs, _ := filepath.Abs(key)
	var hits []daemon.MountView
	for _, m := range st.Mounts {
		if m.ID == key || m.VolumeName == key || m.Mountpoint == abs {
			hits = append(hits, m)
		}
	}
	if len(hits) == 1 {
		return hits[0]
	}
	if len(hits) > 1 {
		fatal(fmt.Errorf("%q matches several mounts; use the mount path", key))
	}
	fatal(fmt.Errorf("no saved mount %q (see `modalfs status`)", key))
	return daemon.MountView{}
}

func human(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

func cmdStatus() {
	c := daemon.NewClient()
	if !c.Alive() {
		fmt.Println("modalfs is not running (start it with `modalfs ui` or `modalfs mount`)")
		return
	}
	st := state(c)
	if len(st.Mounts) == 0 {
		fmt.Println("no saved mounts")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VOLUME\tLABEL\tACCOUNT\tSTATE\tMOUNTPOINT\tPENDING\tDOWN/UP")
	for _, m := range st.Mounts {
		pending, traffic := "-", "-"
		if m.Status != nil {
			pending = fmt.Sprintf("%d", m.Status.PendingFiles)
			traffic = human(m.Status.BytesDown) + " / " + human(m.Status.BytesUp)
			if m.DownSpeed > 0 || m.UpSpeed > 0 {
				traffic += fmt.Sprintf(" (now %s/s / %s/s)", human(m.DownSpeed), human(m.UpSpeed))
			}
		}
		state := m.State
		if m.State == daemon.StateError {
			state = "error: " + m.Error
		}
		if s := m.Status; s != nil && s.UploadError != "" {
			state += ", can't upload: " + s.UploadError
		}
		if s := m.Status; s != nil && s.WaitingForRoom {
			state += ", writes waiting for uploads to free room"
		}
		label := m.Label
		if label == "" {
			label = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.VolumeName, label, m.AccountLabel, state, m.Mountpoint, pending, traffic)
	}
	_ = w.Flush()
	fmt.Printf("\non this computer: %s cached + %s not uploaded yet, of %s\n", human(st.Cache.Bytes), human(st.Cache.Pending), human(st.Cache.Max))
}

func cmdMount(args []string) {
	fl := flag.NewFlagSet("mount", flag.ExitOnError)
	acct := fl.String("account", "", "account label or id")
	env := fl.String("env", "", "Modal environment")
	ro := fl.Bool("ro", false, "read-only")
	auto := fl.Bool("auto", false, "mount automatically whenever modalfs starts")
	mode := fl.String("mode", "auto", "auto | nfs | fuse")
	noFilter := fl.Bool("keep-junk", false, "upload .DS_Store / ._* files too")
	pos := parseFlags(fl, args)
	if len(pos) < 1 || len(pos) > 2 {
		fatal(errors.New("usage: modalfs mount VOLUME [MOUNTPOINT] [--account A]"))
	}
	c := client()
	st := state(c)
	a := pickAccount(st, *acct)
	// existing saved mount?
	for _, m := range st.Mounts {
		if m.AccountID == a.ID && m.VolumeName == pos[0] && m.Environment == *env {
			must(c.Do("POST", "/api/mounts/"+m.ID+"/start", nil, nil))
			// read again: a folder named after the label is chosen as it mounts
			for _, x := range state(c).Mounts {
				if x.ID == m.ID {
					m = x
				}
			}
			fmt.Printf("mounted %s at %s\n", m.VolumeName, m.Mountpoint)
			return
		}
	}
	mp := ""
	if len(pos) == 2 {
		mp, _ = filepath.Abs(pos[1])
	}
	in := map[string]any{"account_id": a.ID, "environment": *env, "volume_name": pos[0], "mountpoint": mp,
		"read_only": *ro, "auto_mount": *auto, "mode": *mode, "filter_junk": !*noFilter, "start": true, "wait": true}
	var out struct {
		Mount config.Mount `json:"mount"`
		Error string       `json:"error"`
	}
	must(c.Do("POST", "/api/mounts", in, &out))
	if out.Error != "" {
		fatal(errors.New(out.Error))
	}
	fmt.Printf("mounted %s at %s\n", out.Mount.VolumeName, out.Mount.Mountpoint)
}

func cmdUnmount(args []string) {
	fl := flag.NewFlagSet("unmount", flag.ExitOnError)
	now := fl.Bool("now", false, "unmount now, even if files are open; pending uploads go on in the background, or at the next mount if modalfs quits first")
	force := fl.Bool("force", false, "same as --now")
	pos := parseFlags(fl, args)
	if len(pos) != 1 {
		fatal(errors.New("usage: modalfs unmount VOLUME|PATH [--now]"))
	}
	c := daemon.NewClient()
	if !c.Alive() {
		fatal(errors.New("modalfs is not running"))
	}
	m := findMount(state(c), pos[0])
	q := ""
	if *now || *force {
		q = "?force=1"
	}
	// Still sent when not mounted: it clears a failed mount's error.
	done := make(chan error, 1)
	go func() { done <- c.DoLong("POST", "/api/mounts/"+m.ID+"/stop"+q, nil, nil) }()
	p := newProgress()
	for {
		select {
		case err := <-done:
			p.clear()
			must(err)
			if m.State == daemon.StateStopped || m.State == daemon.StateError {
				fmt.Printf("%s is not mounted\n", m.VolumeName)
				return
			}
			fmt.Printf("unmounted %s\n", m.Mountpoint)
			if q != "" {
				if v, ok := lookupMount(c, m.ID); ok && v.State == daemon.StateStopping {
					fmt.Println("pending changes are still uploading in the background (see `modalfs status`)")
				}
			}
			return
		case <-time.After(time.Second):
			if v, ok := lookupMount(c, m.ID); ok {
				p.show(v)
			}
		}
	}
}

func lookupMount(c *daemon.Client, id string) (daemon.MountView, bool) {
	var st daemon.StateView
	if c.Do("GET", "/api/state", nil, &st) != nil {
		return daemon.MountView{}, false
	}
	for _, m := range st.Mounts {
		if m.ID == id {
			return m, true
		}
	}
	return daemon.MountView{}, false
}

func cmdQuit(args []string) {
	fl := flag.NewFlagSet("quit", flag.ExitOnError)
	now := fl.Bool("now", false, "don't wait for uploads: unmount at once; what's left is uploaded the next time each volume is mounted")
	parseFlags(fl, args)
	c := daemon.NewClient()
	if !c.Alive() {
		fmt.Println("modalfs is not running")
		return
	}
	q := ""
	if *now {
		q = "?now=1"
	}
	must(c.Do("POST", "/api/shutdown"+q, nil, nil))
	fmt.Println("modalfs is quitting: pending changes are uploaded first (Ctrl-C stops watching; `modalfs quit --now` stops waiting)")
	p := newProgress()
	for c.Alive() {
		var st daemon.StateView
		if c.Do("GET", "/api/state", nil, &st) == nil {
			for _, m := range st.Mounts {
				if m.State == daemon.StateStopping {
					p.show(m)
					break
				}
			}
		}
		time.Sleep(time.Second)
	}
	p.clear()
	fmt.Println("modalfs has quit")
}

// progress prints a stopping mount's upload progress on one line of a
// terminal (and every 30 s otherwise).
type progress struct {
	tty    bool
	up     map[string][2]float64 // mount: time, bytes_up of the previous sample
	speed  map[string]float64
	shown  bool
	silent time.Time
}

func newProgress() *progress {
	return &progress{tty: term.IsTerminal(int(os.Stderr.Fd())), up: map[string][2]float64{}, speed: map[string]float64{}}
}

func (p *progress) show(m daemon.MountView) {
	if m.State != daemon.StateStopping || m.Status == nil {
		return
	}
	s := m.Status
	now := float64(time.Now().UnixNano()) / 1e9
	if prev, ok := p.up[m.ID]; ok && now > prev[0] {
		v := (float64(s.BytesUp) - prev[1]) / (now - prev[0])
		p.speed[m.ID] = 0.7*p.speed[m.ID] + 0.3*v
	}
	p.up[m.ID] = [2]float64{now, float64(s.BytesUp)}
	left := s.PendingBytes
	if m.StopPendingBytes > 0 {
		left = min(left, max(0, m.StopPendingBytes-(s.BytesUp-m.StopBytesUp)))
	}
	line := m.VolumeName + ": unmounting"
	if left > 0 {
		line = fmt.Sprintf("%s: uploading before unmount, %s left", m.VolumeName, human(left))
		if sp := p.speed[m.ID]; sp > 1024 {
			line += fmt.Sprintf(" at %s/s, about %s", human(int64(sp)), (time.Duration(float64(left)/sp) * time.Second).Round(time.Second))
		}
	}
	if m.Unmounted {
		line += " (folder already unmounted)"
	}
	if p.tty {
		fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
		p.shown = true
		return
	}
	if time.Since(p.silent) > 30*time.Second {
		fmt.Fprintln(os.Stderr, line)
		p.silent = time.Now()
	}
}

func (p *progress) clear() {
	if p.tty && p.shown {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

func cmdSimpleMount(args []string, action string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("usage: modalfs %s VOLUME|PATH", action))
	}
	c := client()
	m := findMount(state(c), args[0])
	must(c.Do("POST", "/api/mounts/"+m.ID+"/"+action, nil, nil))
	fmt.Println("ok")
}

func cmdForget(args []string) {
	if len(args) != 1 {
		fatal(errors.New("usage: modalfs forget VOLUME|PATH"))
	}
	c := client()
	m := findMount(state(c), args[0])
	must(c.Do("DELETE", "/api/mounts/"+m.ID, nil, nil))
	fmt.Printf("forgot mount %s (%s)\n", m.VolumeName, m.Mountpoint)
}

// cmdInfo shows what the app's volume page shows.
func cmdInfo(args []string) {
	fl := flag.NewFlagSet("info", flag.ExitOnError)
	measure := fl.Bool("measure", false, "list the volume to measure its size (one request per batch of files)")
	pos := parseFlags(fl, args)
	if len(pos) != 1 {
		fatal(errors.New("usage: modalfs info VOLUME|PATH [--measure]"))
	}
	c := client()
	m := findMount(state(c), pos[0])
	fmt.Printf("%s  %s · %s\n", m.VolumeName, m.AccountLabel, m.State)
	if m.Label != "" {
		fmt.Printf("  label       %s\n", m.Label)
	}
	fmt.Printf("  folder      %s\n", m.Mountpoint)
	if m.VolumeID != "" {
		fmt.Printf("  volume ID   %s\n", m.VolumeID)
	}
	type usage struct {
		Last *struct {
			Total, Files, At int64
			Folders          []struct {
				Name  string
				Bytes int64
			}
		}
		Running      bool
		Counted      int64
		CountedFiles int64 `json:"counted_files"`
		Error        string
	}
	var u usage
	if *measure {
		must(c.Do("POST", "/api/mounts/"+m.ID+"/usage", nil, &u))
		for u.Running {
			fmt.Printf("\r  measuring   %s in %d files…", human(u.Counted), u.CountedFiles)
			time.Sleep(time.Second)
			must(c.Do("GET", "/api/mounts/"+m.ID+"/usage", nil, &u))
		}
		fmt.Print("\r\033[K")
		if u.Error != "" {
			fatal(errors.New(u.Error))
		}
	} else {
		must(c.Do("GET", "/api/mounts/"+m.ID+"/usage", nil, &u))
	}
	if u.Last != nil {
		fmt.Printf("  size        %s in %d files (measured %s)\n", human(u.Last.Total), u.Last.Files, time.Unix(u.Last.At, 0).Format("2006-01-02 15:04"))
		for _, f := range u.Last.Folders {
			fmt.Printf("    %-24s %s\n", f.Name+"/", human(f.Bytes))
		}
	} else {
		fmt.Println("  size        not measured (modalfs info --measure)")
	}
	var pending []struct {
		Path  string
		Bytes int64
	}
	must(c.Do("GET", "/api/mounts/"+m.ID+"/pending", nil, &pending))
	if len(pending) > 0 {
		fmt.Printf("  waiting to upload: %d files\n", len(pending))
		for _, p := range pending {
			fmt.Printf("    %s  %s\n", p.Path, human(p.Bytes))
		}
	}
	printConflicts(c, m)
}

func cmdConflicts(args []string) {
	fl := flag.NewFlagSet("conflicts", flag.ExitOnError)
	handled := fl.Bool("handled", false, "mark them all handled (the files are left as they are)")
	pos := parseFlags(fl, args)
	if len(pos) != 1 {
		fatal(errors.New("usage: modalfs conflicts VOLUME|PATH [--handled]"))
	}
	c := client()
	m := findMount(state(c), pos[0])
	if *handled {
		must(c.Do("POST", "/api/mounts/"+m.ID+"/conflicts/clear", map[string]string{}, nil))
		fmt.Println("marked handled")
		return
	}
	if !printConflicts(c, m) {
		fmt.Println("no open conflicts")
	}
}

func printConflicts(c *daemon.Client, m daemon.MountView) bool {
	var cs []daemon.Conflict
	must(c.Do("GET", "/api/mounts/"+m.ID+"/conflicts", nil, &cs))
	if len(cs) == 0 {
		return false
	}
	fmt.Printf("  conflicts: %d (Modal's version kept the name; yours is the copy)\n", len(cs))
	for _, x := range cs {
		fmt.Printf("    %s  →  your version: %s\n", x.Path, x.Copy)
	}
	return true
}

type volRow struct {
	Encrypted bool   `json:"encrypted"`
	Locked    bool   `json:"locked"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
	Format    string `json:"format"`
	Mounted   bool   `json:"mounted"`
}

func listVols(c *daemon.Client, acct, env string) []volRow {
	var vs []volRow
	must(c.Do("GET", "/api/accounts/"+acct+"/volumes?env="+url.QueryEscape(env), nil, &vs))
	return vs
}

// Actions resolve only identity, so broken encryption metadata cannot prevent
// recovering data, supplying a replacement key, or forgetting a missing key.
func lookupVolume(c *daemon.Client, account, env, name string) volRow {
	var v volRow
	must(c.Do("GET", "/api/accounts/"+account+"/volumes/lookup?name="+url.QueryEscape(name)+"&env="+url.QueryEscape(env), nil, &v))
	return v
}

func cmdVolume(args []string) {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	sub := args[0]
	fl := flag.NewFlagSet("volume "+sub, flag.ExitOnError)
	acctF := fl.String("account", "", "account label or id")
	env := fl.String("env", "", "Modal environment")
	yes := fl.Bool("yes", false, "don't ask for confirmation")
	mountIt := fl.Bool("mount", false, "mount right after creating")
	clear := fl.Bool("clear", false, "remove the label")
	labelF := fl.String("label", "", "a name shown instead of NAME, kept in the volume")
	encrypt := fl.Bool("encrypt", false, "encrypt contents and names; save the displayed key before creating")
	remember := fl.Bool("remember", true, "remember the encryption key on this computer")
	to := fl.String("to", "", "local empty folder for decrypted recovery")
	pos := parseFlags(fl, args[1:])
	c := client()
	st := state(c)
	a := pickAccount(st, *acctF)
	byName := func(name string) volRow { return lookupVolume(c, a.ID, *env, name) }
	switch sub {
	case "ls", "list":
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tFORMAT\tCREATED\tID\t")
		for _, v := range listVols(c, a.ID, *env) {
			m := ""
			if v.Encrypted {
				m = "encrypted"
			}
			if v.Locked {
				m = "locked"
			}
			if v.Mounted {
				m += " mounted"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", v.Name, v.Format, time.Unix(v.CreatedAt, 0).Format("2006-01-02"), v.ID, m)
		}
		_ = w.Flush()
	case "create":
		if len(pos) != 1 {
			fatal(errors.New("usage: modalfs volume create NAME"))
		}
		var v volRow
		in := map[string]any{"name": pos[0], "environment": *env, "label": *labelF}
		if *encrypt {
			var fresh struct {
				Key string `json:"key"`
			}
			must(c.Do("POST", "/api/encryption/key", nil, &fresh))
			fmt.Println("Save this key. Losing it means losing every file. Modal Functions cannot read this volume; remote downloads cannot write to it.")
			fmt.Println(fresh.Key)
			if !*yes {
				fmt.Print("Type saved after saving the key: ")
				line, err := bufio.NewReader(os.Stdin).ReadString('\n')
				must(err)
				if strings.TrimSpace(line) != "saved" {
					fatal(errors.New("aborted; no volume was created"))
				}
			}
			in["encrypted"], in["key"], in["remember"] = true, fresh.Key, *remember
		}
		must(c.Do("POST", "/api/accounts/"+a.ID+"/volumes", in, &v))
		fmt.Printf("created %s (%s)\n", v.Name, v.ID)
		if *mountIt {
			cmdMount([]string{pos[0], "--account", a.ID, "--env", *env})
		}
	case "key":
		if len(pos) != 1 {
			fatal(errors.New("usage: modalfs volume key NAME"))
		}
		v := byName(pos[0])
		var out struct {
			Key string `json:"key"`
		}
		must(c.Do("GET", "/api/accounts/"+a.ID+"/volumes/"+v.ID+"/key", nil, &out))
		fmt.Println(out.Key)
	case "decrypt":
		if len(pos) != 1 || *to == "" {
			fatal(errors.New("usage: modalfs volume decrypt NAME --to FOLDER (key from terminal or stdin)"))
		}
		v := byName(pos[0])
		key := readEncryptionKey()
		dest, err := filepath.Abs(*to)
		must(err)
		var out struct {
			Files  int
			Bytes  int64
			Failed []struct{ Path, Error string }
		}
		must(c.DoLong("POST", "/api/accounts/"+a.ID+"/volumes/"+v.ID+"/decrypt", map[string]any{"key": key, "to": dest, "environment": *env}, &out))
		fmt.Printf("recovered %d files (%s) to %s\n", out.Files, human(out.Bytes), dest)
		if len(out.Failed) > 0 {
			fmt.Printf("couldn't recover %d:\n", len(out.Failed))
			for _, f := range out.Failed {
				fmt.Printf("  %s: %s\n", f.Path, f.Error)
			}
			os.Exit(1)
		}
	case "rename", "mv":
		if len(pos) != 2 {
			fatal(errors.New("usage: modalfs volume rename NAME NEWNAME"))
		}
		v := byName(pos[0])
		must(c.Do("POST", "/api/accounts/"+a.ID+"/volumes/"+v.ID+"/rename", map[string]string{"name": pos[1], "environment": *env}, nil))
		fmt.Printf("renamed %s → %s\n", pos[0], pos[1])
	case "label":
		if len(pos) < 1 || len(pos) > 2 || (*clear && len(pos) == 2) {
			fatal(errors.New("usage: modalfs volume label NAME [LABEL | --clear]"))
		}
		v := byName(pos[0])
		path := "/api/accounts/" + a.ID + "/volumes/" + v.ID + "/label"
		var out struct{ Label string }
		if len(pos) == 1 && !*clear {
			must(c.Do("GET", path+"?env="+url.QueryEscape(*env), nil, &out))
		} else {
			label := ""
			if len(pos) == 2 {
				label = pos[1]
			}
			must(c.Do("POST", path, map[string]string{"label": label, "environment": *env}, &out))
		}
		if out.Label == "" {
			fmt.Printf("%s has no label\n", v.Name)
		} else {
			fmt.Printf("%s: %s\n", v.Name, out.Label)
		}
	case "rm", "delete":
		if len(pos) != 1 {
			fatal(errors.New("usage: modalfs volume rm NAME"))
		}
		v := byName(pos[0])
		if !*yes {
			fmt.Printf("This permanently deletes every file in %q (%s).\nType the volume name to confirm: ", v.Name, a.Label)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != v.Name {
				fatal(errors.New("aborted"))
			}
		}
		must(c.Do("POST", "/api/accounts/"+a.ID+"/volumes/"+v.ID+"/delete", map[string]string{"name": v.Name, "confirm": v.Name, "environment": *env}, nil))
		fmt.Printf("deleted %s\n", v.Name)
	default:
		fatal(fmt.Errorf("unknown volume command %q", sub))
	}
}

func cmdAccount(args []string) {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	sub := args[0]
	fl := flag.NewFlagSet("account "+sub, flag.ExitOnError)
	label := fl.String("label", "", "display name (default: workspace name)")
	tokenID := fl.String("token-id", "", "Modal token id (ak-…)")
	env := fl.String("env", "", "default environment for this account")
	fromEnv := fl.Bool("secret-from-env", false, "take the secret from MODAL_TOKEN_SECRET at run time instead of storing it")
	pos := parseFlags(fl, args[1:])
	c := client()
	switch sub {
	case "ls", "list":
		st := state(c)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "LABEL\tWORKSPACE\tTOKEN\tSECRET\tID")
		for _, a := range st.Accounts {
			fmt.Fprintf(w, "%s\t%s\t%s…\t%s\t%s\n", a.Label, a.Workspace, a.TokenID[:min(7, len(a.TokenID))], a.SecretStore, a.ID)
		}
		_ = w.Flush()
	case "add":
		id := *tokenID
		in := bufio.NewReader(os.Stdin)
		if id == "" {
			fmt.Print("Token ID (ak-…): ")
			id, _ = in.ReadString('\n')
		}
		secret := ""
		if !*fromEnv {
			if s := os.Getenv("MODAL_TOKEN_SECRET"); s != "" && !term.IsTerminal(int(os.Stdin.Fd())) {
				secret = s
			} else {
				fmt.Print("Token secret (as-…, hidden): ")
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				if err != nil {
					line, _ := in.ReadString('\n')
					b = []byte(line)
				}
				secret = string(b)
			}
		}
		var a config.Account
		must(c.Do("POST", "/api/accounts", map[string]any{"label": *label, "tokenId": strings.TrimSpace(id), "tokenSecret": strings.TrimSpace(secret), "environment": *env, "secretFromEnv": *fromEnv}, &a))
		fmt.Printf("added %s (workspace %s, secret in %s)\n", a.Label, a.Workspace, a.SecretStore)
	case "import":
		var out struct {
			Added  []config.Account `json:"added"`
			Errors []string         `json:"errors"`
		}
		must(c.Do("POST", "/api/import/profiles", map[string]any{"names": pos}, &out))
		for _, a := range out.Added {
			fmt.Printf("imported %s (workspace %s)\n", a.Label, a.Workspace)
		}
		for _, e := range out.Errors {
			fmt.Println("skipped", e)
		}
	case "rm", "remove":
		if len(pos) != 1 {
			fatal(errors.New("usage: modalfs account rm ACCOUNT"))
		}
		a := pickAccount(state(c), pos[0])
		must(c.Do("DELETE", "/api/accounts/"+a.ID, nil, nil))
		fmt.Printf("removed %s\n", a.Label)
	case "check":
		if len(pos) != 1 {
			fatal(errors.New("usage: modalfs account check ACCOUNT"))
		}
		a := pickAccount(state(c), pos[0])
		var who struct {
			WorkspaceName string `json:"workspace_name"`
			Username      string `json:"username"`
		}
		must(c.Do("POST", "/api/accounts/"+a.ID+"/verify", nil, &who))
		fmt.Printf("%s: the token works (workspace %s)\n", a.Label, who.WorkspaceName)
	case "token":
		if len(pos) != 1 {
			fatal(errors.New("usage: modalfs account token ACCOUNT [--token-id ID]   (the secret is prompted)"))
		}
		a := pickAccount(state(c), pos[0])
		id := *tokenID
		in := bufio.NewReader(os.Stdin)
		if id == "" {
			fmt.Print("New token ID (ak-…): ")
			id, _ = in.ReadString('\n')
		}
		fmt.Print("New token secret (as-…, hidden): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			line, _ := in.ReadString('\n')
			b = []byte(line)
		}
		var out config.Account
		must(c.Do("POST", "/api/accounts/"+a.ID+"/token", map[string]any{"tokenId": strings.TrimSpace(id), "tokenSecret": strings.TrimSpace(string(b))}, &out))
		fmt.Printf("%s now uses token %s… (mounted volumes switch to it when they're mounted again)\n", out.Label, out.TokenID[:min(7, len(out.TokenID))])
	case "rename":
		if len(pos) != 2 {
			fatal(errors.New("usage: modalfs account rename ACCOUNT NEWNAME"))
		}
		a := pickAccount(state(c), pos[0])
		must(c.Do("PATCH", "/api/accounts/"+a.ID, map[string]any{"label": pos[1]}, nil))
		fmt.Printf("renamed %s to %s\n", a.Label, pos[1])
	case "env":
		if len(pos) != 2 {
			fatal(errors.New("usage: modalfs account env ACCOUNT ENVIRONMENT   (\"\" for the workspace's default)"))
		}
		a := pickAccount(state(c), pos[0])
		must(c.Do("PATCH", "/api/accounts/"+a.ID, map[string]any{"environment": pos[1]}, nil))
		fmt.Printf("%s: new mounts use environment %q\n", a.Label, pos[1])
	default:
		fatal(fmt.Errorf("unknown account command %q", sub))
	}
}

func cmdUI(args []string) {
	printOnly := len(args) > 0 && args[0] == "--print"
	// Prefer the desktop app when it's installed.
	if runtime.GOOS == "darwin" && !printOnly {
		for _, p := range []string{"/Applications/modalfs.app", filepath.Join(os.Getenv("HOME"), "Applications", "modalfs.app")} {
			if _, err := os.Stat(p); err == nil {
				must(exec.Command("open", p).Run())
				return
			}
		}
	}
	c := client()
	// A fresh one-time link: the page trades it for a session cookie, so the
	// link the browser keeps in its history is useless afterwards.
	var link struct {
		URL string `json:"url"`
	}
	must(c.Do("POST", "/api/ui/token", nil, &link))
	if printOnly {
		fmt.Println(link.URL)
		return
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if err := exec.Command(opener, link.URL).Start(); err != nil {
		fmt.Println("open this in a browser:", link.URL)
		return
	}
	fmt.Println("opened", strings.SplitN(link.URL, "#", 2)[0])
}
