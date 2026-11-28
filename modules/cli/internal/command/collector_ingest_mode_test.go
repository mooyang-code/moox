package command

import (
	"bytes"
	"context"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestIngestModeCommandsValidateArguments(t *testing.T) {
	var out bytes.Buffer
	require.ErrorContains(t, runIngestModeGet(context.Background(), &out, ingestModeFlags{}), "--space-id")
	require.ErrorContains(t, runIngestModeSet(context.Background(), &out, ingestModeFlags{SpaceID: "crypto", Ingest: "pull"}), "--ingest")
	require.ErrorContains(t, runIngestModeSet(context.Background(), &out, ingestModeFlags{Ingest: "stream"}), "--space-id")
}

func TestGetIngestModeReadsCollectorStatusAndFiltersDataset(t *testing.T) {
	invoke := func(_ context.Context, service, method string, req, rsp any) error {
		require.Equal(t, "trpc.moox.collector.CollectMgr", service)
		require.Equal(t, "GetCollectorStatus", method)
		require.Equal(t, "crypto", req.(*collectorpb.GetCollectorStatusReq).GetSpaceId())
		out := rsp.(*collectorpb.GetCollectorStatusRsp)
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		out.IngestModes = []*collectorpb.IngestMode{
			{DatasetId: "ds_1m", TaskId: "t1", Ingest: "stream", Enabled: true},
			{DatasetId: "ds_1h", TaskId: "t2", Ingest: "scf", Enabled: false},
		}
		out.Stream = &collectorpb.StreamWorkerStatus{InstanceId: "stream-1", Online: true}
		return nil
	}
	all, err := getIngestMode(context.Background(), invoke, "crypto", "")
	require.NoError(t, err)
	require.Len(t, all.GetModes(), 2)
	view := ingestModeView(all)
	modes := view["modes"].([]map[string]any)
	require.Equal(t, true, modes[0]["enabled"])
	require.Equal(t, false, modes[1]["enabled"])

	filtered, err := getIngestMode(context.Background(), invoke, "crypto", "ds_1h")
	require.NoError(t, err)
	require.Len(t, filtered.GetModes(), 1)
	require.Equal(t, "ds_1h", filtered.GetModes()[0].GetDatasetId())
}
