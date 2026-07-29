package bootstrap

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/client"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/server"
)

type logicalAccountServiceStub struct {
	tradepb.UnimplementedLogicalAccountService

	mu      sync.Mutex
	owner   string
	spaces  []string
	methods []string
}

func (s *logicalAccountServiceStub) GetLogicalAccount(
	ctx context.Context,
	req *tradepb.GetLogicalAccountReq,
) (*tradepb.GetLogicalAccountRsp, error) {
	s.record(ctx, "get")
	return &tradepb.GetLogicalAccountRsp{
		RetInfo:        &tradepb.RetInfo{},
		LogicalAccount: s.account(req.GetLogicalAccountId()),
	}, nil
}

func (s *logicalAccountServiceStub) ClaimLogicalAccountOwner(
	ctx context.Context,
	req *tradepb.ClaimLogicalAccountOwnerReq,
) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	s.record(ctx, "claim")
	s.mu.Lock()
	s.owner = req.GetRunnerId()
	s.mu.Unlock()
	return &tradepb.ClaimLogicalAccountOwnerRsp{
		RetInfo:        &tradepb.RetInfo{},
		LogicalAccount: s.account(req.GetLogicalAccountId()),
	}, nil
}

func (s *logicalAccountServiceStub) ReleaseLogicalAccountOwner(
	ctx context.Context,
	req *tradepb.ReleaseLogicalAccountOwnerReq,
) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	s.record(ctx, "release")
	s.mu.Lock()
	if s.owner == req.GetRunnerId() {
		s.owner = ""
	}
	s.mu.Unlock()
	return &tradepb.ReleaseLogicalAccountOwnerRsp{
		RetInfo:        &tradepb.RetInfo{},
		LogicalAccount: s.account(req.GetLogicalAccountId()),
	}, nil
}

func (s *logicalAccountServiceStub) record(ctx context.Context, method string) {
	spaceID := ""
	if request := thttp.Request(ctx); request != nil {
		spaceID = request.Header.Get("X-Space-Id")
	}
	s.mu.Lock()
	s.spaces = append(s.spaces, spaceID)
	s.methods = append(s.methods, method)
	s.mu.Unlock()
}

func (s *logicalAccountServiceStub) account(id string) *tradepb.LogicalAccount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &tradepb.LogicalAccount{
		LogicalAccountId: id,
		SpaceId:          "space-1",
		OwnerRunnerId:    s.owner,
	}
}

func TestLogicalAccountOwnerClientUsesTradeHTTPAndTrustedSpace(t *testing.T) {
	target, service := startLogicalAccountService(t)
	owner := newLogicalAccountOwnerClient(target, time.Second)

	if err := owner.Validate(context.Background(), "space-1", "logical-1"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := owner.Claim(
			context.Background(),
			"space-1",
			"logical-1",
			"runner-1",
		); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := owner.Release(
			context.Background(),
			"space-1",
			"logical-1",
			"runner-1",
		); err != nil {
			t.Fatal(err)
		}
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if got := strings.Join(service.methods, ","); got != "get,claim,claim,release,release" {
		t.Fatalf("methods = %q", got)
	}
	for _, spaceID := range service.spaces {
		if spaceID != "space-1" {
			t.Fatalf("X-Space-Id = %q, want space-1", spaceID)
		}
	}
}

func startLogicalAccountService(
	t *testing.T,
) (string, *logicalAccountServiceStub) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &logicalAccountServiceStub{}
	tradeServer := server.New(
		server.WithNetwork("tcp"),
		server.WithProtocol("http"),
		server.WithServiceName("trpc.moox.trade.LogicalAccountService"),
		server.WithListener(listener),
	)
	tradepb.RegisterLogicalAccountServiceService(tradeServer, service)
	serveErr := make(chan error, 1)
	go func() { serveErr <- tradeServer.Serve() }()
	t.Cleanup(func() {
		tradeServer.Close(nil)
		select {
		case <-serveErr:
		case <-time.After(time.Second):
			t.Error("Trade test server did not stop")
		}
	})
	return "ip://" + listener.Addr().String(), service
}

type logicalAccountClientProxyStub struct {
	tradepb.LogicalAccountServiceClientProxy

	getResponse     *tradepb.GetLogicalAccountRsp
	getErr          error
	claimResponse   *tradepb.ClaimLogicalAccountOwnerRsp
	releaseResponse *tradepb.ReleaseLogicalAccountOwnerRsp
	waitForContext  bool
}

func (s *logicalAccountClientProxyStub) GetLogicalAccount(
	ctx context.Context,
	_ *tradepb.GetLogicalAccountReq,
	_ ...client.Option,
) (*tradepb.GetLogicalAccountRsp, error) {
	if s.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.getResponse, s.getErr
}

func (s *logicalAccountClientProxyStub) ClaimLogicalAccountOwner(
	_ context.Context,
	_ *tradepb.ClaimLogicalAccountOwnerReq,
	_ ...client.Option,
) (*tradepb.ClaimLogicalAccountOwnerRsp, error) {
	return s.claimResponse, nil
}

func (s *logicalAccountClientProxyStub) ReleaseLogicalAccountOwner(
	_ context.Context,
	_ *tradepb.ReleaseLogicalAccountOwnerReq,
	_ ...client.Option,
) (*tradepb.ReleaseLogicalAccountOwnerRsp, error) {
	return s.releaseResponse, nil
}

func TestLogicalAccountOwnerClientMapsBusinessErrors(t *testing.T) {
	owner := &logicalAccountOwnerClient{
		client: &logicalAccountClientProxyStub{
			getResponse: &tradepb.GetLogicalAccountRsp{
				RetInfo: &tradepb.RetInfo{
					Code: tradepb.ErrorCode_NOT_FOUND,
					Msg:  "logical account missing",
				},
			},
		},
		timeout: time.Second,
	}
	err := owner.Validate(context.Background(), "space-1", "logical-1")
	if err == nil ||
		!strings.Contains(err.Error(), "code=5") ||
		!strings.Contains(err.Error(), "logical account missing") {
		t.Fatalf("error = %v", err)
	}
}

func TestLogicalAccountOwnerClientRejectsMismatchedResponses(t *testing.T) {
	account := &tradepb.LogicalAccount{
		LogicalAccountId: "logical-other",
		SpaceId:          "space-1",
		OwnerRunnerId:    "runner-other",
	}
	owner := &logicalAccountOwnerClient{
		client: &logicalAccountClientProxyStub{
			claimResponse: &tradepb.ClaimLogicalAccountOwnerRsp{
				RetInfo:        &tradepb.RetInfo{},
				LogicalAccount: account,
			},
			releaseResponse: &tradepb.ReleaseLogicalAccountOwnerRsp{
				RetInfo:        &tradepb.RetInfo{},
				LogicalAccount: account,
			},
		},
		timeout: time.Second,
	}
	if err := owner.Claim(
		context.Background(),
		"space-1",
		"logical-1",
		"runner-1",
	); err == nil || !strings.Contains(err.Error(), "mismatched account") {
		t.Fatalf("claim error = %v", err)
	}
	if err := owner.Release(
		context.Background(),
		"space-1",
		"logical-other",
		"runner-other",
	); err == nil || !strings.Contains(err.Error(), "returned owner runner") {
		t.Fatalf("release error = %v", err)
	}
}

func TestLogicalAccountOwnerClientEnforcesTimeout(t *testing.T) {
	owner := &logicalAccountOwnerClient{
		client:  &logicalAccountClientProxyStub{waitForContext: true},
		timeout: 20 * time.Millisecond,
	}
	started := time.Now()
	err := owner.Validate(context.Background(), "space-1", "logical-1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestLogicalAccountOwnerClientValidatesIdentityBeforeTransport(t *testing.T) {
	owner := &logicalAccountOwnerClient{}
	if err := owner.Validate(context.Background(), "", "logical-1"); err == nil ||
		!strings.Contains(err.Error(), "space_id") {
		t.Fatalf("error = %v", err)
	}
	if err := owner.Claim(
		context.Background(),
		"space-1",
		"logical-1",
		"",
	); err == nil || !strings.Contains(err.Error(), "runner_id") {
		t.Fatalf("error = %v", err)
	}
}
