package rpc

import (
	"context"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
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
	existing, err := s.catalog.GetAccount(ctx, account.AccountID)
	if err != nil {
		return &pb.UpdateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if existing != nil {
		if account.SecretID == "" {
			account.SecretID = existing.SecretID
		}
		if account.SecretKey == "" {
			account.SecretKey = existing.SecretKey
		}
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

func (s *Service) GetCOSAccountInfo(ctx context.Context, req *pb.GetCOSAccountInfoReq) (*pb.GetCOSAccountInfoRsp, error) {
	account, err := s.catalog.GetAccount(ctx, req.GetAccountId())
	if err != nil {
		return &pb.GetCOSAccountInfoRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if account == nil {
		return &pb.GetCOSAccountInfoRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")}, nil
	}
	return &pb.GetCOSAccountInfoRsp{RetInfo: retOK(), Secret: &pb.CloudAccountSecret{
		AccountId: account.AccountID,
		Provider:  account.Provider,
		AppId:     account.AppID,
		CosRegion: account.COSRegion,
		CosBucket: account.COSBucket,
		SecretId:  reveal(account.SecretID, req.GetReveal()),
		SecretKey: reveal(account.SecretKey, req.GetReveal()),
	}}, nil
}

func (s *Service) ListCloudRegions(ctx context.Context, req *pb.ListCloudRegionsReq) (*pb.ListCloudRegionsRsp, error) {
	regions := []*pb.CloudRegion{
		{Code: "ap-guangzhou", Name: "广州", Tag: "domestic", MaxNodes: 128, MaxNamespacesPerRegion: 32, MaxFunctionsPerNamespace: 1024},
		{Code: "ap-shanghai", Name: "上海", Tag: "domestic", MaxNodes: 128, MaxNamespacesPerRegion: 32, MaxFunctionsPerNamespace: 1024},
		{Code: "ap-hongkong", Name: "中国香港", Tag: "overseas", MaxNodes: 128, MaxNamespacesPerRegion: 32, MaxFunctionsPerNamespace: 1024},
		{Code: "ap-singapore", Name: "新加坡", Tag: "overseas", MaxNodes: 128, MaxNamespacesPerRegion: 32, MaxFunctionsPerNamespace: 1024},
	}
	return &pb.ListCloudRegionsRsp{RetInfo: retOK(), Regions: regions, Total: int64(len(regions))}, nil
}

func toPBAccountSummary(account store.CloudAccount) *pb.CloudAccountSummary {
	return &pb.CloudAccountSummary{
		Id:          int32(account.ID),
		AccountId:   account.AccountID,
		AccountName: account.AccountName,
		Provider:    account.Provider,
		AppId:       account.AppID,
		CosRegion:   account.COSRegion,
		CosBucket:   account.COSBucket,
		ExtraConfig: account.ExtraConfig,
		IsDeleted:   account.IsDeleted,
		CreateTime:  formatTime(account.CreateTime),
		ModifyTime:  formatTime(account.ModifyTime),
	}
}

func fromPBAccountInput(account *pb.CloudAccountInput) store.CloudAccount {
	return store.CloudAccount{
		AccountID:   account.GetAccountId(),
		AccountName: account.GetAccountName(),
		Provider:    firstString(account.GetProvider(), "tencent"),
		SecretID:    account.GetSecretId(),
		SecretKey:   account.GetSecretKey(),
		AppID:       account.GetAppId(),
		COSRegion:   account.GetCosRegion(),
		COSBucket:   account.GetCosBucket(),
		ExtraConfig: firstString(account.GetExtraConfig(), "{}"),
		IsDeleted:   false,
	}
}
