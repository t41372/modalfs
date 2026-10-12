package daemon

import (
	"bufio"
	"os"
	"strings"
)

// mountedPaths returns the mount points in this process's mount table.
func mountedPaths() (map[string]bool, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw
		fields := strings.Fields(sc.Text())
		if len(fields) > 4 {
			out[unescapeMountinfo(fields[4])] = true
		}
	}
	return out, sc.Err()
}
