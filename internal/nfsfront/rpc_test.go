package nfsfront

import (
	"errors"
	"syscall"

	"github.com/willscott/go-nfs-client/nfs/rpc"
)

// The RPC client chooses a random explicit source port even in unprivileged
// mode, where it does not retry bind collisions. Only retry EADDRINUSE from
// Dial: no RPC was sent, and other connection/request errors stay visible.
func dialTestRPC(addr string) (*rpc.Client, error) {
	var client *rpc.Client
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		client, err = rpc.DialTCP("tcp", addr, false)
		if !errors.Is(err, syscall.EADDRINUSE) {
			return client, err
		}
	}
	return nil, err
}
