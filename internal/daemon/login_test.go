package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This server exercises the real anonymous gRPC transport and credential store,
// without a browser, Modal account, mount, or compute.
type loginModal struct {
	pb.UnimplementedModalClientServer
	approve      chan struct{}
	waiting      chan struct{}
	validate     chan struct{}
	release      chan struct{}
	once         sync.Once
	validateOnce sync.Once
	port         atomic.Int32
	fail         bool
	reject       bool
	polls        atomic.Int32
}

func (s *loginModal) TokenFlowCreate(ctx context.Context, r *pb.TokenFlowCreateRequest) (*pb.TokenFlowCreateResponse, error) {
	if r.GetUtmSource() != "modalfs" {
		return nil, status.Error(codes.InvalidArgument, "missing source")
	}
	s.port.Store(r.GetLocalhostPort())
	return pb.TokenFlowCreateResponse_builder{TokenFlowId: "flow-test", WebUrl: "https://modal.com/token-flow/test", Code: "TEST-CODE", WaitSecret: "private-wait-secret"}.Build(), nil
}

func (s *loginModal) TokenFlowWait(ctx context.Context, r *pb.TokenFlowWaitRequest) (*pb.TokenFlowWaitResponse, error) {
	if r.GetTokenFlowId() != "flow-test" || r.GetWaitSecret() != "private-wait-secret" {
		return nil, status.Error(codes.PermissionDenied, "wrong flow credentials")
	}
	s.once.Do(func() { close(s.waiting) })
	if s.fail {
		return nil, status.Error(codes.Unavailable, "test network failure")
	}
	if s.polls.Add(1) == 1 {
		return pb.TokenFlowWaitResponse_builder{Timeout: true}.Build(), nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.approve:
		return pb.TokenFlowWaitResponse_builder{TokenId: "ak-browser", TokenSecret: "as-private-browser", WorkspaceUsername: "browser-workspace"}.Build(), nil
	}
}

func (s *loginModal) TokenInfoGet(ctx context.Context, r *pb.TokenInfoGetRequest) (*pb.TokenInfoGetResponse, error) {
	if s.validate != nil {
		s.validateOnce.Do(func() { close(s.validate) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.reject {
		return nil, status.Error(codes.PermissionDenied, "test token rejected")
	}
	return pb.TokenInfoGetResponse_builder{TokenId: "ak-browser", WorkspaceId: "ac-browser", WorkspaceName: "browser-workspace"}.Build(), nil
}

func loginFixture(t *testing.T, s *loginModal) (*Daemon, string) {
	t.Helper()
	t.Setenv("MODALFS_HOME", t.TempDir())
	t.Setenv("MODALFS_NO_KEYCHAIN", "1")
	s.approve, s.waiting = make(chan struct{}), make(chan struct{})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	pb.RegisterModalClientServer(srv, s)
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
	d := testDaemon(t)
	d.store, err = config.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.logins.close)
	return d, "http://" + l.Addr().String()
}

func awaitLogin(t *testing.T, f *accountLogin) LoginStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if out := f.snapshot(); out.State != "waiting" {
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("login did not finish")
	return LoginStatus{}
}

func TestBrowserLoginSavesCredentialsWithoutExposingSecrets(t *testing.T) {
	s := &loginModal{}
	d, server := loginFixture(t, s)
	mux := http.NewServeMux()
	d.loginRoutes(mux)
	api := httptest.NewServer(mux)
	defer api.Close()
	resp, err := http.Post(api.URL+"/api/account-logins", "application/json", strings.NewReader(`{"label":"Browser account","environment":"staging","serverURL":"`+server+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var initial LoginStatus
	if err := json.NewDecoder(resp.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	if initial.State != "waiting" || initial.Code != "TEST-CODE" {
		t.Fatalf("start: %+v", initial)
	}
	select {
	case <-s.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("not waiting")
	}
	validation, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", s.port.Load()))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(validation.Body)
	validation.Body.Close()
	if string(body) != "flow-test" {
		t.Fatalf("localhost exposed %q", body)
	}
	close(s.approve)
	out := awaitLogin(t, d.logins.get(initial.ID))
	if out.State != "connected" || out.Account == nil {
		t.Fatalf("finish: %+v", out)
	}
	if out.Account.Label != "Browser account" || out.Account.Environment != "staging" {
		t.Fatalf("account: %+v", out.Account)
	}
	secret, err := config.LoadSecret(*out.Account)
	if err != nil || secret != "as-private-browser" {
		t.Fatalf("secret was not saved: %v", err)
	}
	for _, method := range []string{"GET", "DELETE", "DELETE"} {
		req, _ := http.NewRequest(method, api.URL+"/api/account-logins/"+initial.ID, nil)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(raw), "as-private") || strings.Contains(string(raw), "private-wait") {
			t.Fatal("API exposed a secret")
		}
		if !strings.Contains(string(raw), `"state":"connected"`) {
			t.Fatalf("lost saved outcome: %s", raw)
		}
	}
	if len(d.store.Snapshot().Accounts) != 1 {
		t.Fatal("account saved more than once")
	}
}

func TestBrowserLoginCancelAndFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "network failure"}[fail], func(t *testing.T) {
			s := &loginModal{fail: fail}
			d, server := loginFixture(t, s)
			initial, err := d.startLogin(context.Background(), "", "", server)
			if err != nil {
				t.Fatal(err)
			}
			f := d.logins.get(initial.ID)
			if fail {
				out := awaitLogin(t, f)
				if out.State != "error" || !strings.Contains(out.Error, "test network failure") {
					t.Fatalf("failure hidden: %+v", out)
				}
			} else {
				if out := f.stop(); out.State != "cancelled" {
					t.Fatalf("cancel: %+v", out)
				}
				close(s.approve)
			}
			if len(d.store.Snapshot().Accounts) != 0 {
				t.Fatal("cancelled or failed login saved an account")
			}
		})
	}
}

func TestBrowserLoginCancelReportsAccountAlreadyBeingSaved(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "rejected"}[reject], func(t *testing.T) {
			testCancelDuringLoginSave(t, reject)
		})
	}
}

func testCancelDuringLoginSave(t *testing.T, reject bool) {
	s := &loginModal{validate: make(chan struct{}), release: make(chan struct{}), reject: reject}
	d, server := loginFixture(t, s)
	initial, err := d.startLogin(context.Background(), "", "", server)
	if err != nil {
		t.Fatal(err)
	}
	f := d.logins.get(initial.ID)
	close(s.approve)
	select {
	case <-s.validate:
	case <-time.After(5 * time.Second):
		t.Fatal("not validating")
	}
	result := make(chan LoginStatus, 1)
	go func() { result <- f.stop() }()
	close(s.release)
	select {
	case out := <-result:
		if reject {
			if out.State != "error" || !strings.Contains(out.Error, "test token rejected") {
				t.Fatalf("lost save error: %+v", out)
			}
		} else if out.State != "connected" || out.Account == nil {
			t.Fatalf("reported cancellation after saving: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation stuck")
	}
}

func TestBrowserLoginSavingDoesNotBlockOtherAttempts(t *testing.T) {
	s := &loginModal{validate: make(chan struct{}), release: make(chan struct{})}
	d, server := loginFixture(t, s)
	defer close(s.release)
	if _, err := d.startLogin(context.Background(), "", "", server); err != nil {
		t.Fatal(err)
	}
	close(s.approve)
	select {
	case <-s.validate:
	case <-time.After(5 * time.Second):
		t.Fatal("not validating")
	}
	started := make(chan error, 1)
	go func() { _, err := d.startLogin(context.Background(), "", "", server); started <- err }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a saving account blocked another login")
	}
}

func TestBrowserLoginSaveOutlivesWaitDeadline(t *testing.T) {
	lifetime := loginLifetime
	loginLifetime = time.Second
	t.Cleanup(func() { loginLifetime = lifetime })
	s := &loginModal{validate: make(chan struct{}), release: make(chan struct{}, 1)}
	d, server := loginFixture(t, s)
	defer close(s.release)
	initial, err := d.startLogin(context.Background(), "", "", server)
	if err != nil {
		t.Fatal(err)
	}
	close(s.approve)
	select {
	case <-s.validate:
	case <-time.After(5 * time.Second):
		t.Fatal("not validating")
	}
	// Validation has begun with approved credentials. Holding it past the
	// waiting deadline must not turn a valid token into a save failure.
	time.Sleep(loginLifetime + 50*time.Millisecond)
	s.release <- struct{}{}
	out := awaitLogin(t, d.logins.get(initial.ID))
	if out.State != "connected" || out.Account == nil {
		t.Fatalf("approved account was not saved after waiting expired: %+v", out)
	}
	secret, err := config.LoadSecret(*out.Account)
	if err != nil || secret != "as-private-browser" {
		t.Fatalf("approved credentials were not stored: %v", err)
	}
}

func TestBrowserLoginWaitExpires(t *testing.T) {
	lifetime := loginLifetime
	loginLifetime = 50 * time.Millisecond
	t.Cleanup(func() { loginLifetime = lifetime })
	d, server := loginFixture(t, &loginModal{})
	initial, err := d.startLogin(context.Background(), "", "", server)
	if err != nil {
		t.Fatal(err)
	}
	out := awaitLogin(t, d.logins.get(initial.ID))
	if out.State != "error" || out.Error != "Login link expired. Start browser login again." {
		t.Fatalf("waiting deadline was not reported as expiry: %+v", out)
	}
	if len(d.store.Snapshot().Accounts) != 0 {
		t.Fatal("expired login saved an account")
	}
}

func TestBrowserLoginOpenStartsForegroundBrowser(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec /bin/sleep 2\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	d, server := loginFixture(t, &loginModal{})
	initial, err := d.startLogin(context.Background(), "", "", server)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	d.loginRoutes(mux)
	api := httptest.NewServer(mux)
	defer api.Close()
	// A desktop's opener may stay alive with its browser. The endpoint must
	// return after starting it, well before this fake opener exits.
	client := api.Client()
	client.Timeout = time.Second
	resp, err := client.Post(api.URL+"/api/account-logins/"+initial.ID+"/open", "application/json", nil)
	if err != nil {
		t.Fatalf("opening waited for the browser to exit: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("opening failed: %s", body)
	}
}

func TestBrowserLoginOpenReportsStartFailure(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	d, server := loginFixture(t, &loginModal{})
	initial, err := d.startLogin(context.Background(), "", "", server)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	d.loginRoutes(mux)
	req := httptest.NewRequest("POST", "/api/account-logins/"+initial.ID+"/open", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("reported success without an opener")
	}
	var result struct{ Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error == "" {
		t.Fatal("browser launch error was hidden")
	}
	if out := d.logins.get(initial.ID).snapshot(); out.State != "waiting" || out.URL != initial.URL {
		t.Fatalf("failed browser launch changed the waiting attempt: %+v", out)
	}
}
