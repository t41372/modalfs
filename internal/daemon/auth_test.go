package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUISession(t *testing.T) {
	d := testDaemon(t)
	a := newUIAuth(4242)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := d.tcpGuard(4242, a, api)
	do := func(method, path string, hdr map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:4242"+path, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	tok := a.mint()

	// The page itself needs nothing; the API needs a session.
	if w := do("GET", "/", nil, nil); w.Code != 200 {
		t.Fatalf("GET / = %d", w.Code)
	}
	if w := do("GET", "/api/state", map[string]string{requestHeader: "1"}, nil); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}
	// Minting is for the Unix socket only.
	if w := do("POST", "/api/ui/token", map[string]string{tokenHeader: tok}, nil); w.Code != 403 {
		t.Fatalf("minting over TCP: %d", w.Code)
	}
	// Until it's traded, the token works as a header (just dev's proxy).
	if w := do("GET", "/api/state", map[string]string{tokenHeader: tok}, nil); w.Code != 200 {
		t.Fatalf("unused token as a header: %d", w.Code)
	}
	w := do("POST", "/api/session", map[string]string{tokenHeader: tok, requestHeader: "1"}, nil)
	if w.Code != 200 {
		t.Fatalf("trading the token: %d", w.Code)
	}
	var c *http.Cookie
	for _, x := range w.Result().Cookies() {
		if x.Name == a.cookie {
			c = x
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", c)
	}
	// The token is used up: the link in the history is worthless.
	if w := do("POST", "/api/session", map[string]string{tokenHeader: tok, requestHeader: "1"}, nil); w.Code != 401 {
		t.Fatalf("token traded twice: %d", w.Code)
	}
	if w := do("GET", "/api/state", map[string]string{tokenHeader: tok}, nil); w.Code != 401 {
		t.Fatalf("used token as a header: %d", w.Code)
	}
	if w := do("GET", "/api/state", map[string]string{requestHeader: "1"}, c); w.Code != 200 {
		t.Fatalf("with the cookie: %d", w.Code)
	}
	// Without the request header (a form or an <img> from another page): refused.
	if w := do("POST", "/api/shutdown", nil, c); w.Code != 403 {
		t.Fatalf("cookie without the header: %d", w.Code)
	}
	// Another Host (DNS rebinding): refused.
	r := httptest.NewRequest("GET", "http://evil.example:4242/api/state", nil)
	r.Header.Set(requestHeader, "1")
	r.AddCookie(c)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, r)
	if rw.Code != 403 {
		t.Fatalf("other Host: %d", rw.Code)
	}
}

func TestMayFixItself(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{status.Error(codes.Unavailable, "connection refused"), true},
		{fmt.Errorf("lookup: %w", status.Error(codes.DeadlineExceeded, "x")), true},
		{&net.OpError{Op: "dial", Err: errors.New("network is unreachable")}, true},
		{context.DeadlineExceeded, true},
		{status.Error(codes.Unauthenticated, "bad token"), false},
		{fmt.Errorf("Modal rejected this account's token: %w", status.Error(codes.Unauthenticated, "x")), false},
		{errors.New("/x is not empty; refusing to hide its contents"), false},
		{errors.New(`volume "a" (vo-1) no longer exists`), false},
	} {
		if got := mayFixItself(c.err); got != c.want {
			t.Errorf("mayFixItself(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
