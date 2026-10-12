package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// The browser UI on the localhost port.
//
// `modalfs ui` asks the daemon (over its Unix socket) for a fresh one-time
// token and opens /#token=…. The page trades it for an HttpOnly,
// SameSite=Strict session cookie, which uses the token up: the link left in
// the browser's history is worthless. Until then the token also works as an
// X-Modalfs-Token header, which `just dev`'s proxy uses.
//
// Other pages, including ones on other localhost ports (which count as the
// same site, so they'd get the cookie), are refused because every API call
// must carry the X-Modalfs-Request header: a page from another origin can't
// send it without a CORS preflight, which the daemon never approves. The
// desktop app talks to the Unix socket and needs none of this.

const (
	tokenHeader   = "X-Modalfs-Token"
	requestHeader = "X-Modalfs-Request"
	maxTokens     = 32 // unused one-time tokens kept; older ones expire
)

type uiAuth struct {
	mu      sync.Mutex
	tokens  []string // one-time tokens not yet traded for a cookie, oldest first
	session string   // the cookie's value, for this daemon's lifetime
	cookie  string   // the cookie's name, with the port: cookies don't tell ports apart
}

func newUIAuth(port int) *uiAuth {
	return &uiAuth{session: randomHex(24), cookie: fmt.Sprintf("modalfs_session_%d", port)}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mint returns a new one-time token.
func (a *uiAuth) mint() string {
	t := randomHex(16)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tokens = append(a.tokens, t)
	if len(a.tokens) > maxTokens {
		a.tokens = a.tokens[len(a.tokens)-maxTokens:]
	}
	return t
}

// check reports whether t is an unused one-time token; use also uses it up.
func (a *uiAuth) check(t string, use bool) bool {
	if t == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, x := range a.tokens {
		if subtle.ConstantTimeCompare([]byte(x), []byte(t)) == 1 {
			if use {
				a.tokens = append(a.tokens[:i], a.tokens[i+1:]...)
			}
			return true
		}
	}
	return false
}

func (a *uiAuth) hasSession(r *http.Request) bool {
	c, err := r.Cookie(a.cookie)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.session)) == 1
}

// tcpGuard protects the localhost port: a loopback Host header (defeats DNS
// rebinding), and for the API the session cookie or an unused token, plus
// the request header that keeps other pages out.
func (d *Daemon) tcpGuard(port int, a *uiAuth, next http.Handler) http.Handler {
	okHosts := map[string]bool{fmt.Sprintf("127.0.0.1:%d", port): true, fmt.Sprintf("localhost:%d", port): true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !okHosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(requestHeader) == "" && r.Header.Get(tokenHeader) == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/api/ui/token":
			// Minting is for the CLI on the Unix socket only.
			http.Error(w, "forbidden", http.StatusForbidden)
		case r.URL.Path == "/api/session" && r.Method == http.MethodPost:
			if !a.check(r.Header.Get(tokenHeader), true) {
				http.Error(w, "this link was already used or has expired; run `modalfs ui` again", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: a.cookie, Value: a.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			writeJSON(w, 200, map[string]bool{"ok": true})
		case a.hasSession(r) || a.check(r.Header.Get(tokenHeader), false):
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "missing or bad session", http.StatusUnauthorized)
		}
	})
}
