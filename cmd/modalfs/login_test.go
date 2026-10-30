package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/daemon"
)

func TestAccountLoginFinalStatusNeedsNoCleanup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		result    daemon.LoginStatus
		getError  bool
		openError bool
		wantError string
	}{
		{name: "connected", result: daemon.LoginStatus{State: "connected", Account: &config.Account{Label: "work", Workspace: "team", SecretStore: "file"}}},
		{name: "error", result: daemon.LoginStatus{State: "error", Error: "Modal connection failed"}, wantError: "Modal connection failed"},
		{name: "cancelled", result: daemon.LoginStatus{State: "cancelled"}, wantError: "browser login was cancelled; run modalfs account login to try again"},
		{name: "status request failed", result: daemon.LoginStatus{Error: "Login session not found"}, getError: true, wantError: "Login session not found"},
		{name: "browser launch failed", result: daemon.LoginStatus{State: "connected", Account: &config.Account{Label: "work"}}, openError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			serveCLI(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST /api/account-logins":
					var in map[string]string
					if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
						t.Error(err)
					}
					if in["label"] != "work" || in["environment"] != "staging" {
						t.Errorf("start parameters: %v", in)
					}
					_ = json.NewEncoder(w).Encode(daemon.LoginStatus{ID: "login-test", URL: "https://modal.com/token-flow/test", State: "waiting"})
				case "POST /api/account-logins/login-test/open":
					http.Error(w, `{"error":"could not open browser"}`, http.StatusBadRequest)
				case "GET /api/account-logins/login-test":
					if tc.getError {
						w.WriteHeader(http.StatusNotFound)
					}
					_ = json.NewEncoder(w).Encode(tc.result)
				case "DELETE /api/account-logins/login-test":
					deletes.Add(1)
					http.Error(w, `{"error":"daemon went away during cleanup"}`, http.StatusServiceUnavailable)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := cmdAccountLogin(daemon.NewClient(), "work", "staging", !tc.openError)
			if tc.wantError == "" {
				if err != nil {
					t.Errorf("completed login failed: %v", err)
				}
			} else if err == nil || err.Error() != tc.wantError {
				t.Errorf("got %v, want %q", err, tc.wantError)
			}
			if got := deletes.Load(); got != 0 {
				t.Errorf("sent %d DELETE requests after the final status or a failed GET", got)
			}
		})
	}
}

func TestAccountLoginInterruptUsesCancelResult(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      daemon.LoginStatus
		deleteError bool
		wantError   string
	}{
		{name: "cancelled", result: daemon.LoginStatus{State: "cancelled"}, wantError: "browser login was cancelled; run modalfs account login to try again"},
		{name: "save failed", result: daemon.LoginStatus{State: "error", Error: "Could not save the connected account: test token rejected"}, wantError: "Could not save the connected account: test token rejected"},
		{name: "save completed", result: daemon.LoginStatus{State: "connected", Account: &config.Account{Label: "work", Workspace: "team", SecretStore: "file"}}},
		{name: "cancel request failed", deleteError: true, wantError: "daemon could not cancel login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			serveCLI(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "POST /api/account-logins":
					_ = json.NewEncoder(w).Encode(daemon.LoginStatus{ID: "login-test", URL: "https://modal.com/token-flow/test", State: "waiting"})
				case "POST /api/account-logins/login-test/open":
					// The CLI installs NotifyContext after POST and before opening.
					if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
						t.Error(err)
					}
					_, _ = w.Write([]byte(`{"ok":true}`))
				case "GET /api/account-logins/login-test":
					_ = json.NewEncoder(w).Encode(daemon.LoginStatus{State: "waiting"})
				case "DELETE /api/account-logins/login-test":
					deletes.Add(1)
					if tc.deleteError {
						http.Error(w, `{"error":"daemon could not cancel login"}`, http.StatusInternalServerError)
						return
					}
					_ = json.NewEncoder(w).Encode(tc.result)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			err := cmdAccountLogin(daemon.NewClient(), "", "", false)
			if tc.wantError == "" {
				if err != nil {
					t.Errorf("completed save reported as cancellation: %v", err)
				}
			} else if err == nil || err.Error() != tc.wantError {
				t.Errorf("got %v, want %q", err, tc.wantError)
			}
			if got := deletes.Load(); got != 1 {
				t.Errorf("sent %d DELETE requests, want one", got)
			}
		})
	}
}

func TestAccountLoginInterruptDuringCreate(t *testing.T) {
	if os.Getenv("MODALFS_TEST_LOGIN_CHILD") == "1" {
		if err := cmdAccountLogin(daemon.NewClient(), "", "", false); err != nil {
			t.Fatal(err)
		}
		return
	}
	creating, disconnected := make(chan struct{}), make(chan struct{})
	serveCLI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/account-logins" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		// Read the body so net/http can watch for the client's disconnect.
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		close(creating)
		<-r.Context().Done()
		close(disconnected)
	})
	cmd := exec.Command(os.Args[0], "-test.run=^TestAccountLoginInterruptDuringCreate$")
	cmd.Env = append(os.Environ(), "MODALFS_TEST_LOGIN_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	select {
	case <-creating:
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not request a login")
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("CLI did not exit on Ctrl-C: %v", err)
		}
		if status := exit.Sys().(syscall.WaitStatus); !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Fatalf("CLI did not keep the default SIGINT behavior: %v", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not end the CLI during the initial POST")
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("CLI exit did not cancel the POST request")
	}
}
