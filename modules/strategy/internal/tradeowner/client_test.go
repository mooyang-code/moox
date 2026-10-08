package tradeowner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type proxyStub struct {
	getResponse     *tradepb.GetLogicalAccountRsp
	getErr          error
	claimResponse   *tradepb.ClaimLogicalAccountOwnerRsp
	releaseResponse *tradepb.ReleaseLogicalAccountOwnerRsp
	claimRequest    *tradepb.ClaimLogicalAccountOwnerReq
	waitForContext  bool
}

func (s *proxyStub) GetLogicalAccount(ctx context.Context, _ *tradepb.GetLogicalAccountReq, _ ...client.Option) (*tradepb.GetLogicalAccountRsp, error) {
	if s.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.getResponse, s.getErr
}

func (s *proxyStub) ClaimLogicalAccountOwner(_ context.Context, req *tradepb.ClaimLogicalAccountOwnerReq, _ ...client.Option) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	s.claimRequest = req
	return s.claimResponse, nil
}

func (s *proxyStub) ReleaseLogicalAccountOwner(_ context.Context, _ *tradepb.ReleaseLogicalAccountOwnerReq, _ ...client.Option) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	return s.releaseResponse, nil
}

func account(owner, session string) *tradepb.LogicalAccount {
	return &tradepb.LogicalAccount{LogicalAccountId: "logical-1", SpaceId: "space-1", OwnerInstanceId: owner, OwnerSessionId: session, AuthFence: "fence-7"}
}

func TestClaimSessionCarriesAuthFenceAndChecksOwner(t *testing.T) {
	stub := &proxyStub{
		getResponse:   &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: account("", "")},
		claimResponse: &tradepb.ClaimLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: account("instance-1", "session-1")},
	}
	owner := &Client{proxy: stub, timeout: time.Second}
	if err := owner.ClaimSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if stub.claimRequest.GetExpectedAuthFence() != "fence-7" || stub.claimRequest.GetInstanceId() != "instance-1" || stub.claimRequest.GetSessionId() != "session-1" {
		t.Fatalf("认领请求不符：%+v", stub.claimRequest)
	}
	stub.claimResponse = &tradepb.ClaimLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: account("instance-other", "session-1")}
	if err := owner.ClaimSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1"); err == nil || !strings.Contains(err.Error(), "所有者") {
		t.Fatalf("所有者不符应报错：%v", err)
	}
}

func TestValidateSessionDetectsRebind(t *testing.T) {
	stub := &proxyStub{getResponse: &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: account("instance-1", "session-1")}}
	owner := &Client{proxy: stub, timeout: time.Second}
	if err := owner.ValidateSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1"); err != nil {
		t.Fatal(err)
	}
	if err := owner.ValidateSession(context.Background(), "space-1", "logical-1", "instance-1", "session-2"); err == nil {
		t.Fatal("会话不符应报错")
	}
}

func TestClientMapsBusinessErrors(t *testing.T) {
	stub := &proxyStub{getResponse: &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_NOT_FOUND, Msg: "logical account missing"}}}
	owner := &Client{proxy: stub, timeout: time.Second}
	err := owner.ValidateSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1")
	var coded *ResponseError
	if !errors.As(err, &coded) || coded.StatusCode() != 5 || coded.Message != "logical account missing" || !strings.Contains(err.Error(), "账户不存在") || strings.Contains(err.Error(), "logical account missing") {
		t.Fatalf("错误应携带 Trade 错误码与中文说明，Trade 原始消息只留在字段里：%v", err)
	}
	if !IsPermanentClaimError(err) || IsOwnerConflict(err) {
		t.Fatalf("错误分类不符：permanent=%v conflict=%v", IsPermanentClaimError(err), IsOwnerConflict(err))
	}
	conflict := &ResponseError{Operation: "claim", Code: 14}
	if !IsOwnerConflict(conflict) || !IsPermanentClaimError(conflict) {
		t.Fatal("错误码 14 应视为所有权冲突")
	}
	if IsPermanentClaimError(errors.New("timeout")) {
		t.Fatal("传输错误不应视为确定性拒绝")
	}
}

func TestClientRejectsEmptyAndMismatchedResponses(t *testing.T) {
	owner := &Client{proxy: &proxyStub{}, timeout: time.Second}
	if err := owner.ValidateSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1"); err == nil || !strings.Contains(err.Error(), "没有返回状态") {
		t.Fatalf("空响应应报错：%v", err)
	}
	mismatched := &tradepb.LogicalAccount{LogicalAccountId: "logical-other", SpaceId: "space-1"}
	owner = &Client{proxy: &proxyStub{
		getResponse:     &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: account("", "")},
		releaseResponse: &tradepb.ReleaseLogicalAccountOwnerRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: mismatched},
	}, timeout: time.Second}
	if err := owner.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1"); err == nil || !strings.Contains(err.Error(), "不匹配的账户") {
		t.Fatalf("账户不匹配应报错：%v", err)
	}
}

func TestClientEnforcesTimeoutAndIdentity(t *testing.T) {
	owner := &Client{proxy: &proxyStub{waitForContext: true}, timeout: 20 * time.Millisecond}
	started := time.Now()
	if err := owner.ValidateSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应超时：%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("超时耗时过长")
	}
	if err := owner.ClaimSession(context.Background(), "", "logical-1", "instance-1", "session-1"); err == nil || !strings.Contains(err.Error(), "space_id") {
		t.Fatalf("缺少 space_id 应在传输前报错：%v", err)
	}
	if err := owner.ReleaseSession(context.Background(), "space-1", "logical-1", "instance-1", ""); err == nil || !strings.Contains(err.Error(), "session_id") {
		t.Fatalf("缺少 session_id 应在传输前报错：%v", err)
	}
	unwired := New(Config{Timeout: time.Second})
	if err := unwired.ValidateSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1"); err == nil || !strings.Contains(err.Error(), "未配置") {
		t.Fatalf("未接线时应报配置错误：%v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   Config
		valid bool
	}{
		{"未接线", Config{Timeout: time.Second}, true},
		{"远端 HTTPS", Config{GatewayURL: "https://trade.example:11001", TargetNode: "trade", Timeout: time.Second}, true},
		{"本机 HTTP", Config{GatewayURL: "http://127.0.0.1:11002", TargetNode: "control", Timeout: time.Second}, true},
		{"原生协议", Config{GatewayURL: "ip://trade.example:11003", TargetNode: "trade", Timeout: time.Second}, false},
		{"远端明文", Config{GatewayURL: "http://trade.example:11002", TargetNode: "trade", Timeout: time.Second}, false},
		{"缺节点", Config{GatewayURL: "https://trade.example:11001", Timeout: time.Second}, false},
		{"缺 URL", Config{TargetNode: "trade", Timeout: time.Second}, false},
		{"非法节点名", Config{GatewayURL: "https://trade.example:11001", TargetNode: "Trade.Node", Timeout: time.Second}, false},
		{"带凭据", Config{GatewayURL: "https://user:pass@trade.example", TargetNode: "trade", Timeout: time.Second}, false},
		{"带路径", Config{GatewayURL: "https://trade.example/api/service", TargetNode: "trade", Timeout: time.Second}, false},
		{"负超时", Config{Timeout: -time.Second}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); (err == nil) != tc.valid {
				t.Fatalf("Validate() = %v，期望 valid=%v", err, tc.valid)
			}
		})
	}
}
