package test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	cloudnoderpc "github.com/mooyang-code/moox/modules/cloudnode/internal/rpc"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/cloudnode/schema"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestAsyncSCFPublishSubmitRunnerAndStatus(t *testing.T) {
	dbm, err := store.Open(&config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cloudnode.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbm.Close()) })
	require.NoError(t, dbm.ApplySchema(schema.AllSQL()))

	catalog := dbm.Catalog()
	require.NoError(t, catalog.UpsertAccount(context.Background(), store.CloudAccount{
		AccountID: "local-account",
		Provider:  "tencent",
	}))
	require.NoError(t, catalog.UpsertPackage(context.Background(), store.FunctionPackage{
		SpaceID:        "crypto",
		PackageID:      "collector-package",
		PackageName:    "collector",
		Version:        "e2e",
		Runtime:        "Go1",
		PackageType:    "collector",
		WorkloadType:   "collect.binance.kline",
		CloudAccountID: "local-account",
		Status:         "available",
	}))

	svc := cloudnoderpc.New(dbm)
	ctx, cancel := context.WithCancel(spacecontext.WithSpaceID(context.Background(), "crypto"))
	t.Cleanup(cancel)

	items := make([]*pb.NodeCreateItem, 0, 7)
	for index := 0; index < 7; index++ {
		metadata, metadataErr := structpb.NewStruct(map[string]any{
			"function_name_prefix": "collector-e2e",
			"index":                index,
			"biz_type":             "market_fetcher",
		})
		require.NoError(t, metadataErr)
		items = append(items, &pb.NodeCreateItem{
			CloudAccountId: "local-account",
			Region:         "local",
			Namespace:      "default",
			PackageId:      "collector-package",
			Runtime:        "Go1",
			Metadata:       metadata,
		})
	}

	submit, err := svc.SubmitCreateNodes(ctx, &pb.BatchCreateNodesReq{Nodes: items})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, submit.GetRetInfo().GetCode())
	require.NotEmpty(t, submit.GetJobId())
	require.EqualValues(t, len(items), submit.GetTotalCount())

	before, err := svc.GetNodeBatchChange(ctx, &pb.GetNodeBatchChangeReq{JobId: submit.GetJobId()})
	require.NoError(t, err)
	require.Equal(t, pb.NodeBatchStatus_NODE_BATCH_STATUS_PENDING, before.GetJob().GetStatus())
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 3, 100*time.Millisecond))

	require.Eventually(t, func() bool {
		status, statusErr := svc.GetNodeBatchChange(ctx, &pb.GetNodeBatchChangeReq{JobId: submit.GetJobId()})
		if statusErr != nil || status.GetJob() == nil {
			return false
		}
		if status.GetJob().GetStatus() == pb.NodeBatchStatus_NODE_BATCH_STATUS_FAILED ||
			status.GetJob().GetStatus() == pb.NodeBatchStatus_NODE_BATCH_STATUS_PARTIAL {
			t.Logf("unexpected terminal status: %s", status.GetJob().GetStatus())
			for _, item := range status.GetItems() {
				t.Logf("item %s: %s", item.GetItemId(), item.GetErrorMessage())
			}
			return false
		}
		return status.GetJob().GetStatus() == pb.NodeBatchStatus_NODE_BATCH_STATUS_SUCCESS &&
			status.GetJob().GetSuccessCount() == int32(len(items)) &&
			len(status.GetItems()) == len(items)
	}, 5*time.Second, 50*time.Millisecond, fmt.Sprintf("job %s did not finish", submit.GetJobId()))

	nodes, total, err := catalog.ListNodes(context.Background(), "crypto", &pb.GetNodeListReq{})
	require.NoError(t, err)
	require.EqualValues(t, len(items), total)
	require.Len(t, nodes, len(items))
}
