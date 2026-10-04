//go:build e2e_external

package bootstrap

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/client"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

func TestExternalStrategyClaimsLogicalAccountFromTrade(t *testing.T) {
	target := strings.TrimSpace(os.Getenv("MOOX_STRATEGY_TRADE_RPC_E2E_TARGET"))
	if target == "" {
		t.Fatal("MOOX_STRATEGY_TRADE_RPC_E2E_TARGET is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	header := &thttp.ClientReqHeader{Header: make(http.Header)}
	header.Header.Set("X-Space-Id", "space-e2e")
	proxy := tradepb.NewTradeConsoleServiceClientProxy(
		client.WithTarget(target),
		client.WithNetwork("tcp"),
		client.WithProtocol("http"),
		client.WithTimeout(3*time.Second),
	)
	created, err := proxy.CreateLogicalAccount(
		ctx,
		&tradepb.CreateLogicalAccountReq{
			Name:            "strategy-rpc-e2e",
			ExecutionMode:   tradepb.ExecutionMode_EXECUTION_MODE_PAPER,
			MarketType:      tradepb.MarketType_MARKET_TYPE_SPOT,
			SettlementAsset: "USDT",
		},
		client.WithReqHead(header),
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS ||
		created.GetLogicalAccount().GetLogicalAccountId() == "" {
		t.Fatalf("create response = %+v", created)
	}
	logicalAccountID := created.GetLogicalAccount().GetLogicalAccountId()
	// This direct-HTTP fixture checks the old RPC contract, not Gateway wiring.
	owner := &logicalAccountOwnerClient{client: proxy, timeout: 3 * time.Second}
	if err = owner.Validate(ctx, "space-e2e", logicalAccountID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = owner.Claim(
			ctx,
			"space-e2e",
			logicalAccountID,
			"runner-e2e",
		); err != nil {
			t.Fatal(err)
		}
	}
	err = owner.Claim(ctx, "space-e2e", logicalAccountID, "runner-conflict")
	if err == nil || !strings.Contains(err.Error(), "code=14") {
		t.Fatalf("conflicting claim error = %v", err)
	}
	for range 2 {
		if err = owner.Release(
			ctx,
			"space-e2e",
			logicalAccountID,
			"runner-e2e",
		); err != nil {
			t.Fatal(err)
		}
	}
}
