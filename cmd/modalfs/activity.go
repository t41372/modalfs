package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/t41372/modalfs/internal/daemon"
)

// cmdActivity prints what the app's Activity page shows (mounts, uploads,
// conflicts, errors, recoveries), oldest first like a log. The daemon keeps
// the last 500 entries since it started. Like status, it never starts one.
func cmdActivity(args []string) {
	fl := flag.NewFlagSet("activity", flag.ExitOnError)
	problems := fl.Bool("problems", false, "only conflicts and errors")
	follow := fl.Bool("follow", false, "keep printing new entries until Ctrl-C")
	fl.BoolVar(follow, "f", false, "same as --follow")
	if pos := parseFlags(fl, args); len(pos) != 0 {
		fatal(errors.New("usage: modalfs activity [--problems] [--follow]"))
	}
	c := daemon.NewClient()
	if !c.Alive() {
		fmt.Println("modalfs is not running (start it with `modalfs ui` or `modalfs mount`)")
		return
	}
	var after int64
	boot := ""
	first := true
	printed, dropped := false, false
	for {
		st := state(c)
		if !first && st.BootID != boot {
			// A new daemon numbers its entries from 1 again.
			fmt.Fprintln(os.Stderr, "-- modalfs restarted --")
			after = 0
		}
		boot = st.BootID
		if first || st.LastSeq > after {
			var evs []daemon.Event
			must(c.Do("GET", "/api/events?after="+strconv.FormatInt(after, 10), nil, &evs))
			if first && len(evs) > 0 && evs[0].Seq > 1 {
				dropped = true
			}
			for _, e := range evs {
				after = e.Seq
				if *problems && e.Kind != "conflict" && e.Kind != "error" {
					continue
				}
				fmt.Println(activityLine(e))
				printed = true
			}
		}
		if first && !printed {
			switch {
			case *problems && dropped:
				fmt.Fprintln(os.Stderr, "no conflicts or errors in the last 500 entries; older ones aren't kept")
			case *problems:
				fmt.Fprintln(os.Stderr, "no conflicts or errors since the background process started")
			default:
				fmt.Fprintln(os.Stderr, "nothing yet since the background process started")
			}
		}
		first = false
		if !*follow {
			return
		}
		time.Sleep(time.Second)
	}
}

func activityLine(e daemon.Event) string {
	s := fmt.Sprintf("%s  %-9s  ", time.Unix(e.At, 0).Format("2006-01-02 15:04:05"), e.Kind)
	if e.Volume != "" {
		s += e.Volume + ": "
	}
	return s + e.Message
}
