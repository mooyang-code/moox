package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

const tradeConsolePath = "trpc.moox.trade.TradeConsoleService"

func (w *strategyGatewayWire) verifyOwner(ctx context.Context, method string, request proto.Message) error {
	if err := w.verify(ctx, tradeConsolePath, method, request); err != nil {
		return err
	}
	metadata := codec.Message(ctx).ServerMetaData()
	if string(metadata["X-Space-Id"]) != "crypto" {
		return errors.New("Trade space metadata changed")
	}
	return nil
}

func (w *strategyGatewayWire) owner(id string) *tradepb.LogicalAccount {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logicalOwner == nil {
		w.logicalOwner = &tradepb.LogicalAccount{SpaceId: "crypto", LogicalAccountId: id, OwnerGeneration: 8, AuthFence: "fixture-fence"}
	}
	return proto.Clone(w.logicalOwner).(*tradepb.LogicalAccount)
}

func (w *strategyGatewayWire) GetLogicalAccount(ctx context.Context, req *tradepb.GetLogicalAccountReq) (*tradepb.GetLogicalAccountRsp, error) {
	if err := w.verifyOwner(ctx, "GetLogicalAccount", req); err != nil {
		return nil, err
	}
	w.mu.Lock()
	first := w.calls[tradeConsolePath+"/GetLogicalAccount"] == 1
	w.mu.Unlock()
	if first {
		return nil, errs.NewFrameError(errs.RetServerNoService, "refresh account read")
	}
	return &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: w.owner(req.GetLogicalAccountId())}, nil
}

func (w *strategyGatewayWire) ClaimLogicalAccountOwner(ctx context.Context, req *tradepb.ClaimLogicalAccountOwnerReq) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	if err := w.verifyOwner(ctx, "ClaimLogicalAccountOwner", req); err != nil {
		return nil, err
	}
	if req.GetRunnerId() == "unknown" {
		return nil, errs.NewFrameError(errs.RetServerNoService, "claim result unknown")
	}
	account := w.owner(req.GetLogicalAccountId())
	account.OwnerRunnerId, account.OwnerInstanceId, account.OwnerSessionId = req.GetRunnerId(), req.GetInstanceId(), req.GetSessionId()
	w.mu.Lock()
	w.logicalOwner = account
	w.mu.Unlock()
	return &tradepb.ClaimLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: proto.Clone(account).(*tradepb.LogicalAccount)}, nil
}

func (w *strategyGatewayWire) ReleaseLogicalAccountOwner(ctx context.Context, req *tradepb.ReleaseLogicalAccountOwnerReq) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	if err := w.verifyOwner(ctx, "ReleaseLogicalAccountOwner", req); err != nil {
		return nil, err
	}
	account := w.owner(req.GetLogicalAccountId())
	account.OwnerRunnerId, account.OwnerInstanceId, account.OwnerSessionId = "", "", ""
	w.mu.Lock()
	w.logicalOwner = account
	w.mu.Unlock()
	return &tradepb.ReleaseLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: proto.Clone(account).(*tradepb.LogicalAccount)}, nil
}

func (w *strategyGatewayWire) RebindLogicalAccountOwner(ctx context.Context, req *tradepb.RebindLogicalAccountOwnerReq) (*tradepb.RebindLogicalAccountOwnerRsp, error) {
	if err := w.verifyOwner(ctx, "RebindLogicalAccountOwner", req); err != nil {
		return nil, err
	}
	return &tradepb.RebindLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: w.owner(req.GetLogicalAccountId())}, nil
}

func TestLogicalAccountOwnerUsesSharedNativeGateway(t *testing.T) {
	for _, key := range []string{"MOOX_TRADE_GATEWAY_URL", "MOOX_TRADE_GATEWAY_NODE_ID", "MOOX_GATEWAY_CALLER", "MOOX_GATEWAY_SERVICE_SECRET_KEY"} {
		t.Setenv(key, "ignored-old-input")
	}
	cfg, wire := strategyGatewayFixture(t, "")
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, gateway.Close()) })
	owner := newLogicalAccountOwnerClient(cfg.Trade, gateway)
	require.NoError(t, owner.Validate(t.Context(), "crypto", "logical-1"))
	require.NoError(t, newRPCService(nil, cfg, gateway).LogicalAccounts.Validate(t.Context(), "crypto", "logical-1"))
	require.NoError(t, owner.ClaimSession(t.Context(), "crypto", "logical-1", "instance", "session"))
	require.NoError(t, owner.ValidateSession(t.Context(), "crypto", "logical-1", "instance", "session"))
	require.NoError(t, owner.ReleaseSession(t.Context(), "crypto", "logical-1", "instance", "session"))
	ctx := gatewayclient.WithCallMetadata(t.Context(), gatewayclient.CallMetadata{SpaceID: "crypto"})
	_, err = owner.client.RebindLogicalAccountOwner(ctx, &tradepb.RebindLogicalAccountOwnerReq{LogicalAccountId: "logical-1"})
	require.NoError(t, err)
	require.ErrorContains(t, owner.Claim(t.Context(), "crypto", "logical-1", "unknown"), "claim result unknown")
	_, err = owner.client.GetLogicalAccount(ctx, &tradepb.GetLogicalAccountReq{}, client.WithTarget("ip://127.0.0.1:1"))
	require.ErrorContains(t, err, "instead of tRPC client options")
	_, err = gateway.Forward(ctx, tradeConsolePath, "SubmitOrder", codec.SerializationTypePB, nil)
	require.ErrorContains(t, err, "not allowed")
	wire.mu.Lock()
	claims, releases, rebinds := wire.calls[tradeConsolePath+"/ClaimLogicalAccountOwner"], wire.calls[tradeConsolePath+"/ReleaseLogicalAccountOwner"], wire.calls[tradeConsolePath+"/RebindLogicalAccountOwner"]
	wire.mu.Unlock()
	require.Equal(t, 2, claims, "failed claim must be sent once")
	require.Equal(t, 1, releases)
	require.Equal(t, 1, rebinds)
	require.NoError(t, gateway.Close())
	require.Error(t, owner.Validate(t.Context(), "crypto", "logical-1"))
}

func TestLogicalAccountGatewayRequiresSharedClientAndSpace(t *testing.T) {
	owner := newLogicalAccountOwnerClient(TradeConfig{Timeout: time.Second}, nil)
	require.ErrorContains(t, owner.Validate(t.Context(), "crypto", "logical-1"), "gateway client is required")
	cfg, _ := strategyGatewayFixture(t, "")
	gateway, err := cfg.OpenGateway(nil)
	require.NoError(t, err)
	defer gateway.Close()
	_, err = newLogicalAccountGateway(gateway).GetLogicalAccount(context.Background(), &tradepb.GetLogicalAccountReq{})
	require.ErrorContains(t, err, "space is required")
}
