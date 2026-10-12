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
	// Last known state, from the Function's records (see Status).
	State string `json:"state,omitempty"`
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
	host := strings.ToLower(url)
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	for _, site := range videoSites {
		if host == site || strings.HasSuffix(host, "."+site) {
			return "yt-dlp"
		}
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
