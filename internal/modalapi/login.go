package modalapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
)

// Login follows the Modal CLI's browser token flow. Credentials come back
// over the control plane, never through the browser or the localhost server.
type Login struct {
	URL, Code      string
	client         *Client
	server         *http.Server
	id, waitSecret string
}

func StartLogin(ctx context.Context, cfg Config) (*Login, error) {
	cfg.TokenID, cfg.TokenSecret = "", ""
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		c.Close()
		return nil, err
	}
	flow, err := c.Stub.TokenFlowCreate(ctx, pb.TokenFlowCreateRequest_builder{
		UtmSource: "modalfs", LocalhostPort: int32(l.Addr().(*net.TCPAddr).Port),
	}.Build())
	if err != nil {
		l.Close()
		c.Close()
		return nil, err
	}
	f := &Login{URL: flow.GetWebUrl(), Code: flow.GetCode(), client: c, id: flow.GetTokenFlowId(), waitSecret: flow.GetWaitSecret()}
	mux := http.NewServeMux()
	// This is the CLI's browser validation endpoint, not the daemon API.
	// Its public flow ID is the only value it serves; waitSecret stays here.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		fmt.Fprint(w, f.id)
	})
	f.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = f.server.Serve(l) }()
	return f, nil
}

// Wait long-polls until authorized or cancelled. Modal's timeout response
// means the user is still deciding, not that authentication failed.
func (f *Login) Wait(ctx context.Context) (tokenID, secret string, err error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		r, err := f.client.Stub.TokenFlowWait(cctx, pb.TokenFlowWaitRequest_builder{
			TokenFlowId: f.id, WaitSecret: f.waitSecret, Timeout: 40,
		}.Build())
		cancel()
		if err != nil {
			return "", "", err
		}
		if !r.GetTimeout() {
			return r.GetTokenId(), r.GetTokenSecret(), nil
		}
	}
}

func (f *Login) Close() {
	_ = f.server.Close()
	_ = f.client.Close()
}
