package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/modalapi"
)

var loginLifetime = 10 * time.Minute

// LoginStatus is safe to send to clients. Neither the wait secret nor the
// resulting token secret leaves the daemon.
type LoginStatus struct {
	ID        string          `json:"id"`
	URL       string          `json:"url,omitempty"`
	Code      string          `json:"code,omitempty"`
	ExpiresAt int64           `json:"expires_at,omitempty"`
	State     string          `json:"state"` // waiting, connected, cancelled, error
	Account   *config.Account `json:"account,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type accountLogin struct {
	mu       sync.Mutex // serializes cancellation with saving the account
	status   LoginStatus
	cancel   context.CancelFunc
	finished time.Time
}

func (f *accountLogin) snapshot() LoginStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *accountLogin) stop() LoginStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancel()
	if f.status.State == "waiting" {
		f.status.State = "cancelled"
		f.finished = time.Now()
	}
	return f.status
}

type accountLogins struct {
	mu     sync.Mutex
	flows  map[string]*accountLogin
	closed bool
}

func (s *accountLogins) get(id string) *accountLogin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flows[id]
}

func (s *accountLogins) close() {
	s.mu.Lock()
	s.closed = true
	flows := s.flows
	s.flows = nil
	s.mu.Unlock()
	for _, f := range flows {
		f.stop()
	}
}

func (d *Daemon) startLogin(ctx context.Context, label, env, server string) (LoginStatus, error) {
	if server == "" {
		server = "https://api.modal.com"
	}
	cctx, cancelStart := context.WithTimeout(ctx, 20*time.Second)
	defer cancelStart()
	flow, err := modalapi.StartLogin(cctx, modalapi.Config{ServerURL: server, Transport: d.transport()})
	if err != nil {
		return LoginStatus{}, friendly(err)
	}
	expires := time.Now().Add(loginLifetime)
	waitCtx, cancel := context.WithDeadline(context.Background(), expires)
	f := &accountLogin{status: LoginStatus{ID: config.NewID("login"), URL: flow.URL, Code: flow.Code, ExpiresAt: expires.Unix(), State: "waiting"}, cancel: cancel}
	d.logins.mu.Lock()
	if d.logins.closed {
		d.logins.mu.Unlock()
		cancel()
		flow.Close()
		return LoginStatus{}, errors.New("modalfs is quitting; open it again to connect an account")
	}
	if d.logins.flows == nil {
		d.logins.flows = make(map[string]*accountLogin)
	}
	// Keep completed outcomes long enough for a reconnecting client to see them.
	for id, old := range d.logins.flows {
		// A busy attempt may be saving over the network. Cleanup must not
		// hold up unrelated attempts or their cancellation/status requests.
		if !old.mu.TryLock() {
			continue
		}
		if !old.finished.IsZero() && time.Since(old.finished) > loginLifetime {
			delete(d.logins.flows, id)
		}
		old.mu.Unlock()
	}
	d.logins.flows[f.status.ID] = f
	d.logins.mu.Unlock()
	initial := f.snapshot()
	go func() {
		defer flow.Close()
		defer cancel()
		tokenID, secret, err := flow.Wait(waitCtx)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.status.State != "waiting" {
			return
		}
		f.finished = time.Now()
		if err != nil {
			if waitCtx.Err() != nil {
				f.status.State, f.status.Error = "error", "Login link expired. Start browser login again."
			} else {
				f.status.State, f.status.Error = "error", "Browser login failed: "+friendly(err).Error()+". Start browser login again."
			}
			return
		}
		// Once saving begins, cancellation waits for its outcome. A caller must
		// never be told it cancelled an account that was actually saved.
		a, err := d.AddAccount(context.Background(), label, tokenID, secret, server, env, "", false)
		if err != nil {
			f.status.State, f.status.Error = "error", "Could not save the connected account: "+err.Error()
			return
		}
		f.status.State, f.status.Account = "connected", &a
	}()
	return initial, nil
}

func (d *Daemon) loginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/account-logins", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Label, Environment, ServerURL string }
		if err := readJSON(r, &in); err != nil {
			writeErr(w, err)
			return
		}
		out, err := d.startLogin(r.Context(), in.Label, in.Environment, in.ServerURL)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("GET /api/account-logins/{id}", func(w http.ResponseWriter, r *http.Request) {
		f := d.logins.get(r.PathValue("id"))
		if f == nil {
			writeJSON(w, 404, map[string]string{"error": "Login session not found. Start browser login again."})
			return
		}
		writeJSON(w, 200, f.snapshot())
	})
	mux.HandleFunc("DELETE /api/account-logins/{id}", func(w http.ResponseWriter, r *http.Request) {
		out := LoginStatus{State: "cancelled"}
		if f := d.logins.get(r.PathValue("id")); f != nil {
			out = f.stop()
		}
		writeJSON(w, 200, out)
	})
	mux.HandleFunc("POST /api/account-logins/{id}/open", func(w http.ResponseWriter, r *http.Request) {
		f := d.logins.get(r.PathValue("id"))
		if f == nil {
			writeErr(w, errors.New("login session not found"))
			return
		}
		out := f.snapshot()
		if out.State != "waiting" {
			writeErr(w, errors.New("this login is no longer waiting; start browser login again"))
			return
		}
		if err := openPath(out.URL); err != nil {
			writeErr(w, fmt.Errorf("could not open the browser (%v); copy the login link into your browser", err))
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
}
