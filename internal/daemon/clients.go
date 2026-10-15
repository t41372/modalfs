package daemon

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/modalapi"
)

// Modal clients. Each one holds a gRPC connection with keepalive goroutines,
// so the API handlers share one per account, environment, server and
// transport instead of dialing for every request. A mount gets a client of
// its own, closed when it stops: its byte counters are the mount's upload
// and download progress, and mustn't mix with another mount's.

type clientKey struct{ account, env, server, transport string }

type clientCache struct {
	mu sync.Mutex
	m  map[clientKey]*modalapi.Client
}

// clientConfig works out how to reach Modal for an account and environment.
func (d *Daemon) clientConfig(accountID, env string) (modalapi.Config, config.Account, error) {
	a, ok := d.store.Account(accountID)
	if !ok {
		return modalapi.Config{}, a, fmt.Errorf("account %q not found", accountID)
	}
	secret, err := config.LoadSecret(a)
	if err != nil {
		return modalapi.Config{}, a, fmt.Errorf("account %s: cannot read token secret: %w", a.Label, err)
	}
	if env == "" {
		env = a.Environment
	}
	server := a.ServerURL
	if server == "" {
		server = "https://api.modal.com"
	}
	return modalapi.Config{TokenID: a.TokenID, TokenSecret: secret, ServerURL: server, Environment: env, Transport: d.transport()}, a, nil
}

// environment names the environment env stands for: itself, or for "" the
// account's, or else the workspace's default. Mounts and downloads are
// pinned to it, so a change of either default moves neither.
func (d *Daemon) environment(ctx context.Context, accountID, env string) (string, error) {
	if env != "" {
		return env, nil
	}
	a, ok := d.store.Account(accountID)
	if !ok {
		return "", fmt.Errorf("account %q not found", accountID)
	}
	if a.Environment != "" {
		return a.Environment, nil
	}
	c, _, err := d.clientFor(accountID, "")
	if err != nil {
		return "", err
	}
	envs, err := c.ListEnvironments(ctx)
	if err != nil {
		return "", friendly(err)
	}
	for _, e := range envs {
		if e.Default {
			return e.Name, nil
		}
	}
	return "", fmt.Errorf("Modal named no default environment for account %s; choose one in its settings", a.Label)
}

func (d *Daemon) transport() string {
	if t := os.Getenv("MODALFS_TRANSPORT"); t != "" {
		return t
	}
	return d.store.Snapshot().Settings.Transport
}

// clientFor returns the shared client for an account and environment, for
// short API calls. Don't close it; dropClients does.
func (d *Daemon) clientFor(accountID, env string) (*modalapi.Client, config.Account, error) {
	cfg, a, err := d.clientConfig(accountID, env)
	if err != nil {
		return nil, a, err
	}
	key := clientKey{a.ID, cfg.Environment, cfg.ServerURL, cfg.Transport}
	d.clients.mu.Lock()
	defer d.clients.mu.Unlock()
	if c := d.clients.m[key]; c != nil {
		return c, a, nil
	}
	c, err := modalapi.New(cfg)
	if err != nil {
		return nil, a, err
	}
	if d.clients.m == nil {
		d.clients.m = map[clientKey]*modalapi.Client{}
	}
	d.clients.m[key] = c
	return c, a, nil
}

// newClient returns a client of its own (for a mount); the caller closes it.
func (d *Daemon) newClient(accountID, env string) (*modalapi.Client, config.Account, error) {
	cfg, a, err := d.clientConfig(accountID, env)
	if err != nil {
		return nil, a, err
	}
	c, err := modalapi.New(cfg)
	return c, a, err
}

// dropClients closes the shared clients of one account, or all of them for
// "". A request still using one fails and can be retried.
func (d *Daemon) dropClients(accountID string) {
	d.clients.mu.Lock()
	var drop []*modalapi.Client
	for k, c := range d.clients.m {
		if accountID == "" || k.account == accountID {
			drop = append(drop, c)
			delete(d.clients.m, k)
		}
	}
	d.clients.mu.Unlock()
	d.dl.dropRunners(accountID) // they use the shared clients
	for _, c := range drop {
		closeClient(c)
	}
}

// closeClient releases a client's connections.
func closeClient(c *modalapi.Client) {
	if c == nil {
		return
	}
	_ = c.Close()
}
