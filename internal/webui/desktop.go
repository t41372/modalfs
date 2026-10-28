package webui

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"testing/fstest"
)

// DesktopFS marks only the native app's HTML. A browser WebView can expose
// the same message handlers as Wails, so their presence doesn't identify us.
func DesktopFS(base fs.FS) (fs.FS, error) {
	index, err := fs.ReadFile(base, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read desktop UI: %w", err)
	}
	if !bytes.Contains(index, []byte("<head>")) {
		return nil, fmt.Errorf("desktop UI index has no head element")
	}
	index = bytes.Replace(index, []byte("<head>"), []byte("<head>\n<meta name=\"modalfs-desktop\" content=\"1\">"), 1)
	return desktopFS{FS: base, index: fstest.MapFS{"index.html": {Data: index}}}, nil
}

type desktopFS struct {
	fs.FS
	index fs.FS
}

func (f desktopFS) Open(name string) (fs.File, error) {
	if name == "index.html" {
		return f.index.Open(name)
	}
	return f.FS.Open(name)
}

// DesktopAPI preserves Wails' streaming request bodies. Its scheme handler
// reports an unknown length as zero; ReverseProxy treats zero as no body.
func DesktopAPI(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength == 0 {
			r = r.Clone(r.Context())
			r.ContentLength = -1 // net/http's representation of an unknown length
			r.Header.Del("Content-Length")
		}
		api.ServeHTTP(w, r)
	})
}
