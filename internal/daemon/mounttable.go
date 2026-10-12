package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// tablePath is how the mount table names a mountpoint: with the symlinks
// above it resolved (/tmp is /private/tmp on macOS). It never looks at the
// mountpoint itself, which may be a stalled NFS mount.
func tablePath(mp string) string {
	mp = filepath.Clean(mp)
	if dir, err := filepath.EvalSymlinks(filepath.Dir(mp)); err == nil {
		return filepath.Join(dir, filepath.Base(mp))
	}
	return mp
}

// isMountpoint reports whether something is mounted at p. ok is false when
// the mount table can't be read; mounted is then only a guess.
func isMountpoint(p string) (mounted, ok bool) {
	table, err := mountedPaths()
	if err == nil {
		return table[tablePath(p)], true
	}
	// Compare devices instead. This stats p, which can block on a dead mount.
	a, err := os.Stat(p)
	if err != nil {
		return false, false
	}
	b, err := os.Stat(filepath.Dir(p))
	if err != nil {
		return false, false
	}
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && sa.Dev != sb.Dev, false
}

// unescapeMountinfo decodes the octal escapes (\040 for a space) Linux uses
// for spaces, tabs, newlines and backslashes in /proc/self/mountinfo.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
