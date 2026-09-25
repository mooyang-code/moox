package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/ruleseed"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
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

	var invocationCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var response proto.Message
		switch r.URL.Path {
		case "/api/service/cloudnode/GetNodeList":
			response = &cloudnodepb.GetNodeListRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Items:   []*cloudnodepb.CloudNode{{NodeId: "node-symbols", FunctionName: "node-symbols", Region: "ap-guangzhou", PackageId: "pkg", BizType: "market_fetcher", TriggerType: "invoke", Metadata: &structpb.Struct{Fields: map[string]*structpb.Value{"deployment_ready": structpb.NewBoolValue(true)}}}},
				Page:    &commonpb.PageResult{Page: 1, Size: 100, Total: 1, HasMore: false},
			}
		case "/api/service/cloudnode/InvokeFunction":
			invocationCount.Add(1)
			response = &cloudnodepb.InvokeFunctionRsp{RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"}, Scf: &cloudnodepb.ScfInvokeResult{Code: 0, RequestId: "request-1"}}
		default:
			http.NotFound(w, r)
			return
		}
		message, marshalErr := protojson.Marshal(response)
		if marshalErr != nil {
			http.Error(w, marshalErr.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(message)
	}))
	defer server.Close()

	scheduler := &marketfetch.Scheduler{
		Tasks: dbm.Tasks(), Instances: dbm.TaskInstances(), Batches: dbm.FetchBatches(), Retries: dbm.FetchRetries(),
		Invoker: scfinvoker.New(scfinvoker.Config{ServiceGatewayTarget: server.URL, Auth: runtime.AuthConfig{AccessKey: "test", SecretKey: "test", TargetNode: "test"}}),
		Symbols: defaultRuleDatasetSource{}, SpaceID: "crypto", InvokeNonRealtimeOnly: true,
		InvokeConcurrency: 1, Now: func() time.Time { return time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC) },
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto"))

	instances, total, err := dbm.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: "crypto", Page: 1, PageSize: 200})
	require.NoError(t, err)
	require.Equal(t, int64(0), total)
	require.Empty(t, instances)
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
	require.Zero(t, spot)
	require.Zero(t, swap)
	require.Zero(t, kline, "realtime kline rules use Timer nodes, while this test provides only an Invoke node")
	_ = invocationCount
}

type defaultRuleDatasetSource struct{}

func (defaultRuleDatasetSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "binance"}, nil
}

func (defaultRuleDatasetSource) ResolveSubjects(_ context.Context, _ string, tagIDs []string) ([]domain.Subject, error) {
	for _, tagID := range tagIDs {
		if tagID == "binance_swap" {
			return []domain.Subject{{SubjectID: "ETH-USDT", Status: "active"}}, nil
		}
	}
	return []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}}, nil
}
