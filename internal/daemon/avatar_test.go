package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAvatarDownload(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG fake"))
		case "/big.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(make([]byte, avatarMaxSize+10))
		case "/page":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>"))
		case "/x.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<svg/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := newAvatarStore(t.TempDir())
	s.http = srv.Client()
	ctx := context.Background()

	typ, data, err := s.download(ctx, srv.URL+"/ok.png")
	if err != nil || typ != "image/png" || !strings.HasPrefix(string(data), "\x89PNG") {
		t.Fatalf("ok.png: %q %q %v", typ, data, err)
	}
	for _, p := range []string{"/big.png", "/page", "/x.svg", "/missing"} {
		if _, _, err := s.download(ctx, srv.URL+p); err == nil {
			t.Errorf("%s: want an error", p)
		}
	}
	if _, _, err := s.download(ctx, "http://example.com/a.png"); err == nil {
		t.Error("plain http: want an error")
	}
}

func TestAvatarStoreKeepsPictures(t *testing.T) {
	dir := t.TempDir()
	s := newAvatarStore(dir)
	s.byAcct["a1"] = avatar{URL: "https://x/a.png", Type: "image/png", Data: []byte("img"), Fetched: 1}
	s.saveLocked()
	if got := newAvatarStore(dir).byAcct["a1"]; string(got.Data) != "img" || got.Type != "image/png" {
		t.Fatalf("reloaded %+v", got)
	}
	s.forget("a1")
	if _, ok := newAvatarStore(dir).byAcct["a1"]; ok {
		t.Fatal("forgotten picture came back")
	}
}
