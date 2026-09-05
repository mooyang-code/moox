package rpc

import (
	"context"
	"fmt"

	gonanoid "github.com/matoous/go-nanoid/v2"
	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

type LogicalAccountServer struct {
	LogicalAccounts *logicalapp.Service
	Store           *store.Store
	NewID           func() string
	Flatten         func(
		context.Context,
		string,
		string,
		string,
		string,
	) (store.OperatorActionRecord, error)
}

func (h *LogicalAccountServer) CreateLogicalAccount(
	ctx context.Context,
	req *tradepb.CreateLogicalAccountReq,
) (*tradepb.CreateLogicalAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.CreateLogicalAccountRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	record, err := h.LogicalAccounts.Create(
		ctx,
		spaceID,
		h.logicalAccountID(),
		req.GetName(),
		executionModeFromPB(req.GetExecutionMode()),
		marketFromPB(req.GetMarketType()),
		req.GetSettlementAsset(),
	)
	return &tradepb.CreateLogicalAccountRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) GetLogicalAccount(
	ctx context.Context,
	req *tradepb.GetLogicalAccountReq,
) (*tradepb.GetLogicalAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.GetLogicalAccountRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	record, err := h.Store.GetLogicalAccount(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
	)
	return &tradepb.GetLogicalAccountRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) ListLogicalAccounts(
	ctx context.Context,
	req *tradepb.ListLogicalAccountsReq,
) (*tradepb.ListLogicalAccountsRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ListLogicalAccountsRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	records, err := h.Store.ListLogicalAccounts(ctx, spaceID)
	if err != nil {
		return &tradepb.ListLogicalAccountsRsp{RetInfo: errorInfo(err)}, nil
	}
	page := pageFromPB(req.GetPage())
	total := int64(len(records))
	start, end := page.offset, page.offset+page.size
	if start > len(records) {
		start = len(records)
	}
	if end > len(records) {
		end = len(records)
	}
	values := make([]*tradepb.LogicalAccount, 0, end-start)
	for _, record := range records[start:end] {
		values = append(values, h.logicalAccount(ctx, record))
	}
	return &tradepb.ListLogicalAccountsRsp{
		RetInfo: success(), LogicalAccounts: values,
		PageResult: pageResult(page, total),
	}, nil
}

func (h *LogicalAccountServer) UpdateLogicalAccount(
	ctx context.Context,
	req *tradepb.UpdateLogicalAccountReq,
) (*tradepb.UpdateLogicalAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.UpdateLogicalAccountRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	record, err := h.LogicalAccounts.UpdateName(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
		req.GetName(),
	)
	return &tradepb.UpdateLogicalAccountRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) AddLogicalAccountMember(
	ctx context.Context,
	req *tradepb.AddLogicalAccountMemberReq,
) (*tradepb.AddLogicalAccountMemberRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.AddLogicalAccountMemberRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	err = h.LogicalAccounts.AddMember(ctx, logicalapp.AddMemberCommand{
		SpaceID: spaceID, LogicalAccountID: req.GetLogicalAccountId(),
		TradingAccountID:      req.GetTradingAccountId(),
		Enabled:               req.GetEnabled(),
		Priority:              int(req.GetPriority()),
		AdoptExistingExposure: req.GetAdoptExistingExposure(),
	})
	if err != nil {
		return &tradepb.AddLogicalAccountMemberRsp{RetInfo: errorInfo(err)}, nil
	}
	record, err := h.Store.GetLogicalAccount(ctx, spaceID, req.GetLogicalAccountId())
	return &tradepb.AddLogicalAccountMemberRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) RemoveLogicalAccountMember(
	ctx context.Context,
	req *tradepb.RemoveLogicalAccountMemberReq,
) (*tradepb.RemoveLogicalAccountMemberRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.RemoveLogicalAccountMemberRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	err = h.LogicalAccounts.RemoveMember(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
		req.GetTradingAccountId(),
	)
	if err != nil {
		return &tradepb.RemoveLogicalAccountMemberRsp{RetInfo: errorInfo(err)}, nil
	}
	record, err := h.Store.GetLogicalAccount(ctx, spaceID, req.GetLogicalAccountId())
	return &tradepb.RemoveLogicalAccountMemberRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) ClaimLogicalAccountOwner(
	ctx context.Context,
	req *tradepb.ClaimLogicalAccountOwnerReq,
) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ClaimLogicalAccountOwnerRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	var record store.LogicalAccountRecord
	if req.GetInstanceId() != "" {
		record, _, err = h.LogicalAccounts.ClaimSession(
			ctx, spaceID, req.GetLogicalAccountId(), req.GetInstanceId(),
			req.GetSessionId(), req.GetExpectedAuthFence(),
		)
	} else {
		record, err = h.LogicalAccounts.ClaimOwner(
			ctx, spaceID, req.GetLogicalAccountId(), req.GetRunnerId(),
		)
	}
	return &tradepb.ClaimLogicalAccountOwnerRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) ReleaseLogicalAccountOwner(
	ctx context.Context,
	req *tradepb.ReleaseLogicalAccountOwnerReq,
) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ReleaseLogicalAccountOwnerRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	if req.GetInstanceId() != "" {
		err = h.LogicalAccounts.ReleaseSession(
			ctx, spaceID, req.GetLogicalAccountId(), req.GetInstanceId(),
			req.GetSessionId(), req.GetExpectedAuthFence(),
		)
	} else {
		err = h.LogicalAccounts.ReleaseOwner(
			ctx, spaceID, req.GetLogicalAccountId(), req.GetRunnerId(),
		)
	}
	if err != nil {
		return &tradepb.ReleaseLogicalAccountOwnerRsp{RetInfo: errorInfo(err)}, nil
	}
	record, err := h.Store.GetLogicalAccount(ctx, spaceID, req.GetLogicalAccountId())
	return &tradepb.ReleaseLogicalAccountOwnerRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) RebindLogicalAccountOwner(
	ctx context.Context,
	req *tradepb.RebindLogicalAccountOwnerReq,
) (*tradepb.RebindLogicalAccountOwnerRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.RebindLogicalAccountOwnerRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	var record store.LogicalAccountRecord
	if req.GetInstanceId() != "" {
		record, _, err = h.LogicalAccounts.RebindSession(
			ctx, spaceID, req.GetLogicalAccountId(), req.GetInstanceId(), req.GetSessionId(),
			req.GetExpectedAuthFence(), req.GetNewInstanceId(), req.GetNewSessionId(),
		)
	} else {
		record, err = h.LogicalAccounts.RebindOwner(
			ctx, spaceID, req.GetLogicalAccountId(), req.GetRunnerId(), req.GetRebindKey(),
		)
	}
	return &tradepb.RebindLogicalAccountOwnerRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) PauseLogicalAccount(
	ctx context.Context,
	req *tradepb.PauseLogicalAccountReq,
) (*tradepb.PauseLogicalAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.PauseLogicalAccountRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	record, err := h.LogicalAccounts.Pause(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
		req.GetReason(),
	)
	return &tradepb.PauseLogicalAccountRsp{
		RetInfo:        errorInfo(err),
		LogicalAccount: h.logicalAccount(ctx, record),
	}, nil
}

func (h *LogicalAccountServer) ResumeLogicalAccount(
	ctx context.Context,
	req *tradepb.ResumeLogicalAccountReq,
) (*tradepb.ResumeLogicalAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.ResumeLogicalAccountRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	record, warning, err := h.LogicalAccounts.Resume(
		ctx,
		spaceID,
		req.GetLogicalAccountId(),
	)
	return &tradepb.ResumeLogicalAccountRsp{
		RetInfo: errorInfo(err), LogicalAccount: h.logicalAccount(ctx, record),
		Warning: warning,
	}, nil
}

func (h *LogicalAccountServer) FlattenLogicalAccount(
	ctx context.Context,
	req *tradepb.FlattenLogicalAccountReq,
) (*tradepb.FlattenLogicalAccountRsp, error) {
	spaceID, err := requiredSpace(ctx)
	if err == nil {
		err = validatePB(req)
	}
	if err != nil {
		return &tradepb.FlattenLogicalAccountRsp{
			RetInfo: invalidOrErrorInfo(err),
		}, nil
	}
	if err == nil && h.Flatten == nil {
		err = fmt.Errorf("trade RPC: flatten service is not configured")
	}
	var action store.OperatorActionRecord
	if err == nil {
		action, err = h.Flatten(
			ctx,
			spaceID,
			req.GetActionId(),
			req.GetLogicalAccountId(),
			req.GetReason(),
		)
	}
	return &tradepb.FlattenLogicalAccountRsp{
		RetInfo: errorInfo(err), Action: operatorActionToPB(action),
	}, nil
}

func (h *LogicalAccountServer) logicalAccount(
	ctx context.Context,
	record store.LogicalAccountRecord,
) *tradepb.LogicalAccount {
	if record.LogicalAccountID == "" {
		return nil
	}
	members, err := h.Store.ListLogicalAccountMembers(
		ctx,
		record.SpaceID,
		record.LogicalAccountID,
		true,
	)
	if err != nil {
		return logicalAccountToPB(record, nil, logicalapp.Readiness{
			Reasons: []string{err.Error()},
		})
	}
	readiness, err := h.LogicalAccounts.Readiness(
		ctx,
		record.SpaceID,
		record.LogicalAccountID,
	)
	if err != nil {
		readiness = logicalapp.Readiness{Reasons: []string{err.Error()}}
	}
	return logicalAccountToPB(record, members, readiness)
}

func (h *LogicalAccountServer) logicalAccountID() string {
	if h.NewID != nil {
		return h.NewID()
	}
	id, err := gonanoid.New()
	if err != nil {
		panic(fmt.Sprintf("generate LogicalAccount ID: %v", err))
	}
	return id
}
