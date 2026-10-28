package webui

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

func TestDesktopFS(t *testing.T) {
	const index = "<!doctype html><html><head><title>modalfs</title></head><body></body></html>"
	base := fstest.MapFS{
		"index.html": {Data: []byte(index)},
		"app.js":     {Data: []byte("test asset")},
	}
	native, err := DesktopFS(base)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(native, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte(`name="modalfs-desktop" content="1"`)) != 1 {
		t.Fatalf("native HTML has no unique desktop marker: %s", raw)
	}
	info, err := fs.Stat(native, "index.html")
	if err != nil || info.Size() != int64(len(raw)) {
		t.Fatalf("native HTML size does not match its content: %v, %v", info, err)
	}
	if raw, _ := fs.ReadFile(base, "index.html"); string(raw) != index {
		t.Fatal("marking the native HTML changed the browser HTML")
	}
	if raw, _ := fs.ReadFile(native, "app.js"); string(raw) != "test asset" {
		t.Fatal("native asset lookup changed the JavaScript")
	}
}

func TestDesktopAPIKeepsStreamingBodies(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			body := `{"label":"測試帳號","appearance":"dark","padding":"` + strings.Repeat("x", 64<<10) + `"}`
			r := httptest.NewRequest(method, "/api/settings?test=1", io.NopCloser(strings.NewReader(body)))
			r.ContentLength = 0 // how Wails represents its streaming bodies
			r.Header.Set("X-Modalfs-Request", "1")
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil || string(raw) != body || r.Method != method {
					t.Errorf("daemon received %s with %d bytes (err %v), want %s with %d bytes", r.Method, len(raw), err, method, len(body))
				}
				if r.URL.RawQuery != "test=1" || r.Header.Get("X-Modalfs-Request") != "1" {
					t.Error("desktop transport lost the query or request header")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer backend.Close()
			target, err := url.Parse(backend.URL)
			if err != nil {
				t.Fatal(err)
			}
			h := DesktopAPI(httputil.NewSingleHostReverseProxy(target))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusNoContent {
				t.Fatalf("request was not forwarded: status=%d", w.Code)
			}
		})
	}
}

func TestDesktopAPIKeepsBodylessActions(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
	DesktopAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || len(raw) != 0 || r.ContentLength != 0 {
			t.Errorf("bodyless action changed: %q, length=%d, err=%v", raw, r.ContentLength, err)
		}
	})).ServeHTTP(httptest.NewRecorder(), r)
}
