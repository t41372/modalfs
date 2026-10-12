// Package webui embeds the modalfs GUI, built from web/ (React, Vite) into
// dist/ by `just ui`. The same files are served by the daemon on localhost and
// by the desktop app (cmd/modalfs-gui).
//
// A checkout that hasn't built the UI still compiles: dist/ holds only a
// .gitkeep, and the handler serves a page saying how to build it.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"testing/fstest"
)

//go:embed all:dist
var files embed.FS

// FS returns the built UI (index.html at its root), or a placeholder page when
// the UI wasn't built.
func FS() fs.FS {
	sub, _ := fs.Sub(files, "dist")
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return fstest.MapFS{"index.html": {Data: []byte(placeholder)}}
	}
	return sub
}

func Handler() http.Handler {
	fsrv := http.FileServer(http.FS(FS()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Vite names assets by their content, so they never change under one name.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fsrv.ServeHTTP(w, r)
	})
}

const placeholder = `<!doctype html><meta charset="utf-8"><title>modalfs</title>
<body style="font:15px/1.5 -apple-system,system-ui,sans-serif;max-width:34em;margin:15vh auto;padding:0 1.5em;color:#333">
<h1 style="font-size:20px">The modalfs UI wasn't built into this binary</h1>
<p>Everything else works: use the <code>modalfs</code> CLI. To include the UI, build from a checkout with
<a href="https://bun.sh">Bun</a> and <a href="https://github.com/casey/just">just</a> installed:</p>
<pre style="background:#f4f4f6;padding:.8em 1em;border-radius:8px">just build</pre>
<p>Release downloads include it.</p>`
