package command

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestProdDatasetPeriodProgressDiagnostic(t *testing.T) {
	if os.Getenv("MOOX_RUN_PROD_PERIOD_DIAG") != "1" {
		t.Skip("production diagnostic is opt-in")
	}
	root, err := filepath.Abs("../../../..")
	require.NoError(t, err)
	snapshot, err := setupconfig.Load(filepath.Join(root, "moox.toml"), root)
	require.NoError(t, err)
	defer clearSetupSecrets(snapshot)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_, transport, session, _, err := openRemoteStorage(ctx, snapshot, "storage")
	require.NoError(t, err)
	defer transport.Close()
	defer session.Close()
	namespace := "pp" + strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	spaceID := namespace + "_space"
	sourceID := namespace + "_source"
	datasetID := "dataset_" + namespace

	items, err := listAllStorageDataNodes(ctx, session.metadata, session.auth)
	require.NoError(t, err)
	node, err := selectDeploymentDataNode(items)
	require.NoError(t, err)

	space := &storagepb.Space{SpaceId: spaceID, Name: "Period E2E", Owner: "storage-e2e", Status: "active"}
	source := &storagepb.DataSource{SpaceId: spaceID, DataSourceId: sourceID, Name: "Period E2E Source", Kind: "internal", Status: "active"}
	var dataset *storagepb.Dataset
	defer func() {
		if dataset != nil {
			rsp, cleanupErr := session.metadata.DeleteDataset(context.Background(), &storagepb.DeleteDatasetReq{AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID})
			require.NoError(t, cleanupErr)
			require.Equal(t, storagepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		}
		rsp, cleanupErr := session.metadata.DeleteDataSource(context.Background(), &storagepb.DeleteDataSourceReq{AuthInfo: session.auth, SpaceId: spaceID, DataSourceId: sourceID})
		require.NoError(t, cleanupErr)
		require.Equal(t, storagepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		spaceRsp, cleanupErr := session.metadata.DeleteSpace(context.Background(), &storagepb.DeleteSpaceReq{AuthInfo: session.auth, SpaceId: spaceID})
		require.NoError(t, cleanupErr)
		require.Equal(t, storagepb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())
	}()
	spaceRsp, err := session.metadata.CreateSpace(ctx, &storagepb.CreateSpaceReq{AuthInfo: session.auth, Space: space})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode(), spaceRsp.GetRetInfo().GetMsg())
	sourceRsp, err := session.metadata.CreateDataSource(ctx, &storagepb.CreateDataSourceReq{AuthInfo: session.auth, DataSource: source})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, sourceRsp.GetRetInfo().GetCode(), sourceRsp.GetRetInfo().GetMsg())

	createRsp, err := session.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: session.auth, Dataset: &storagepb.Dataset{
		SpaceId: spaceID, DatasetId: datasetID, DataSourceId: sourceID, Name: "周期验证集",
		DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, KeepDuration: "0", Status: "disabled",
		DataNodeId: node.GetNodeId(), Freqs: []string{"1m"},
	}})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode(), createRsp.GetRetInfo().GetMsg())
	dataset = createRsp.GetDataset()
	require.NotNil(t, dataset)

	columnRsp, err := session.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: session.auth, Column: &storagepb.DatasetColumn{
		SpaceId: spaceID, DatasetId: datasetID, ColumnName: "value", OriginId: "value",
		ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active", Attributes: map[string]string{"display_name": "数值"},
	}})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, columnRsp.GetRetInfo().GetCode(), columnRsp.GetRetInfo().GetMsg())
	checkRsp, err := session.metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{
		AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID,
	})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, checkRsp.GetRetInfo().GetCode(), checkRsp.GetRetInfo().GetMsg())
	require.True(t, checkRsp.GetReady(), "activation checks: %v", checkRsp.GetChecks())
	activateRsp, err := session.metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{
		AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID, ExpectedRevision: checkRsp.GetDatasetRevision(),
	})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, activateRsp.GetRetInfo().GetCode(), activateRsp.GetRetInfo().GetMsg())
	dataset = activateRsp.GetDataset()
	require.Equal(t, "active", dataset.GetStatus())

	primary, ok := session.primary.(*storagePrimaryProxy)
	require.True(t, ok)
	period := time.Now().UTC().Truncate(time.Minute).Add(-time.Minute)
	expectation := &storagepb.DatasetPeriodExpectation{
		SpaceId: spaceID, DatasetId: datasetID, Frequency: "1m", PeriodTime: period.Unix(),
		SeriesHash: "prod-period-e2e-v1", ExpectedCount: 2, DeadlineAt: period.Add(10 * time.Minute).Unix(),
	}
	ensureRsp, err := primary.proxy.EnsureDatasetPeriod(ctx, &storagepb.PrimaryEnsureDatasetPeriodReq{
		AuthInfo: session.primaryAuth, Expectation: expectation,
	}, primary.options...)
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, ensureRsp.GetRetInfo().GetCode(), ensureRsp.GetRetInfo().GetMsg())
	require.Equal(t, "waiting", ensureRsp.GetStatus())
	makeRow := func(at time.Time, subjectID, value string) *storagepb.RowFieldUpsert {
		return &storagepb.RowFieldUpsert{
			Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: "1m", DataTime: at.Format(time.RFC3339Nano), SeriesTag: "venue:e2e|market:test|source:e2e",
			}}},
			Fields: []*storagepb.FieldValue{{FieldId: "value", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}}},
		}
	}
	rowA := makeRow(period, "SERIES-A", "a")
	rowB := makeRow(period, "SERIES-B", "b")

	firstRsp, err := primary.proxy.CommitTimeSeriesBatch(ctx, &storagepb.PrimaryCommitTimeSeriesBatchReq{
		AuthInfo: session.primaryAuth, Expectation: expectation,
		Items:         []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: rowA}},
		SourceEventId: namespace + "-a", WriteSource: "collector",
	}, primary.options...)
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, firstRsp.GetRetInfo().GetCode(), firstRsp.GetRetInfo().GetMsg())
	require.Equal(t, "waiting", firstRsp.GetPeriodStatus())
	require.Len(t, firstRsp.GetKeys(), 1)
	secondRsp, err := primary.proxy.CommitTimeSeriesBatch(ctx, &storagepb.PrimaryCommitTimeSeriesBatchReq{
		AuthInfo: session.primaryAuth, Expectation: expectation,
		Items:         []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 1, Row: rowB}},
		SourceEventId: namespace + "-b", WriteSource: "collector",
	}, primary.options...)
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, secondRsp.GetRetInfo().GetCode(), secondRsp.GetRetInfo().GetMsg())
	require.Equal(t, "complete", secondRsp.GetPeriodStatus())
	require.Len(t, secondRsp.GetKeys(), 1)

	retryRsp, err := primary.proxy.CommitTimeSeriesBatch(ctx, &storagepb.PrimaryCommitTimeSeriesBatchReq{
		AuthInfo: session.primaryAuth, Expectation: expectation,
		Items:         []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 1, Row: rowB}},
		SourceEventId: namespace + "-b-retry", WriteSource: "collector",
	}, primary.options...)
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, retryRsp.GetRetInfo().GetCode(), retryRsp.GetRetInfo().GetMsg())
	require.Equal(t, "complete", retryRsp.GetPeriodStatus())

	readRsp, err := session.primary.ReadFields(ctx, &storagepb.PrimaryReadFieldsReq{
		AuthInfo: session.primaryAuth, Keys: []*storagepb.RowKey{rowA.GetKey(), rowB.GetKey()}, FieldIds: []string{"value"},
	})
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, readRsp.GetRetInfo().GetCode(), readRsp.GetRetInfo().GetMsg())
	require.Len(t, readRsp.GetRows(), 2)
	values := map[string]string{}
	for _, row := range readRsp.GetRows() {
		require.NotNil(t, row.GetKey().GetTimeSeries())
		require.Len(t, row.GetFields(), 1)
		values[row.GetKey().GetTimeSeries().GetSubjectId()] = row.GetFields()[0].GetValue().GetStringValue()
	}
	require.Equal(t, map[string]string{"SERIES-A": "a", "SERIES-B": "b"}, values)

	degradedPeriod := period.Add(-time.Minute)
	degradedExpectation := &storagepb.DatasetPeriodExpectation{
		SpaceId: spaceID, DatasetId: datasetID, Frequency: "1m", PeriodTime: degradedPeriod.Unix(),
		SeriesHash: "prod-period-e2e-degraded-v1", ExpectedCount: 2, DeadlineAt: time.Now().UTC().Add(2 * time.Second).Unix(),
	}
	degradedEnsureRsp, err := primary.proxy.EnsureDatasetPeriod(ctx, &storagepb.PrimaryEnsureDatasetPeriodReq{
		AuthInfo: session.primaryAuth, Expectation: degradedExpectation,
	}, primary.options...)
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, degradedEnsureRsp.GetRetInfo().GetCode(), degradedEnsureRsp.GetRetInfo().GetMsg())
	require.Equal(t, "waiting", degradedEnsureRsp.GetStatus())
	degradedRow := makeRow(degradedPeriod, "SERIES-DEGRADED", "partial")
	degradedCommitRsp, err := primary.proxy.CommitTimeSeriesBatch(ctx, &storagepb.PrimaryCommitTimeSeriesBatchReq{
		AuthInfo: session.primaryAuth, Expectation: degradedExpectation,
		Items:         []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: degradedRow}},
		SourceEventId: namespace + "-degraded", WriteSource: "collector",
	}, primary.options...)
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, degradedCommitRsp.GetRetInfo().GetCode(), degradedCommitRsp.GetRetInfo().GetMsg())
	require.Equal(t, "waiting", degradedCommitRsp.GetPeriodStatus())
	time.Sleep(4 * time.Second)
	degradedFinalRsp, err := primary.proxy.EnsureDatasetPeriod(ctx, &storagepb.PrimaryEnsureDatasetPeriodReq{
		AuthInfo: session.primaryAuth, Expectation: degradedExpectation,
	}, primary.options...)
	require.NoError(t, err)
	require.Equal(t, storagepb.ErrorCode_SUCCESS, degradedFinalRsp.GetRetInfo().GetCode(), degradedFinalRsp.GetRetInfo().GetMsg())
	require.Equal(t, "degraded", degradedFinalRsp.GetStatus())

	t.Logf("period progress verified: first=%s second=%s retry=%s rows=%d degraded=%s", firstRsp.GetPeriodStatus(), secondRsp.GetPeriodStatus(), retryRsp.GetPeriodStatus(), len(readRsp.GetRows()), degradedFinalRsp.GetStatus())
}
