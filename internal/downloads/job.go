// Package downloads runs remote downloads: a Modal Function (remote/app.py)
// downloads a link into one of the user's volumes while the laptop may be
// off, and the daemon follows it through the records the Function keeps on
// the volume and, while someone watches, a live tunnel.
//
// docs/design/remote-downloads.md has the design and the measurements it
// rests on.
package downloads

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/t41372/modalfs/internal/config"
)

// Tools a download can use. gopeed is the default.
var Tools = []string{"gopeed", "aria2", "yt-dlp"}

// Size is the container a download runs in.
type Size struct {
	Name      string
	CPU       float64 // physical cores
	MemoryMiB int
}

var Sizes = map[string]Size{
	"small":  {"small", 0.125, 512},
	"medium": {"medium", 0.25, 1024},
	"large":  {"large", 1, 2048},
}

// MaxRun is how long one Function call may run (Modal's limit is 24 h).
// The agent pauses before it and continues in a new call.
const MaxRun = 24 * time.Hour

// Job is one download, as the daemon keeps it.
type Job struct {
	ID          string `json:"id"`
	AccountID   string `json:"account_id"`
	Environment string `json:"environment,omitempty"`
	VolumeID    string `json:"volume_id"` // pinned: the Function refuses any other volume
	VolumeName  string `json:"volume_name"`
	Folder      string `json:"folder"` // volume-relative destination
	URL         string `json:"url"`
	Tool        string `json:"tool"`
	Size        string `json:"size"`
	// The token the agent's live channel wants. The cookies file, for
	// yt-dlp, goes to the Function with each call and nowhere else.
	LiveToken string   `json:"live_token"`
	Cookies   string   `json:"cookies,omitempty"`
	Created   int64    `json:"created"`
	Calls     []string `json:"calls,omitempty"` // Function call IDs, oldest first
	// StallLimit stops a download that has received nothing for this many
	// seconds (the agent's default when zero).
	StallLimit int `json:"stall_limit,omitempty"`
	// WARP sends the download through Cloudflare WARP (app.py, class Warp):
	// the downloader can reach nothing else.
	WARP bool `json:"warp,omitempty"`

	// What the daemon knows of it: preparing, running, pausing,
	// cancelling, or how it ended (see Ended).
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
	// ErrorKind is what Modal had to do with a failure (see Why).
	ErrorKind string `json:"error_kind,omitempty"`
	// Started is when the last call was spawned (Unix seconds).
	Started int64 `json:"started,omitempty"`
	// Ran is how long its calls have run in all, in seconds, what it's billed
	// for (counted each time a run ends); Ended is when it last ended.
	Ran   int64 `json:"ran,omitempty"`
	Ended int64 `json:"ended,omitempty"`
	// Stopping is "pause" or "cancel" while the daemon is stopping the
	// download's call for the user, so the call's end is read as that and
	// not as a failure. Keep keeps the partial files of a cancel.
	Stopping string `json:"stopping,omitempty"`
	Keep     bool   `json:"keep,omitempty"`
	// Status is the last status.json read, kept so the list shows where a
	// download stopped without reading every volume.
	Status *Status `json:"status,omitempty"`
	// Before uses status.json only when written after this time (its own
	// clock): the one there when a run starts is the last run's.
	Before float64 `json:"status_before,omitempty"`
}

// The daemon's states for a job, besides those it ends in.
const (
	StatePreparing  = "preparing" // deploying the Function, then spawning it
	StateRunning    = "running"   // a call is running; its status.json says more
	StatePausing    = "pausing"
	StateCancelling = "cancelling"
	StateCancelled  = "cancelled"
)

// Ended reports whether a job's state is one it rests in: no call runs.
func Ended(state string) bool {
	switch state {
	case "done", "paused", "stalled", "failed", "time limit", StateCancelled:
		return true
	}
	return false
}

// StripCredentials returns a link without user:password@ and its query
// string (signed URLs, tracker passkeys), as the agent writes it in
// job.json. Magnet links keep only their info hash.
func StripCredentials(link string) string {
	if strings.HasPrefix(link, "magnet:") {
		for _, p := range strings.FieldsFunc(strings.TrimPrefix(link, "magnet:"), func(r rune) bool { return r == '?' || r == '&' }) {
			if strings.HasPrefix(p, "xt=") {
				return "magnet:?" + p
			}
		}
		return "magnet:"
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" {
		s, _, _ := strings.Cut(link, "?")
		return s
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment = nil, "", false, ""
	return u.String()
}

// NewJob checks a request and makes a job for it.
func NewJob(accountID, env, volumeID, volumeName, folder, url, tool, size string) (Job, error) {
	if !strings.Contains(url, "://") && !strings.HasPrefix(url, "magnet:") {
		return Job{}, fmt.Errorf("%q isn't a link (http://, https://, ftp:// or magnet:)", url)
	}
	if tool == "" {
		tool = DefaultTool(url)
	}
	if !contains(Tools, tool) {
		return Job{}, fmt.Errorf("unknown downloader %q (use one of %s)", tool, strings.Join(Tools, ", "))
	}
	if size == "" {
		size = "small"
	}
	if _, ok := Sizes[size]; !ok {
		return Job{}, fmt.Errorf("unknown size %q (use small, medium or large)", size)
	}
	folder = strings.Trim(path.Clean("/"+folder), "/")
	if strings.HasPrefix(folder, ".modalfs") {
		return Job{}, fmt.Errorf("downloads can't go into .modalfs, where modalfs keeps its records")
	}
	return Job{
		ID: config.NewID("dl"), AccountID: accountID, Environment: env, VolumeID: volumeID, VolumeName: volumeName,
		Folder: folder, URL: url, Tool: tool, Size: size, LiveToken: randomToken(), Created: config.Now(),
	}, nil
}

// DefaultTool picks the downloader for a link: yt-dlp for video sites,
// gopeed for everything else.
func DefaultTool(url string) string {
	if hostIs(url, videoSites...) {
		return "yt-dlp"
	}
	return "gopeed"
}

// Sites yt-dlp is the right tool for. Anything else can still be sent to
// yt-dlp by choosing it.
var videoSites = []string{
	"youtube.com", "youtu.be", "bilibili.com", "b23.tv", "vimeo.com", "twitter.com", "x.com",
	"tiktok.com", "instagram.com", "twitch.tv", "dailymotion.com", "nicovideo.jp", "reddit.com",
	"soundcloud.com", "facebook.com", "douyin.com", "youku.com", "iqiyi.com", "v.qq.com",
}

// IsBitTorrent reports whether a link is a torrent, which WARP can't carry.
func IsBitTorrent(link string) bool {
	l := strings.ToLower(link)
	l, _, _ = strings.Cut(l, "?")
	return strings.HasPrefix(l, "magnet:") || strings.HasSuffix(l, ".torrent")
}

// DefaultWARP is whether a link goes through Cloudflare WARP unless asked
// otherwise: YouTube, which often refuses cloud servers (through WARP, 10
// of 12 live runs got past its bot check; direct, 2 of 5). Other links go
// direct: WARP is slower, and nothing showed the other video sites need it.
func DefaultWARP(link string) bool {
	return !IsBitTorrent(link) && hostIs(link, "youtube.com", "youtu.be", "youtube-nocookie.com")
}

func hostIs(link string, sites ...string) bool {
	host := strings.ToLower(link)
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	host, _, _ = strings.Cut(host, ":")
	for _, site := range sites {
		if host == site || strings.HasSuffix(host, "."+site) {
			return true
		}
	}
	return false
}

// Dir is where the job's records live on the volume.
func (j Job) Dir() string { return ".modalfs/downloads/" + j.ID }

// args is what the Function gets for one call that may run for timeout.
func (j Job) args(app string, timeout time.Duration) map[string]any {
	size := Sizes[j.Size]
	a := map[string]any{
		"id": j.ID, "url": j.URL, "tool": j.Tool, "folder": j.Folder, "volume_id": j.VolumeID,
		"live_token": j.LiveToken, "app": app, "cpu": size.CPU, "memory": size.MemoryMiB,
		"timeout": int(timeout / time.Second), "update_ytdlp": true,
	}
	if j.Cookies != "" {
		a["cookies"] = j.Cookies
	}
	if j.StallLimit > 0 {
		a["stall_limit"] = j.StallLimit
	}
	if j.WARP {
		a["warp"] = true
	}
	return a
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
