package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var errRecoveryMount = errors.New("decrypt to a local folder outside modalfs mounts")

// Resolve one component at a time, checking before Lstat: EvalSymlinks on
// the whole destination could itself enter a mount served by this process.
func resolveRecoveryPath(p string, mounts []string) (string, error) {
	outside := func(p string) bool {
		for _, mp := range mounts {
			if p == mp || inside(p, mp) {
				return false
			}
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(p), "/"), "/")
	resolved := "/"
	links := 0
	for len(parts) > 0 {
		next := filepath.Join(resolved, parts[0])
		parts = parts[1:]
		if !outside(next) {
			return "", errRecoveryMount
		}
		info, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			final := filepath.Join(append([]string{next}, parts...)...)
			if !outside(final) {
				return "", errRecoveryMount
			}
			return final, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		links++
		if links > 40 {
			return "", errors.New("too many symlinks in recovery destination")
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(resolved, target)
		}
		target = filepath.Join(append([]string{target}, parts...)...)
		parts = strings.Split(strings.TrimPrefix(filepath.Clean(target), "/"), "/")
		resolved = "/"
	}
	return resolved, nil
}

func (d *Daemon) recoveryPath(dest string) (string, error) {
	var mounts []string
	for _, m := range d.store.Snapshot().Mounts {
		if m.Mountpoint != "" {
			mounts = append(mounts, filepath.Clean(expandHome(m.Mountpoint)))
		}
	}
	d.mu.Lock()
	for _, r := range d.running {
		if r.tablePath != "" {
			mounts = append(mounts, r.tablePath)
		}
	}
	d.mu.Unlock()
	// Canonicalize only parents, never a configured mountpoint itself.
	for _, mp := range append([]string{}, mounts...) {
		parent, err := resolveRecoveryPath(filepath.Dir(mp), mounts)
		if errors.Is(err, errRecoveryMount) {
			continue // already covered by an enclosing mount
		}
		if err != nil {
			return "", err
		}
		mounts = append(mounts, filepath.Join(parent, filepath.Base(mp)))
	}
	return resolveRecoveryPath(dest, mounts)
}
