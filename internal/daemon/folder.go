package daemon

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/t41372/modalfs/internal/config"
)

// A mount in the mount root is named after the volume's label, so Finder
// shows the name the user gave it, and follows the label when it changes:
// the folder is chosen again each time the volume is mounted, never while
// it is. A mount the user put somewhere else keeps its folder.

// folderName makes a label (or a volume name) a folder name that works on
// macOS and Linux: no slash, colon or control character, not hidden, not
// "." or "..", at most 255 bytes. "" if nothing usable is left.
func folderName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == '\\' || unicode.IsControl(r) {
			return '-'
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "."))
	for len(s) > 255 {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return strings.TrimSpace(s)
}

// followsLabel reports whether a mount's folder is named by modalfs: it was
// left to the default, or it's the old default, the volume's name in the
// mount root.
func followsLabel(m config.Mount, root string) bool {
	return m.FollowLabel || filepath.Clean(expandHome(m.Mountpoint)) == filepath.Join(root, m.VolumeName)
}

// labelFolder is where a mount that follows its label goes now: the label
// (or the volume's name) in the mount root, with " 2", " 3"… when another
// mount has that folder or a folder of that name already holds files. It
// returns the mount's current folder when its label isn't known yet, so a
// label still being read doesn't move it back and forth.
func (d *Daemon) labelFolder(m config.Mount, volID string, f *config.File) string {
	root := expandHome(f.Settings.WithDefaults().MountRoot)
	cur := filepath.Clean(expandHome(m.Mountpoint))
	label, known := d.labels.lookup(volID)
	if !known {
		return cur
	}
	name := folderName(label)
	if name == "" {
		name = folderName(m.VolumeName)
	}
	if name == "" {
		return cur
	}
	for n := 1; ; n++ {
		p := filepath.Join(root, name)
		if n > 1 {
			p = filepath.Join(root, name+" "+strconv.Itoa(n))
		}
		if p == cur {
			return p
		}
		if _, err := checkMountpoint(p, f, m.ID, false); err != nil {
			continue
		}
		if ents, err := os.ReadDir(p); err == nil && !onlyLitter(ents) {
			continue
		}
		return p
	}
}

func onlyLitter(ents []os.DirEntry) bool {
	for _, e := range ents {
		if e.Name() != ".DS_Store" && e.Name() != ".localized" {
			return false
		}
	}
	return true
}

// followLabel moves a mount that follows its label to the folder its label
// names, before it's mounted, and removes the old folder if it's empty.
func (d *Daemon) followLabel(m config.Mount, volID string) config.Mount {
	var moved string
	_ = d.store.Update(func(f *config.File) error {
		root := expandHome(f.Settings.WithDefaults().MountRoot)
		for i := range f.Mounts {
			x := &f.Mounts[i]
			if x.ID != m.ID || !followsLabel(*x, root) {
				continue
			}
			x.FollowLabel = true
			if p := d.labelFolder(*x, volID, f); p != filepath.Clean(expandHome(x.Mountpoint)) {
				moved, x.Mountpoint = expandHome(x.Mountpoint), p
			}
			m = *x
		}
		return nil
	})
	if moved != "" {
		if ents, err := os.ReadDir(moved); err == nil && onlyLitter(ents) {
			for _, e := range ents {
				_ = os.Remove(filepath.Join(moved, e.Name()))
			}
			_ = os.Remove(moved)
		}
	}
	return m
}
