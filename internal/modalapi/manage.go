package modalapi

import (
	"context"
	"sort"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Identity describes who a token belongs to.
type Identity struct {
	TokenID       string `json:"token_id"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	Username      string `json:"username,omitempty"`
	TokenName     string `json:"token_name,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
}

// WhoAmI validates the token and returns its workspace and user.
func (c *Client) WhoAmI(ctx context.Context) (Identity, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.TokenInfoGetResponse, error) {
		return c.Stub.TokenInfoGet(ctx, pb.TokenInfoGetRequest_builder{}.Build())
	})
	if err != nil {
		return Identity{}, err
	}
	id := Identity{TokenID: r.GetTokenId(), WorkspaceID: r.GetWorkspaceId(), WorkspaceName: r.GetWorkspaceName(), TokenName: r.GetTokenName()}
	if u := r.GetUserIdentity(); u != nil {
		id.Username = u.GetUsername()
	} else if s := r.GetServiceUserIdentity(); s != nil {
		id.Username = s.GetServiceUserName()
	}
	if t := r.GetExpiresAt(); t != nil && t.GetSeconds() > 0 {
		id.ExpiresAt = t.GetSeconds()
	}
	return id, nil
}

type Environment struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

func (c *Client) ListEnvironments(ctx context.Context) ([]Environment, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.EnvironmentListResponse, error) {
		return c.Stub.EnvironmentList(ctx, &emptypb.Empty{})
	})
	if err != nil {
		return nil, err
	}
	var out []Environment
	for _, e := range r.GetItems() {
		out = append(out, Environment{Name: e.GetName(), Default: e.GetDefault()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// CreateVolume creates a new v2 volume and fails if the name is taken.
func (c *Client) CreateVolume(ctx context.Context, name string) (VolumeInfo, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.VolumeGetOrCreateResponse, error) {
		return c.Stub.VolumeGetOrCreate(ctx, pb.VolumeGetOrCreateRequest_builder{
			DeploymentName: name, EnvironmentName: c.cfg.Environment,
			ObjectCreationType: pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_FAIL_IF_EXISTS,
			Version:            pb.VolumeFsVersion_VOLUME_FS_VERSION_V2,
		}.Build())
	})
	if err != nil {
		return VolumeInfo{}, err
	}
	return VolumeInfo{ID: r.GetVolumeId(), Name: name, Version: r.GetMetadata().GetVersion(), CreatedAt: time.Now().Unix()}, nil
}

// DeleteVolume permanently deletes a volume and all of its data.
func (c *Client) DeleteVolume(ctx context.Context, volID string) error {
	_, err := withRetry(ctx, func(ctx context.Context) (*emptypb.Empty, error) {
		return c.Stub.VolumeDelete(ctx, pb.VolumeDeleteRequest_builder{VolumeId: volID}.Build())
	})
	return err
}

func (c *Client) RenameVolume(ctx context.Context, volID, newName string) error {
	_, err := withRetry(ctx, func(ctx context.Context) (*emptypb.Empty, error) {
		return c.Stub.VolumeRename(ctx, pb.VolumeRenameRequest_builder{VolumeId: volID, Name: newName}.Build())
	})
	return err
}

// Environment returns the environment this client is scoped to ("" = workspace default).
func (c *Client) Environment() string { return c.cfg.Environment }
