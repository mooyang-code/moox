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
// Scheduler tick expands both spot and swap symbol sources into task instances.
func TestDefaultRuleInitAndSchedulerE2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dbm, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbm.Close() })
	require.NoError(t, dbm.ApplySchema(collectorschema.AllSQL()))

	rules, err := ruleseed.LoadFile(filepath.Join("..", "..", "..", "examples", "setup", "default", "collector-rules.yaml"))
	require.NoError(t, err)
	summary, err := ruleseed.SeedMissing(ctx, dbm.TaskRules(), rules)
	require.NoError(t, err)
	require.Equal(t, 6, summary.Created)

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
		Rules: dbm.TaskRules(), Instances: dbm.TaskInstances(), Batches: dbm.FetchBatches(), Retries: dbm.FetchRetries(),
		Invoker: scfinvoker.New(scfinvoker.Config{ServiceGatewayTarget: server.URL, Auth: runtime.AuthConfig{AccessKey: "test", SecretKey: "test", TargetNode: "test"}}),
		Symbols: defaultRuleDatasetSource{}, SpaceID: "crypto_market", InvokeNonRealtimeOnly: true,
		InvokeConcurrency: 1, Now: func() time.Time { return time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC) },
	}
	require.NoError(t, scheduler.Tick(ctx, "crypto_market"))

	instances, total, err := dbm.TaskInstances().List(ctx, store.TaskInstanceFilter{SpaceID: "crypto_market", Page: 1, PageSize: 200})
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
	require.Len(t, instances, 5)
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
	require.Equal(t, 3, spot)
	require.Equal(t, 2, swap)
	require.Equal(t, 3, kline)
	require.Eventually(t, func() bool { return invocationCount.Load() >= 2 }, 2*time.Second, 10*time.Millisecond)
}

type defaultRuleDatasetSource struct{}

func (defaultRuleDatasetSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "binance"}, nil
}

func (defaultRuleDatasetSource) ListSubjects(_ context.Context, _ string, datasetID string, _ string) ([]domain.DatasetSubject, error) {
	if datasetID == "binance_swap_symbols" {
		return []domain.DatasetSubject{{SubjectID: "ETH-USDT", ExternalSymbol: "ETHUSDT", Status: "active"}}, nil
	}
	return []domain.DatasetSubject{{SubjectID: "BTC-USDT", ExternalSymbol: "BTCUSDT", Status: "active"}}, nil
}
