package downloads

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	modal "github.com/modal-labs/modal-client/go"
	"github.com/t41372/modalfs/internal/downloads/remote"
	"github.com/t41372/modalfs/internal/modalapi"
)

// AppName is the Modal app remote downloads run in, one per account and
// environment.
const AppName = "modalfs-downloads"

// ClientVersion is the Modal Python client the deploy Sandbox installs.
const ClientVersion = "1.5.5"

// Modal runs downloads in a Modal Function in the account's workspace.
type Modal struct {
	API *modalapi.Client // the account's client: volume reads, app tags
	App string
	// Timeout is how long one call may run before the agent continues in a
	// new one (MaxRun when zero; the live test shortens it).
	Timeout time.Duration
	// DeployAppID is the app the deploy Sandbox ran in, once it has.
	DeployAppID string

	sdk      *modal.Client
	cfg      modalapi.Config
	mu       sync.Mutex
	deployed string // the agent version known to be deployed
}

// NewModal makes a runner for one account and environment. api must be a
// client for the same account and environment; it isn't closed by Close.
func NewModal(cfg modalapi.Config, api *modalapi.Client, app string) (*Modal, error) {
	if s := strings.TrimSuffix(cfg.ServerURL, "/"); s != "" && s != "https://api.modal.com" && s != "https://api.modal.com:443" {
		// The Modal SDK takes its server from MODAL_SERVER_URL or
		// ~/.modal.toml only; it can't be pointed elsewhere per client.
		return nil, fmt.Errorf("remote downloads only work with Modal's own server, not %s", cfg.ServerURL)
	}
	if app == "" {
		app = AppName
	}
	sdk, err := modal.NewClientWithOptions(&modal.ClientParams{
		TokenID: cfg.TokenID, TokenSecret: cfg.TokenSecret, Environment: cfg.Environment,
	})
	if err != nil {
		return nil, err
	}
	return &Modal{API: api, App: app, sdk: sdk, cfg: cfg}, nil
}

func (m *Modal) Close() { m.sdk.Close() }

// Deploy makes sure the app in the workspace runs this build's agent, and
// deploys it if not. Deploying needs Modal's Python client, so it runs in
// a short-lived Sandbox: users don't need Python. say reports progress.
func (m *Modal) Deploy(ctx context.Context, say func(string)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := remote.Version()
	if m.deployed == want {
		return nil
	}
	_, tags, err := m.API.DeployedApp(ctx, m.App)
	if err != nil && !modalapi.IsNotDeployed(err) {
		return fmt.Errorf("looking up the %s app: %w", m.App, err)
	}
	if tags["modalfs-agent"] == want {
		m.deployed = want
		return nil
	}
	if say == nil {
		say = func(string) {}
	}
	say("Preparing the downloader in your Modal workspace")
	if err := m.deploy(ctx, want); err != nil {
		return err
	}
	if _, tags, err = m.API.DeployedApp(ctx, m.App); err != nil {
		return fmt.Errorf("the %s app was deployed but can't be found: %w", m.App, err)
	}
	if tags["modalfs-agent"] != want {
		return fmt.Errorf("deployed the %s app, but it reports version %q, not %q", m.App, tags["modalfs-agent"], want)
	}
	m.deployed = want
	return nil
}

func (m *Modal) deploy(ctx context.Context, version string) error {
	app, err := m.sdk.Apps.FromName(ctx, m.App+"-deploy", &modal.AppFromNameParams{Environment: m.cfg.Environment, CreateIfMissing: true})
	if err != nil {
		return err
	}
	m.DeployAppID = app.AppID
	image := m.sdk.Images.FromRegistry("python:3.12-slim", nil).
		DockerfileCommands([]string{"RUN pip install --no-cache-dir modal==" + ClientVersion}, nil)
	// The token reaches the Sandbox as an ephemeral Secret, never as an
	// argument or in its environment listing.
	secret, err := m.sdk.Secrets.FromMap(ctx, map[string]string{
		"MODAL_TOKEN_ID": m.cfg.TokenID, "MODAL_TOKEN_SECRET": m.cfg.TokenSecret,
	}, &modal.SecretFromMapParams{Environment: m.cfg.Environment})
	if err != nil {
		return err
	}
	env := map[string]string{"MODALFS_APP_NAME": m.App, "MODALFS_AGENT_VERSION": version}
	if m.cfg.Environment != "" {
		env["MODAL_ENVIRONMENT"] = m.cfg.Environment
	}
	sb, err := m.sdk.Sandboxes.Create(ctx, app, image, &modal.SandboxCreateParams{
		Secrets: []*modal.Secret{secret}, Env: env, Timeout: 10 * time.Minute, CPU: 0.25, MemoryMiB: 1024,
	})
	if err != nil {
		return fmt.Errorf("starting the deploy Sandbox: %w", m.buildReason(ctx, err))
	}
	defer func() { _, _ = sb.Terminate(context.WithoutCancel(ctx), nil) }()
	if err := sb.Filesystem.WriteBytes(ctx, remote.App, "/app/app.py", nil); err != nil {
		return fmt.Errorf("copying the app into the deploy Sandbox: %w", err)
	}
	for name, b := range remote.Files() {
		if err := sb.Filesystem.WriteBytes(ctx, b, "/app/"+name, nil); err != nil {
			return fmt.Errorf("copying %s into the deploy Sandbox: %w", name, err)
		}
	}
	proc, err := sb.Exec(ctx, []string{"modal", "deploy", "/app/app.py"}, &modal.SandboxExecParams{Workdir: "/app"})
	if err != nil {
		return err
	}
	out, _ := io.ReadAll(proc.Stdout)
	errOut, _ := io.ReadAll(proc.Stderr)
	code, err := proc.Wait(ctx, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("modal deploy failed (exit %d): %s", code, lastLines(string(out)+string(errOut), 15))
	}
	return nil
}

// buildReason adds what Modal said about a failed image build, which the
// SDK leaves out of a build it reports as terminated: it may name the cause
// (a spend limit, say). Asking is best effort.
func (m *Modal) buildReason(ctx context.Context, err error) error {
	id := ImageIDIn(err.Error())
	if id == "" {
		return err
	}
	_, exception, e := m.API.ImageBuildResult(ctx, id)
	exception = strings.TrimSpace(exception)
	if e != nil || exception == "" || strings.Contains(err.Error(), exception) {
		return err
	}
	return fmt.Errorf("%w (Modal: %s)", err, exception)
}

// Start spawns a call of the download Function for the job, with its
// volume at /vol.
func (m *Modal) Start(ctx context.Context, j Job) (string, error) {
	if err := m.Deploy(ctx, nil); err != nil {
		return "", err
	}
	fn, err := m.sdk.Functions.FromName(ctx, m.App, "download", &modal.FunctionFromNameParams{Environment: m.cfg.Environment})
	if err != nil {
		return "", err
	}
	size := Sizes[j.Size]
	timeout := m.Timeout
	if timeout == 0 {
		timeout = MaxRun
	}
	// The options only apply once bound with Instance: Spawn on the result
	// of WithOptions alone silently ignores them (no volume attached).
	bound, err := fn.WithOptions(&modal.FunctionWithOptionsParams{
		CPU: &size.CPU, MemoryMiB: &size.MemoryMiB, Timeout: &timeout,
		Volumes: map[string]*modal.Volume{"/vol": {VolumeID: j.VolumeID}},
	}).Instance(ctx)
	if err != nil {
		return "", err
	}
	call, err := bound.Spawn(ctx, []any{j.args(m.App, timeout)}, nil)
	if err != nil {
		return "", err
	}
	return call.FunctionCallID, nil
}

func (m *Modal) Result(ctx context.Context, callID string) (map[string]any, bool, error) {
	fc, err := m.sdk.FunctionCalls.FromID(ctx, callID, nil)
	if err != nil {
		return nil, false, err
	}
	zero := time.Duration(0)
	out, err := fc.Get(ctx, &modal.FunctionCallGetParams{Timeout: &zero})
	var timeout modal.FunctionTimeoutError
	if errors.As(err, &timeout) && strings.HasPrefix(timeout.Exception, "Timeout exceeded") {
		return nil, false, nil // still running
	}
	if err != nil {
		return nil, true, err
	}
	summary, ok := stringKeys(out).(map[string]any)
	if !ok {
		return nil, true, fmt.Errorf("the download Function returned %T, not a summary", out)
	}
	return summary, true, nil
}

// stringKeys turns the map[any]any the SDK decodes Python dicts into into
// map[string]any, all the way down.
func stringKeys(v any) any {
	switch v := v.(type) {
	case map[any]any:
		m := make(map[string]any, len(v))
		for k, x := range v {
			m[fmt.Sprint(k)] = stringKeys(x)
		}
		return m
	case map[string]any:
		for k, x := range v {
			v[k] = stringKeys(x)
		}
		return v
	case []any:
		for i, x := range v {
			v[i] = stringKeys(x)
		}
		return v
	}
	return v
}

func (m *Modal) Cancel(ctx context.Context, callID string) error {
	fc, err := m.sdk.FunctionCalls.FromID(ctx, callID, nil)
	if err != nil {
		return err
	}
	return fc.Cancel(ctx, &modal.FunctionCallCancelParams{TerminateContainers: true})
}

func (m *Modal) Volume(j Job) Volume { return ModalVolume{Client: m.API, VolumeID: j.VolumeID} }
