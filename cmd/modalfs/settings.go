package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
)

const settingsUsage = `usage: modalfs settings [NAME VALUE]
  cache-size GB                      all the disk modalfs uses, changes not uploaded yet included (at least 1)
  read-ahead BLOCKS                  8 MiB blocks read ahead of a file being read from start to end
  mount-root FOLDER                  where a new mount's folder goes
  transport auto|http1               how blocks travel; http1 if a proxy or firewall blocks the default
  download-size small|medium|large   the machine a remote download runs on
  download-stall MINUTES             stop a remote download that has received nothing for this long`

// cmdSettings shows the settings, or changes one, as the app's Settings
// page does.
func cmdSettings(args []string) {
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Println(settingsUsage)
		return
	}
	if len(args) != 0 && len(args) != 2 {
		fatal(errors.New(settingsUsage))
	}
	c := client()
	if len(args) == 0 {
		s := state(c).Settings.WithDefaults()
		transport := s.Transport
		if transport == "" {
			transport = "auto"
		}
		downloads := "off"
		if s.RemoteDownloads {
			downloads = "on (modalfs download disable turns them off)"
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "cache-size\t%d GB\n", s.CacheSizeGB)
		fmt.Fprintf(tw, "read-ahead\t%d blocks\n", s.ReadAhead)
		fmt.Fprintf(tw, "mount-root\t%s\n", s.MountRoot)
		fmt.Fprintf(tw, "transport\t%s\n", transport)
		fmt.Fprintf(tw, "remote downloads\t%s\n", downloads)
		fmt.Fprintf(tw, "download-size\t%s\n", s.DownloadSize)
		fmt.Fprintf(tw, "download-stall\t%d minutes\n", s.DownloadStallMinutes)
		tw.Flush()
		return
	}
	name, v := args[0], args[1]
	whole := func(what string) int {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.ToUpper(v), "GB"))
		if err != nil || n < 1 {
			fatal(fmt.Errorf("%s is a whole number, at least 1, not %q", what, v))
		}
		return n
	}
	var change map[string]any
	switch name {
	case "cache-size":
		change = map[string]any{"cache_size_gb": whole("cache-size")}
	case "read-ahead":
		change = map[string]any{"read_ahead": whole("read-ahead")}
	case "mount-root":
		root := v // the daemon expands ~
		if !strings.HasPrefix(v, "~") {
			var err error
			root, err = filepath.Abs(v)
			must(err)
		}
		change = map[string]any{"mount_root": root}
	case "transport":
		change = map[string]any{"transport": v}
	case "download-size":
		change = map[string]any{"download_size": v}
	case "download-stall":
		change = map[string]any{"download_stall_minutes": whole("download-stall")}
	default:
		fatal(fmt.Errorf("there's no setting %q\n%s", name, settingsUsage))
	}
	must(c.Do("PATCH", "/api/settings", change, nil))
	fmt.Printf("%s set to %s\n", name, v)
}
