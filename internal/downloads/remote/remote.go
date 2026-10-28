// Package remote holds the Modal app that remote downloads run in: app.py
// defines the Function and the agent inside it (see its docstring), and
// warp/ the Cloudflare WARP proxy the Function's image builds from source.
package remote

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sort"
	"strings"
)

//go:embed app.py
var App []byte

//go:embed build-go-tools.sh
var buildGoTools []byte

// The WARP proxy's source. Its go.mod and go.sum are kept as .txt files: a
// real go.mod would make warp/ a module of its own, which can't be embedded
// here, and its dependencies stay out of modalfs's own go.mod.
//
//go:embed warp/main.go warp/go.mod.txt warp/go.sum.txt
var warpFS embed.FS

// Files are what a deploy copies next to app.py: paths relative to it, as
// app.py's image expects them (warp/go.mod, not warp/go.mod.txt).
func Files() map[string][]byte {
	out := map[string][]byte{"build-go-tools.sh": buildGoTools}
	_ = fs.WalkDir(warpFS, "warp", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := warpFS.ReadFile(p)
		if err != nil {
			return err
		}
		out[strings.TrimSuffix(p, ".txt")] = b
		return nil
	})
	return out
}

// Version identifies this app.py and the files it deploys with. A deployed
// app carries it as its "modalfs-agent" tag, so modalfs knows when to
// deploy again.
func Version() string {
	h := sha256.New()
	h.Write(App)
	files := Files()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h.Write([]byte("\x00" + name + "\x00"))
		h.Write(files[name])
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}
