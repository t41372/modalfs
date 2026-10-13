package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/t41372/modalfs/internal/daemon"
	"github.com/t41372/modalfs/internal/downloads"
)

const downloadUsage = `Remote downloads: a Modal Function in your workspace downloads a link
into a volume, so your computer can sleep meanwhile. Billed to your account.

  modalfs download enable|disable           turn remote downloads on or off
  modalfs download add VOLUME[:/FOLDER] URL...
        [--downloader gopeed|aria2|yt-dlp] [--size small|medium|large] [--cookies FILE]
        [--warp | --no-warp] [--account A] [--env E]
  modalfs download ls
  modalfs download show ID [--follow]       progress, attempts and interruptions, speed
  modalfs download pause ID
  modalfs download resume ID [--warp | --no-warp]
  modalfs download cancel ID [--keep]       stop it and delete the partial files (--keep keeps them)
  modalfs download logs ID                  the downloader's log, saved when a run ends
  modalfs download rm ID                    forget it and delete its records (finished files stay)
  modalfs download spend [--account A] [--env E]
                                            what the workspace and its downloads cost this month

The downloader is yt-dlp for video sites and gopeed for everything else;
if gopeed keeps failing on a link, the download switches to aria2 by itself.

--warp sends a download through Cloudflare WARP, for sites that block
cloud servers: slower, and the downloader can reach nothing else (if WARP
can't be reached, the download fails; it never goes direct instead).
YouTube links use it unless --no-warp. Torrents can't: BitTorrent connects
to its peers directly, which shows them the container's address.
ID can be the start of an ID shown by ls.
`

func cmdDownload(args []string) {
	if len(args) == 0 {
		args = []string{"ls"}
	}
	sub := args[0]
	if sub == "-h" || sub == "--help" || sub == "help" {
		fmt.Print(downloadUsage)
		return
	}
	fl := flag.NewFlagSet("download "+sub, flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(os.Stderr, downloadUsage) }
	acctF := fl.String("account", "", "account label or id")
	env := fl.String("env", "", "Modal environment")
	tool := fl.String("downloader", "", "gopeed, aria2 or yt-dlp (default: yt-dlp for video sites, gopeed otherwise)")
	fl.StringVar(tool, "tool", "", "same as --downloader")
	size := fl.String("size", "", "small, medium or large (default: the setting, else small)")
	cookies := fl.String("cookies", "", "a cookies.txt file for yt-dlp")
	follow := fl.Bool("follow", false, "keep showing progress until it stops")
	fl.BoolVar(follow, "f", false, "same as --follow")
	keep := fl.Bool("keep", false, "keep the partial files")
	warp := fl.Bool("warp", false, "through Cloudflare WARP")
	noWARP := fl.Bool("no-warp", false, "not through Cloudflare WARP")
	pos := parseFlags(fl, args[1:])
	if *warp && *noWARP {
		fatal(errors.New("--warp and --no-warp can't go together"))
	}
	warpChoice := func(in map[string]any) {
		if *warp || *noWARP {
			in["warp"] = *warp
		}
	}
	c := client()
	switch sub {
	case "enable", "on":
		must(c.Do("PATCH", "/api/settings", map[string]any{"remote_downloads": true}, nil))
		fmt.Println("Remote downloads are on. Each download runs in a Modal Function in the volume's workspace and is billed to that account (the smallest size costs about $0.01 an hour). It stops when the download ends, pauses or stalls.")
	case "disable", "off":
		must(c.Do("PATCH", "/api/settings", map[string]any{"remote_downloads": false}, nil))
		fmt.Println("Remote downloads are off. Downloads already running carry on; pause or cancel them with `modalfs download pause|cancel`.")
	case "add":
		if len(pos) < 2 {
			fatal(errors.New("usage: modalfs download add VOLUME[:/FOLDER] URL... (see modalfs download help)"))
		}
		vol, folder, _ := strings.Cut(pos[0], ":")
		st := state(c)
		a := pickAccount(st, *acctF)
		in := map[string]any{"account": a.ID, "environment": *env, "volume": vol, "folder": folder, "urls": pos[1:], "tool": *tool, "size": *size}
		warpChoice(in)
		if *cookies != "" {
			b, err := os.ReadFile(*cookies)
			must(err)
			in["cookies"] = string(b)
		}
		var out []daemon.DownloadView
		must(c.Do("POST", "/api/downloads", in, &out))
		for _, v := range out {
			via := ""
			if v.WARP {
				via = ", through Cloudflare WARP"
			}
			fmt.Printf("%s  %s → %s:/%s (%s, %s%s)\n", v.ID, v.Name, v.VolumeName, v.Folder, v.Tool, v.Size, via)
		}
		fmt.Println("Preparing the downloader in your Modal workspace (up to a minute when modalfs is new or updated). Follow it with `modalfs download show ID -f`.")
	case "ls", "list":
		var list []daemon.DownloadView
		must(c.Do("GET", "/api/downloads", nil, &list))
		if len(list) == 0 {
			fmt.Println("no remote downloads")
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATE\tPROGRESS\tSPEED\tDESTINATION")
		for _, v := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s:/%s\n", v.ID, clip(v.Name, 40), v.State, dlProgress(v.Status), speed(v), v.VolumeName, v.Folder)
		}
		_ = w.Flush()
	case "show":
		id := downloadID(c, pos)
		if *follow {
			followDownload(c, id)
			return
		}
		showDownload(c, id)
	case "pause", "resume", "cancel":
		id := downloadID(c, pos)
		var v daemon.DownloadView
		in := map[string]any{"keep": *keep}
		if sub == "resume" {
			warpChoice(in)
		}
		must(c.Do("POST", "/api/downloads/"+id+"/"+sub, in, &v))
		switch sub {
		case "pause":
			fmt.Printf("pausing %s; its state is saved, and `modalfs download resume %s` continues it\n", v.Name, v.ID)
		case "resume":
			if v.WARP {
				fmt.Printf("resuming %s, through Cloudflare WARP\n", v.Name)
			} else {
				fmt.Printf("resuming %s\n", v.Name)
			}
		case "cancel":
			if v.State == downloads.StateCancelled {
				fmt.Printf("cancelled %s\n", v.Name)
			} else {
				fmt.Printf("cancelling %s\n", v.Name)
			}
		}
	case "logs", "log":
		id := downloadID(c, pos)
		resp, err := c.Raw("GET", "/api/downloads/"+id+"/log")
		must(err)
		_, _ = os.Stdout.Write(resp)
	case "spend", "cost":
		st := state(c)
		a := pickAccount(st, *acctF)
		var b daemon.Billing
		must(c.Do("GET", "/api/accounts/"+a.ID+"/billing?fresh=1&env="+url.QueryEscape(*env), nil, &b))
		printSpend(a.Label, b)
	case "rm", "forget":
		id := downloadID(c, pos)
		must(c.Do("DELETE", "/api/downloads/"+id, nil, nil))
		fmt.Printf("forgot %s and deleted its records (downloaded files stay where they are)\n", id)
	default:
		fmt.Fprint(os.Stderr, downloadUsage)
		os.Exit(2)
	}
}

// downloadID finds the download an ID or the start of one names.
func downloadID(c *daemon.Client, pos []string) string {
	if len(pos) != 1 {
		fatal(errors.New("give one download ID (see modalfs download ls)"))
	}
	var list []daemon.DownloadView
	must(c.Do("GET", "/api/downloads", nil, &list))
	var hits []string
	for _, v := range list {
		if v.ID == pos[0] {
			return v.ID
		}
		if strings.HasPrefix(v.ID, pos[0]) || strings.HasPrefix(strings.TrimPrefix(v.ID, "dl-"), pos[0]) {
			hits = append(hits, v.ID)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0]
	case 0:
		fatal(fmt.Errorf("no download %q (see modalfs download ls)", pos[0]))
	}
	fatal(fmt.Errorf("%q matches %s; give more of the ID", pos[0], strings.Join(hits, ", ")))
	return ""
}

func showDownload(c *daemon.Client, id string) {
	var d daemon.DownloadDetail
	must(c.Do("GET", "/api/downloads/"+url.PathEscape(id), nil, &d))
	v := d.Download
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "%s\t%s\n", k, v)
		}
	}
	row("ID", v.ID)
	row("Name", v.Name)
	row("Link", v.URL)
	row("Into", fmt.Sprintf("%s:/%s (%s)", v.VolumeName, v.Folder, v.AccountLabel))
	row("State", v.State)
	row("Error", v.Error)
	row("Why", whyText(v.ErrorKind))
	if v.Silent >= silentWarn {
		row("Note", silentText(v))
	}
	row("Downloader", fmt.Sprintf("%s, %s", v.Tool, v.Size))
	if v.WARP {
		via := "on"
		if s := v.Status; s != nil && s.WARP != nil && s.WARP.Colo != "" {
			via = "on, leaving Cloudflare in " + s.WARP.Colo
		}
		row("Cloudflare WARP", via)
	} else if downloads.IsBitTorrent(v.URL) {
		row("Network", "direct: BitTorrent shows its peers the container's address")
	}
	if s := v.Status; s != nil {
		row("Progress", dlProgress(s))
		if s.Speed > 0 {
			row("Speed", speed(v))
		}
		if s.ETA != nil && !downloads.Ended(v.State) {
			row("Time left", (time.Duration(*s.ETA) * time.Second).String())
		}
		if s.Attempt > 1 {
			row("Attempt", fmt.Sprint(s.Attempt))
		}
		row("Note", s.Note)
		for _, f := range s.Files {
			row("File", fmt.Sprintf("%s (%s, sha256 %s)", f.Path, human(f.Size), f.SHA256))
		}
	}
	_ = w.Flush()
	if len(d.History) > 1 {
		fmt.Printf("\nSpeed, %s to %s (peak %s/s):\n  %s\n", clock(d.History[0][0]), clock(d.History[len(d.History)-1][0]), human(int64(peak(d.History))), sparkline(d.History, 64))
	}
	if len(d.Events) > 0 {
		fmt.Println("\nWhat happened:")
		for _, e := range d.Events {
			fmt.Printf("  %s  %s\n", clock(e.T), eventLine(e))
		}
	}
	if s := v.Status; s != nil && len(s.LogTail) > 0 {
		fmt.Println("\nLast log lines:")
		for _, l := range s.LogTail {
			fmt.Println("  " + l)
		}
	}
	if d.RecordsError != "" {
		fmt.Println("\nCouldn't read all its records:", d.RecordsError)
	}
}

// followDownload prints a progress line from the live channel until the
// download stops.
func followDownload(c *daemon.Client, id string) {
	var after float64
	for {
		var lv daemon.LiveView
		must(c.Do("GET", fmt.Sprintf("/api/downloads/%s/live?after=%v", url.PathEscape(id), after), nil, &lv))
		if n := len(lv.Samples); n > 0 {
			after = lv.Samples[n-1][0]
		}
		var v daemon.DownloadView
		var list []daemon.DownloadView
		must(c.Do("GET", "/api/downloads", nil, &list))
		for _, x := range list {
			if x.ID == id {
				v = x
			}
		}
		if lv.Status != nil {
			v.Status = lv.Status
		}
		line := fmt.Sprintf("%s  %s  %s", v.State, dlProgress(v.Status), speed(v))
		if !lv.Live && !downloads.Ended(v.State) {
			line += "  (updates every few seconds)"
		}
		fmt.Printf("\r\033[K%s", line)
		if downloads.Ended(v.State) {
			fmt.Println()
			if v.Error != "" {
				fmt.Println(v.Error)
			}
			if w := whyText(v.ErrorKind); w != "" {
				fmt.Println(w)
			}
			return
		}
		time.Sleep(time.Second)
	}
}

func dlProgress(s *downloads.Status) string {
	if s == nil {
		return "-"
	}
	if s.Total > 0 {
		return fmt.Sprintf("%s of %s (%.0f%%)", human(s.Done), human(s.Total), 100*float64(s.Done)/float64(s.Total))
	}
	return human(s.Done)
}

func speed(v daemon.DownloadView) string {
	if v.Status == nil || downloads.Ended(v.State) || v.Status.Speed <= 0 {
		return "-"
	}
	return human(v.Status.Speed) + "/s"
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func clock(t float64) string {
	return time.Unix(int64(t), 0).Format("01-02 15:04:05")
}

func peak(h []downloads.Sample) float64 {
	p := 0.0
	for _, s := range h {
		p = math.Max(p, s[2])
	}
	return p
}

// sparkline draws the speed over time in at most n columns, each the
// average of its stretch of time; a stretch with no samples (an
// interruption) is a space.
func sparkline(h []downloads.Sample, n int) string {
	n = min(n, len(h)) // samples are about a second apart: no false gaps
	t0, t1 := h[0][0], h[len(h)-1][0]
	if t1 <= t0 {
		return ""
	}
	sum, cnt := make([]float64, n), make([]int, n)
	for _, s := range h {
		i := min(n-1, int(float64(n)*(s[0]-t0)/(t1-t0)))
		sum[i] += s[2]
		cnt[i]++
	}
	p := peak(h)
	bars := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for i := range n {
		if cnt[i] == 0 {
			b.WriteRune(' ')
			continue
		}
		k := 0
		if p > 0 {
			k = min(len(bars)-1, int(float64(len(bars))*sum[i]/float64(cnt[i])/p))
		}
		b.WriteRune(bars[k])
	}
	return b.String()
}

// eventLine describes one line of a download's events.jsonl.
func eventLine(e downloads.Event) string {
	f := e.Fields
	num := func(k string) int64 { x, _ := f[k].(float64); return int64(x) }
	str := func(k string) string { x, _ := f[k].(string); return x }
	switch e.Kind {
	case "start":
		return fmt.Sprintf("started (attempt %d)", num("attempt"))
	case "interrupted":
		return fmt.Sprintf("the container was lost without warning; %s had been saved", human(num("done")))
	case "signal":
		return fmt.Sprintf("interrupted by Modal (preempted or stopped); saved at %s", human(num("done")))
	case "resumed":
		return fmt.Sprintf("continued on a new machine %ds later, from %s", num("gap"), human(num("done")))
	case "paused":
		return fmt.Sprintf("paused at %s", human(num("done")))
	case "error":
		return fmt.Sprintf("%s error: %s", str("tool"), str("message"))
	case "fallback":
		return fmt.Sprintf("switched to %s (it starts over): %s", str("tool"), str("reason"))
	case "stalled":
		if str("action") == "stop" {
			return fmt.Sprintf("no data for %ds; stopped", num("seconds"))
		}
		return fmt.Sprintf("no data for %ds; reconnecting", num("seconds"))
	case "continued":
		return "reached the Function's time limit; continued in a new one"
	case "warp":
		return fmt.Sprintf("connected through Cloudflare WARP (%s)", str("colo"))
	case "failed":
		return "failed: " + str("message")
	case "finished":
		return "finished"
	}
	return e.Kind
}

// silentWarn is how long a download may go unheard from before show says
// so: Modal moving a container takes about a minute.
const silentWarn = 180

func silentText(v daemon.DownloadView) string {
	return fmt.Sprintf("no word from its container for %s. Modal may still be starting or moving it; if it doesn't come back, the workspace may be over its spend limit or out of credit (see `modalfs download spend`)", (time.Duration(v.Silent) * time.Second).String())
}

// whyText explains what Modal had to do with a failure.
func whyText(kind string) string {
	switch kind {
	case downloads.WhyBilling:
		return "Modal says the workspace is out of credit or over its spend limit. Raise the limit or add credit on Modal's Usage & Billing page (`modalfs download spend` shows the link), then resume it."
	case downloads.WhyWARP:
		return "It was to go through Cloudflare WARP, and WARP couldn't be used, so nothing was downloaded directly instead. Resume it to try WARP again, or `modalfs download resume ID --no-warp` to download without it."
	case downloads.WhyUnexplained:
		return "Modal stopped it without saying why. One cause is a workspace over its spend limit or out of credit: check Modal's Usage & Billing page (`modalfs download spend` shows the link), then resume it."
	}
	return ""
}

func printSpend(account string, b daemon.Billing) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	month := time.Unix(b.PeriodStart, 0).UTC().Format("January 2006")
	fmt.Fprintf(w, "Account\t%s\n", account)
	fmt.Fprintf(w, "Used in %s\t$%.2f (before credits and free allowances)\n", month, b.Metered)
	fmt.Fprintf(w, "To be charged\t$%.2f (after them)\n", b.Billed)
	switch {
	case b.Downloads != nil:
		fmt.Fprintf(w, "Remote downloads\t$%.4f this month (Modal's report lags by an hour or so)\n", *b.Downloads)
	case b.DownloadsError != "":
		fmt.Fprintf(w, "Remote downloads\tunknown: %s\n", b.DownloadsError)
	}
	if h := b.Hourly; h != nil {
		fmt.Fprintf(w, "A download costs\t$%.4f an hour (small), $%.4f (medium), $%.4f (large) while it runs\n", h["small"], h["medium"], h["large"])
	}
	_ = w.Flush()
	fmt.Printf("\nModal's API doesn't show the credit left or the spend limit. They're on %s\n", b.UsageURL)
}
