package rpc

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/admin/internal/service/setup"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

type setupDomain interface {
	Apply(context.Context, setup.Manifest) (setup.Result, error)
	Inspect(context.Context, setup.Manifest) (setup.Status, error)
}

type Service struct {
	pb.UnimplementedSetup
	domain setupDomain
}

func NewService(domain setupDomain) *Service {
	return &Service{domain: domain}
}

func (s *Service) ApplySetup(ctx context.Context, request *pb.ApplySetupReq) (*pb.ApplySetupRsp, error) {
	manifest, err := applyManifest(request)
	if err != nil || s.domain == nil {
		return &pb.ApplySetupRsp{RetInfo: setupRetError(pb.ErrorCode_INVALID_PARAM, "setup_invalid")}, nil
	}
	result, err := s.domain.Apply(ctx, manifest)
	if err != nil {
		code, message := mapSetupError(err)
		return &pb.ApplySetupRsp{RetInfo: setupRetError(code, message)}, nil
	}
	return &pb.ApplySetupRsp{
		RetInfo: setupRetOK(), Action: result.Action,
		Users: int32(result.Users), Secrets: int32(result.Secrets), Hosts: int32(result.Hosts),
		Spaces: int32(result.Spaces), SpacesCreated: int32(result.SpacesCreated),
		SpacesUnchanged: int32(result.SpacesUnchanged),
	}, nil
}

func (s *Service) GetSetupStatus(ctx context.Context, request *pb.GetSetupStatusReq) (*pb.GetSetupStatusRsp, error) {
	manifest, err := statusManifest(request)
	if err != nil || s.domain == nil {
		return &pb.GetSetupStatusRsp{RetInfo: setupRetError(pb.ErrorCode_INVALID_PARAM, "setup_invalid")}, nil
	}
	status, err := s.domain.Inspect(ctx, manifest)
	if err != nil {
		code, message := mapSetupError(err)
		return &pb.GetSetupStatusRsp{RetInfo: setupRetError(code, message)}, nil
	}
	return &pb.GetSetupStatusRsp{
		RetInfo: setupRetOK(), State: status.State,
		Users: int32(status.Users), Secrets: int32(status.Secrets), Hosts: int32(status.Hosts),
		Missing: int32(status.Missing), Conflicts: int32(status.Conflicts), Spaces: int32(status.Spaces),
	}, nil
}

func applyManifest(request *pb.ApplySetupReq) (setup.Manifest, error) {
	if request == nil {
		return setup.Manifest{}, setup.ErrInvalid
	}
	return manifestFromPB(
		request.GetAdmin(), request.GetTencentCloud(), request.GetControlHost(),
		request.GetOtherHosts(), request.GetSpaces(),
	)
}

func statusManifest(request *pb.GetSetupStatusReq) (setup.Manifest, error) {
	if request == nil {
		return setup.Manifest{}, setup.ErrInvalid
	}
	return manifestFromPB(
		request.GetAdmin(), request.GetTencentCloud(), request.GetControlHost(),
		request.GetOtherHosts(), request.GetSpaces(),
	)
}

func manifestFromPB(
	admin *pb.SetupAdmin,
	cloud *pb.SetupTencentCloud,
	control *pb.SetupHost,
	others []*pb.SetupHost,
	spaces []*pb.SetupSpace,
) (setup.Manifest, error) {
	if admin == nil || cloud == nil || control == nil {
		return setup.Manifest{}, setup.ErrInvalid
	}
	manifest := setup.Manifest{
		Admin:        setup.Admin{Username: admin.GetUsername(), Password: admin.GetPassword()},
		TencentCloud: setup.TencentCloud{SecretID: cloud.GetSecretId(), SecretKey: cloud.GetSecretKey()},
		ControlHost:  hostFromPB(control),
		OtherHosts:   make([]setup.Host, 0, len(others)),
		Spaces:       make([]setup.Space, 0, len(spaces)),
	}
	for _, host := range others {
		if host == nil {
			return setup.Manifest{}, setup.ErrInvalid
		}
		manifest.OtherHosts = append(manifest.OtherHosts, hostFromPB(host))
	}
	for _, space := range spaces {
		if space == nil {
			return setup.Manifest{}, setup.ErrInvalid
		}
		manifest.Spaces = append(manifest.Spaces, spaceFromPB(space))
	}
	return manifest, nil
}

func hostFromPB(host *pb.SetupHost) setup.Host {
	return setup.Host{
		Name: host.GetName(), Address: host.GetAddress(), Port: int(host.GetPort()),
		Username: host.GetUsername(), Password: host.GetPassword(),
	}
}

func spaceFromPB(space *pb.SetupSpace) setup.Space {
	return setup.Space{
		SpaceID: space.GetSpaceId(), Name: space.GetName(), Description: space.GetDescription(),
		Owner: space.GetOwner(), Market: space.GetMarket(), Timezone: space.GetTimezone(),
		Status: space.GetStatus(), AttributesJSON: space.GetAttributesJson(),
	}
}

func mapSetupError(err error) (pb.ErrorCode, string) {
	switch {
	case errors.Is(err, setup.ErrConflict):
		return pb.ErrorCode_INVALID_PARAM, "setup_conflict"
	case errors.Is(err, setup.ErrInvalid):
		return pb.ErrorCode_INVALID_PARAM, "setup_invalid"
	default:
		return pb.ErrorCode_INNER_ERR, "setup_storage_failed"
	}
}

func setupRetOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

func setupRetError(code pb.ErrorCode, message string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: message}
}
