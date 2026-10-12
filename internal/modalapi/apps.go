package modalapi

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// ReadFile reads a whole file of at most max bytes from a volume, for the
// small records remote downloads keep there.
func (c *Client) ReadFile(ctx context.Context, volID, path string, max int64) ([]byte, error) {
	urls, size, err := c.BlockURLs(ctx, volID, path, 0, 1)
	if err != nil {
		return nil, err
	}
	if size > max {
		return nil, fmt.Errorf("%s is %d bytes, more than the %d expected", path, size, max)
	}
	if n := (size + BlockSize - 1) / BlockSize; int64(len(urls)) < n {
		if urls, size, err = c.BlockURLs(ctx, volID, path, 0, n); err != nil {
			return nil, err
		}
	}
	out := make([]byte, 0, size)
	for i, u := range urls {
		n := min(BlockSize, size-int64(i)*BlockSize)
		if n <= 0 {
			break
		}
		b, err := c.Download(ctx, u, int(n))
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	return out, nil
}

// DeployedApp returns the ID and tags of a deployed app, or an error that
// IsNotFound recognises when there's none by that name.
func (c *Client) DeployedApp(ctx context.Context, name string) (string, map[string]string, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.AppGetByDeploymentNameResponse, error) {
		return c.Stub.AppGetByDeploymentName(ctx, pb.AppGetByDeploymentNameRequest_builder{Name: name, EnvironmentName: c.cfg.Environment}.Build())
	})
	if err != nil {
		return "", nil, err
	}
	if r.GetAppId() == "" {
		return "", nil, fmt.Errorf("app %s: %w", name, errNotDeployed)
	}
	t, err := withRetry(ctx, func(ctx context.Context) (*pb.AppGetTagsResponse, error) {
		return c.Stub.AppGetTags(ctx, pb.AppGetTagsRequest_builder{AppId: r.GetAppId()}.Build())
	})
	if err != nil {
		return "", nil, err
	}
	return r.GetAppId(), t.GetTags(), nil
}

var errNotDeployed = errors.New("not deployed")

// IsNotDeployed reports whether DeployedApp found no app by that name.
func IsNotDeployed(err error) bool {
	return err != nil && (IsNotFound(err) || errors.Is(err, errNotDeployed))
}

// StopApp stops a deployed app and everything running in it.
func (c *Client) StopApp(ctx context.Context, appID string) error {
	_, err := withRetry(ctx, func(ctx context.Context) (*emptypb.Empty, error) {
		return c.Stub.AppStop(ctx, pb.AppStopRequest_builder{AppId: appID, Source: pb.AppStopSource_APP_STOP_SOURCE_PYTHON_CLIENT}.Build())
	})
	return err
}

// StopContainer stops a running container, as `modal container stop` does:
// SIGINT, then its inputs are rescheduled. It's what preemption looks like.
func (c *Client) StopContainer(ctx context.Context, taskID string) error {
	_, err := withRetry(ctx, func(ctx context.Context) (*pb.ContainerStopResponse, error) {
		return c.Stub.ContainerStop(ctx, pb.ContainerStopRequest_builder{TaskId: taskID}.Build())
	})
	return err
}
