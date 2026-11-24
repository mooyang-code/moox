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
		Users: int32(result.Users), Secrets: int32(result.Secrets),
		ExchangeKeys: exchangeKeyResultsToPB(result.ExchangeKeys),
		Kept:         result.Kept,
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
		Users: int32(status.Users), Secrets: int32(status.Secrets),
		Missing: int32(status.Missing), Kept: status.Kept,
		ExchangeKeys: exchangeKeyResultsToPB(status.ExchangeKeys),
	}, nil
}

func applyManifest(request *pb.ApplySetupReq) (setup.Manifest, error) {
	if request == nil {
		return setup.Manifest{}, setup.ErrInvalid
	}
	return manifestFromPB(request.GetAdmin(), request.GetTencentCloud(), request.GetExchangeKeys())
}

func statusManifest(request *pb.GetSetupStatusReq) (setup.Manifest, error) {
	if request == nil {
		return setup.Manifest{}, setup.ErrInvalid
	}
	return manifestFromPB(request.GetAdmin(), request.GetTencentCloud(), request.GetExchangeKeys())
}

func manifestFromPB(
	admin *pb.SetupAdmin,
	cloud *pb.SetupTencentCloud,
	exchangeKeys []*pb.SetupExchangeKey,
) (setup.Manifest, error) {
	if admin == nil || cloud == nil {
		return setup.Manifest{}, setup.ErrInvalid
	}
	manifest := setup.Manifest{
		Admin:        setup.Admin{Username: admin.GetUsername(), Password: admin.GetPassword()},
		TencentCloud: setup.TencentCloud{SecretID: cloud.GetSecretId(), SecretKey: cloud.GetSecretKey()},
	}
	for _, key := range exchangeKeys {
		if key == nil {
			return setup.Manifest{}, setup.ErrInvalid
		}
		manifest.ExchangeKeys = append(manifest.ExchangeKeys, setup.ExchangeKey{
			Name: key.GetName(), Exchange: key.GetExchange(), Environment: key.GetEnvironment(),
			APIKey: key.GetApiKey(), SecretKey: key.GetSecretKey(), Passphrase: key.GetPassphrase(), ReadOnly: key.GetReadonly(),
		})
	}
	return manifest, nil
}

func exchangeKeyResultsToPB(results []setup.ExchangeKeyResult) []*pb.SetupExchangeKeyResult {
	out := make([]*pb.SetupExchangeKeyResult, 0, len(results))
	for _, result := range results {
		out = append(out, &pb.SetupExchangeKeyResult{SecretId: result.SecretID, State: result.State, Readonly: result.ReadOnly})
	}
	return out
}

func mapSetupError(err error) (pb.ErrorCode, string) {
	switch {
	case errors.Is(err, setup.ErrInvalid):
		return pb.ErrorCode_INVALID_PARAM, "setup_invalid"
	case errors.Is(err, setup.ErrAdminMissing):
		return pb.ErrorCode_INVALID_PARAM, "setup_admin_missing：还没有管理员，先用 setup deploy 部署控制机（会建管理员）"
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
