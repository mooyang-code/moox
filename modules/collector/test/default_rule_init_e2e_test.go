package test

import (
	"context"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/ruleseed"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/server"
)

// TestDefaultRuleInitAndSchedulerE2E proves the complete recovery boundary:
// the packaged rules are seeded into a real Collector SQLite database and one
// Scheduler tick expands both spot and swap instrument sources into task instances.
func TestDefaultRuleInitAndSchedulerE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(collectorschema.AllSQL()))

	rules, err := ruleseed.LoadFile(filepath.Join("..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	summary, err := ruleseed.SeedMissing(ctx, dbm.Tasks(), rules)
	require.NoError(t, err)
	require.Equal(t, 5, summary.TasksCreated)

	cloudNode := &defaultRuleCloudNode{}
	address := startDefaultRuleCloudNode(t, cloudNode)

	scheduler := &marketfetch.Scheduler{
		Tasks: dbm.Tasks(), Instances: dbm.TaskInstances(), Batches: dbm.FetchBatches(), Retries: dbm.FetchRetries(),
		Invoker:           scfinvoker.New([]client.Option{client.WithTarget("ip://" + address), client.WithNetwork("tcp"), client.WithProtocol("trpc")}),
		ResolveSymbol:     marketwiring.ResolveSymbol,
		ResolveSourceID:   marketwiring.DefaultSourceID,
		Symbols:           defaultRuleDatasetSource{},
		SpaceID:           "crypto",
		InvokeConcurrency: 1, Now: func() time.Time { return time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC) },
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool { return cloudNode.invocations.Load() > 0 }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, "crypto", cloudNode.space.Load(), "CloudNode 从 tRPC 元数据取得 space")

	instances, total, err := dbm.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: "crypto", Page: 1, PageSize: 200})
	require.NoError(t, err)
	require.Equal(t, int64(4), total)
	require.Len(t, instances, 4)
	var spot, swap, kline int
	for _, instance := range instances {
		if instance.MarketType == "spot" {
			spot++
		}
		if instance.MarketType == "swap" {
			swap++
		}
		if instance.DataType == "kline" {
			kline++
		}
	}
	require.Equal(t, 2, spot)
	require.Equal(t, 2, swap)
	require.Equal(t, 4, kline)
}

type defaultRuleDatasetSource struct{}

func (defaultRuleDatasetSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "binance"}, nil
}

func (defaultRuleDatasetSource) GetTag(_ context.Context, spaceID, tagID string) (*storagepb.Tag, error) {
	marketType := "spot"
	if tagID == "binance_swap" {
		marketType = "swap"
	}
	return &storagepb.Tag{SpaceId: spaceID, TagId: tagID, Source: "binance", MarketType: marketType}, nil
}

func (defaultRuleDatasetSource) ResolveSubjects(_ context.Context, _ string, tagIDs []string) ([]domain.Subject, error) {
	for _, tagID := range tagIDs {
		if tagID == "binance_swap" {
			return []domain.Subject{{SubjectID: "ETH-USDT", Status: "active"}}, nil
		}
	}
	return []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}}, nil
}

// defaultRuleCloudNode 是假的 CloudNodeMgr：列出一个已部署的采集节点，并记录函数调用。
type defaultRuleCloudNode struct {
	cloudnodepb.UnimplementedCloudNodeMgr
	invocations atomic.Int32
	space       atomic.Value
}

func (f *defaultRuleCloudNode) GetNodeList(ctx context.Context, _ *cloudnodepb.GetNodeListReq) (*cloudnodepb.GetNodeListRsp, error) {
	f.space.Store(string(trpc.GetMetaData(ctx, gatewayroute.MetadataSpaceID)))
	return &cloudnodepb.GetNodeListRsp{
		RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
		Items:   []*cloudnodepb.CloudNode{{NodeId: "node-symbols", FunctionName: "node-symbols", Region: "ap-guangzhou", PackageId: "pkg", BizType: "market_fetcher", TriggerType: "invoke", Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{"deployment_ready": structpb.NewBoolValue(true)}}}},
		Page:    &commonpb.PageResult{Page: 1, Size: 100, Total: 1, HasMore: false},
	}, nil
}

func (f *defaultRuleCloudNode) InvokeFunction(context.Context, *cloudnodepb.InvokeFunctionReq) (*cloudnodepb.InvokeFunctionRsp, error) {
	f.invocations.Add(1)
	return &cloudnodepb.InvokeFunctionRsp{RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"}, Scf: &cloudnodepb.ScfInvokeResult{Code: 0, RequestId: "request-1"}}, nil
}

func startDefaultRuleCloudNode(t *testing.T, impl cloudnodepb.CloudNodeMgrService) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	service := server.New(server.WithListener(listener), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.cloudnode.CloudNodeMgr"))
	cloudnodepb.RegisterCloudNodeMgrService(service, impl)
	go func() { _ = service.Serve() }()
	t.Cleanup(func() { _ = service.Close(nil) })
	return listener.Addr().String()
}
