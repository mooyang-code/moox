package rpc

import (
	"context"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

func (s *Service) ListCloudAccounts(ctx context.Context, req *pb.ListCloudAccountsReq) (*pb.ListCloudAccountsRsp, error) {
	accounts, total, err := s.catalog.ListAccounts(ctx, req.GetProvider())
	if err != nil {
		return &pb.ListCloudAccountsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.CloudAccountSummary, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, toPBAccountSummary(account))
	}
	return &pb.ListCloudAccountsRsp{RetInfo: retOK(), Accounts: out, Total: total}, nil
}

func (s *Service) CreateCloudAccount(ctx context.Context, req *pb.CreateCloudAccountReq) (*pb.CreateCloudAccountRsp, error) {
	if req.GetAccount() == nil || req.GetAccount().GetAccountId() == "" {
		return &pb.CreateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	account := fromPBAccountInput(req.GetAccount())
	if account.Provider != "tencent" || account.CredentialSecretID == "" {
		return &pb.CreateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "provider must be tencent and credential_secret_id is required")}, nil
	}
	if err := s.catalog.UpsertAccount(ctx, account); err != nil {
		return &pb.CreateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.CreateCloudAccountRsp{RetInfo: retOK(), Account: toPBAccountSummary(account)}, nil
}

func (s *Service) UpdateCloudAccount(ctx context.Context, req *pb.UpdateCloudAccountReq) (*pb.UpdateCloudAccountRsp, error) {
	if req.GetAccount() == nil || req.GetAccount().GetAccountId() == "" {
		return &pb.UpdateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	account := fromPBAccountInput(req.GetAccount())
	if account.Provider != "tencent" || account.CredentialSecretID == "" {
		return &pb.UpdateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "provider must be tencent and credential_secret_id is required")}, nil
	}
	if err := s.catalog.UpsertAccount(ctx, account); err != nil {
		return &pb.UpdateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.UpdateCloudAccountRsp{RetInfo: retOK()}, nil
}

func (s *Service) DeleteCloudAccount(ctx context.Context, req *pb.DeleteCloudAccountReq) (*pb.DeleteCloudAccountRsp, error) {
	if req.GetAccountId() == "" {
		return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	if err := s.catalog.DeleteAccount(ctx, req.GetAccountId()); err != nil {
		return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.DeleteCloudAccountRsp{RetInfo: retOK()}, nil
}

func (s *Service) ListCloudRegions(ctx context.Context, req *pb.ListCloudRegionsReq) (*pb.ListCloudRegionsRsp, error) {
	regions := make([]*pb.CloudRegion, 0, len(tencent.SCFRegions()))
	for _, region := range tencent.SCFRegions() {
		regions = append(regions, &pb.CloudRegion{Code: region.Code, Name: region.Name, Tag: region.Tag, MaxNodes: 128, MaxNamespacesPerRegion: 32, MaxFunctionsPerNamespace: 1024})
	}
	return &pb.ListCloudRegionsRsp{RetInfo: retOK(), Regions: regions, Total: int64(len(regions))}, nil
}

func toPBAccountSummary(account store.CloudAccount) *pb.CloudAccountSummary {
	return &pb.CloudAccountSummary{
		Id:                 int32(account.ID),
		AccountId:          account.AccountID,
		AccountName:        account.AccountName,
		Provider:           account.Provider,
		CredentialSecretId: account.CredentialSecretID,
		AppId:              account.AppID,
		CosRegion:          account.COSRegion,
		CosBucket:          account.COSBucket,
		ExtraConfig:        account.ExtraConfig,
		IsDeleted:          account.IsDeleted,
		CreateTime:         formatTime(account.CreateTime),
		ModifyTime:         formatTime(account.ModifyTime),
	}
}

func fromPBAccountInput(account *pb.CloudAccountInput) store.CloudAccount {
	return store.CloudAccount{
		AccountID:          account.GetAccountId(),
		AccountName:        account.GetAccountName(),
		Provider:           firstString(account.GetProvider(), "tencent"),
		CredentialSecretID: account.GetCredentialSecretId(),
		AppID:              account.GetAppId(),
		COSRegion:          account.GetCosRegion(),
		COSBucket:          account.GetCosBucket(),
		ExtraConfig:        firstString(account.GetExtraConfig(), "{}"),
		IsDeleted:          false,
	}
}
