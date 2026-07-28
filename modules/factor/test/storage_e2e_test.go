//go:build integration

package e2e_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFactorRealStorageE2E(t *testing.T) {
	require.Equal(t, "1", os.Getenv("MOOX_FACTOR_STORAGE_E2E"),
		"integration test must be started through scripts/test-factor-storage-e2e.sh")
	deployRoot := requiredEnv(t, "MOOX_DEPLOY_ROOT")
	dataNodeID := requiredEnv(t, "MOOX_FACTOR_STORAGE_E2E_DATA_NODE_ID")
	gatewayTarget := requiredEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET")
	gatewayNodeID := requiredEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID")
	credentials := gatewayauth.CredentialsFromEnv()
	require.NotEmpty(t, credentials.KeyID)
	require.NotEmpty(t, credentials.Caller)
	require.NotEmpty(t, credentials.Secret)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	auth := &commonpb.AuthInfo{
		AppId: "moox-factor", Operator: "factor-storage-e2e",
		RequestId: fmt.Sprintf("factor-storage-e2e-%d", time.Now().UnixNano()),
	}
	auth.AppKey = mooxsecurity.HMACSHA256Hex(
		requiredEnv(t, "MOOX_STORAGE_PRIMARY_AUTH_SECRET"),
		[]byte(auth.AppId),
	)
	options := gatewayauth.NewTRPCClientOptions(
		gatewayauth.ServiceGatewayTarget(storageio.NormalizeStorageTarget(gatewayTarget, "11003")),
		gatewayNodeID,
		credentials,
	)
	metadata := storagepb.NewMetadataClientProxy(options...)
	primary := storagepb.NewPrimaryStoreClientProxy(options...)
	view := storagepb.NewDataViewClientProxy(options...)
	factor := factorpb.NewFactorMgrClientProxy(options...)
	storage := storageio.NewClientWithCredentials(gatewayTarget, gatewayNodeID, credentials, auth)

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	spaceID := "factor_e2e_" + suffix
	sourceID := "portfolio_" + suffix
	targetID := "portfolio_factor_" + suffix
	viewID := "factor_view_" + suffix
	factorID := "excess_return_" + suffix
	factorName := "ExcessReturn_" + suffix
	bindingID := "bind-" + suffix
	subjectID := "fund-" + suffix
	const freq = "1m"
	first := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Nanosecond)
	end := second.Add(time.Nanosecond)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if rsp, err := factor.DeleteBinding(cleanupCtx, &factorpb.DeleteBindingReq{BindingId: bindingID}); err != nil ||
			rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			t.Logf("cleanup binding %s failed: rsp=%v err=%v", bindingID, rsp, err)
		}
		if rsp, err := factor.SetFactorStatus(cleanupCtx, &factorpb.SetFactorStatusReq{
			FactorId: factorID, Status: "disabled",
		}); err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			t.Logf("cleanup factor %s failed: rsp=%v err=%v", factorID, rsp, err)
		}
		if rsp, err := metadata.DeleteSpace(cleanupCtx, &storagepb.DeleteSpaceReq{
			AuthInfo: auth, SpaceId: spaceID,
		}); err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			t.Logf("cleanup space %s failed: rsp=%v err=%v", spaceID, rsp, err)
		}
	})

	spaceRsp, err := metadata.CreateSpace(ctx, &storagepb.CreateSpaceReq{
		AuthInfo: auth,
		Space: &storagepb.Space{
			SpaceId: spaceID, Name: "因子验收空间", Owner: "factor-storage-e2e", Status: "active",
		},
	})
	requireStorageOK(t, "CreateSpace", spaceRsp, err)
	dataSourceRsp, err := metadata.CreateDataSource(ctx, &storagepb.CreateDataSourceReq{
		AuthInfo: auth,
		DataSource: &storagepb.DataSource{
			SpaceId: spaceID, DataSourceId: "factor_e2e", Name: "因子验收源",
			Kind: "internal", Timezone: "UTC", Status: "active",
		},
	})
	requireStorageOK(t, "CreateDataSource", dataSourceRsp, err)
	subjectRsp, err := metadata.UpsertSubject(ctx, &storagepb.UpsertSubjectReq{
		AuthInfo: auth,
		Subject: &storagepb.Subject{
			SpaceId: spaceID, SubjectId: subjectID, SubjectType: "custom",
			Name: "验收组合", Timezone: "UTC", Status: "active",
		},
	})
	requireStorageOK(t, "UpsertSubject", subjectRsp, err)
	groupRsp, err := metadata.CreateFieldGroup(ctx, &storagepb.CreateFieldGroupReq{
		AuthInfo: auth,
		FieldGroup: &storagepb.FieldGroup{
			SpaceId: spaceID, GroupId: "factor_e2e", Name: "因子验收", Status: "active",
		},
	})
	requireStorageOK(t, "CreateFieldGroup", groupRsp, err)
	for _, field := range []struct {
		id, name string
	}{
		{id: "nav", name: "净值"},
		{id: "benchmark_return", name: "基准收益"},
	} {
		fieldRsp, fieldErr := metadata.CreateField(ctx, &storagepb.CreateFieldReq{
			AuthInfo: auth,
			Field: &storagepb.Field{
				SpaceId: spaceID, FieldId: field.id, Name: field.name, GroupId: "factor_e2e",
				ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
			},
		})
		requireStorageOK(t, "CreateField "+field.id, fieldRsp, fieldErr)
	}
	sourceRsp, err := metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{
		AuthInfo: auth,
		Dataset: &storagepb.Dataset{
			SpaceId: spaceID, DatasetId: sourceID, DataSourceId: "factor_e2e",
			Name: "组合时序", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs: []string{freq}, DataNodeId: dataNodeID, KeepDuration: "24h", Status: "disabled",
		},
	})
	require.NoError(t, err)
	requireStorageRet(t, "CreateDataset", sourceRsp.GetRetInfo())
	for _, column := range []struct {
		id, display string
	}{
		{id: "nav", display: "净值"},
		{id: "benchmark_return", display: "基准收益"},
	} {
		columnRsp, columnErr := metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{
			AuthInfo: auth,
			Column: &storagepb.DatasetColumn{
				SpaceId: spaceID, DatasetId: sourceID, ColumnName: column.id,
				OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD,
				OriginId:   column.id, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Status: "active", Attributes: map[string]string{"display_name": column.display},
			},
		})
		requireStorageOK(t, "UpsertDatasetColumn "+column.id, columnRsp, columnErr)
	}
	subjectBindingRsp, err := metadata.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{
		AuthInfo: auth,
		DatasetSubject: &storagepb.DatasetSubject{
			SpaceId: spaceID, DatasetId: sourceID, SubjectId: subjectID,
			SubjectRole: "normal", Status: "active",
		},
	})
	requireStorageOK(t, "BindDatasetSubject", subjectBindingRsp, err)
	checkRsp, err := metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{
		AuthInfo: auth, SpaceId: spaceID, DatasetId: sourceID,
	})
	require.NoError(t, err)
	requireStorageRet(t, "CheckDatasetActivation", checkRsp.GetRetInfo())
	require.True(t, checkRsp.GetReady(), "source activation checks: %v", checkRsp.GetChecks())
	activateRsp, err := metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{
		AuthInfo: auth, SpaceId: spaceID, DatasetId: sourceID,
		ExpectedRevision: checkRsp.GetDatasetRevision(),
	})
	requireStorageOK(t, "ActivateDataset", activateRsp, err)

	writeRsp, err := primary.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{
		AuthInfo: auth, SourceEventId: "factor-storage-e2e-input-" + suffix,
		Rows: []*storagepb.RowFieldUpsert{
			inputRow(spaceID, sourceID, subjectID, freq, first, 1.05, 0.01),
			inputRow(spaceID, sourceID, subjectID, freq, second, 1.18, 0.02),
		},
	})
	requireStorageOK(t, "PrimaryStore.UpsertFields", writeRsp, err)
	sourceChunk, err := storage.ReadRangeChunk(ctx, storageio.WindowKey{
		SpaceID: spaceID, SourceDataset: sourceID, SubjectID: subjectID, Freq: freq,
	}, first, end, 1, 10, []string{"benchmark_return", "nav"})
	require.NoError(t, err)
	require.Equal(t, []time.Time{first, second}, sourceChunk.TargetTimes)
	require.Equal(t, []string{"benchmark_return", "nav"}, sourceChunk.Frame.Columns)

	sourceCode := `def compute(df, params):
    excess = df["nav"] - df["benchmark_return"]
    return {
        "excess_return": excess,
        "rolling_rank": excess.rolling(int(params["window"]), min_periods=1).rank(),
    }
`
	createFactorRsp, err := factor.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId: factorID, Name: factorName, SourceCode: sourceCode,
		InputColumns: []string{"nav", "benchmark_return"},
		Outputs:      []string{"excess_return", "rolling_rank"},
		ParamsJson:   `{"window":2}`, LookbackRows: 2, Status: "enabled",
	}})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.CreateFactor", createFactorRsp.GetRetInfo())
	bindRsp, err := factor.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		BindingId: bindingID, FactorId: factorID, SpaceId: spaceID,
		SourceDataset: sourceID, Freq: freq, SubjectMode: "all", SubjectsJson: "[]",
		TargetDataset: targetID, Status: "enabled",
	}})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.UpsertBinding", bindRsp.GetRetInfo())
	targetColumnsRsp, err := metadata.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{
		AuthInfo: auth, SpaceId: spaceID, DatasetId: targetID,
		Page: &commonpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	requireStorageRet(t, "ListDatasetColumns(target)", targetColumnsRsp.GetRetInfo())
	require.ElementsMatch(t, []string{"excess_return", "rolling_rank"}, datasetColumnNames(targetColumnsRsp.GetColumns()))

	viewColumns := []*storagepb.ViewColumn{
		viewColumn(spaceID, viewID, targetID, "excess_return", "超额收益", 10),
		viewColumn(spaceID, viewID, targetID, "rolling_rank", "滚动排名", 20),
	}
	createViewRsp, err := metadata.CreateView(ctx, &storagepb.CreateViewReq{
		AuthInfo: auth,
		View: &storagepb.View{
			SpaceId: spaceID, ViewId: viewID, Name: "因子结果视图",
			PrimaryDatasetId: targetID, DatasetIds: []string{targetID},
			GrainKeys: []string{"subject_id", "freq", "data_time"}, Engine: "duckdb",
			KeepDuration: "24h", Status: "active", Columns: viewColumns,
		},
	})
	requireStorageOK(t, "CreateView", createViewRsp, err)
	waitForViewReady(t, ctx, metadata, auth, spaceID, viewID)

	runDeployedFactor(t, ctx, deployRoot, factorID, spaceID, sourceID, subjectID, freq, first, end)
	rows := waitForViewRows(t, ctx, view, auth, spaceID, viewID, targetID, subjectID, freq, first, second, end, false)
	assertFactorRows(t, rows, first, second, false)

	nullSource := `def compute(df, params):
    excess = df["nav"] - df["benchmark_return"]
    rolling = excess.rolling(int(params["window"]), min_periods=1).rank()
    excess.iloc[-1] = None
    rolling.iloc[-1] = None
    return {"excess_return": excess, "rolling_rank": rolling}
`
	updateRsp, err := factor.UpdateFactor(ctx, &factorpb.UpdateFactorReq{
		FactorId: factorID,
		Factor: &factorpb.FactorDef{
			FactorId: factorID, Name: factorName, SourceCode: nullSource,
			InputColumns: []string{"nav", "benchmark_return"},
			Outputs:      []string{"excess_return", "rolling_rank"},
			ParamsJson:   `{"window":2}`, LookbackRows: 2, Status: "enabled",
		},
	})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.UpdateFactor", updateRsp.GetRetInfo())
	runDeployedFactor(t, ctx, deployRoot, factorID, spaceID, sourceID, subjectID, freq, first, end)
	rows = waitForViewRows(t, ctx, view, auth, spaceID, viewID, targetID, subjectID, freq, first, second, end, true)
	assertFactorRows(t, rows, first, second, true)
}

type retResponse interface {
	GetRetInfo() *commonpb.RetInfo
}

func requireStorageOK[T retResponse](t *testing.T, action string, rsp T, err error) {
	t.Helper()
	require.NoError(t, err, action)
	requireStorageRet(t, action, rsp.GetRetInfo())
}

func requireStorageRet(t *testing.T, action string, ret *commonpb.RetInfo) {
	t.Helper()
	require.NotNil(t, ret, "%s returned no ret_info", action)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, ret.GetCode(), "%s: %s", action, ret.GetMsg())
	t.Logf("real RPC %s succeeded", action)
}

func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	require.NotEmpty(t, value, "%s is required", name)
	return value
}

func inputRow(spaceID, datasetID, subjectID, freq string, at time.Time, nav, benchmark float64) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{
			SpaceId: spaceID, DatasetId: datasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: freq, DataTime: at.Format(time.RFC3339Nano),
			}},
		},
		Fields: []*storagepb.FieldValue{
			doubleValue("nav", nav),
			doubleValue("benchmark_return", benchmark),
		},
	}
}

func doubleValue(id string, value float64) *storagepb.FieldValue {
	return &storagepb.FieldValue{
		FieldId: id,
		Value: &storagepb.TypedValue{
			Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value},
		},
	}
}

func viewColumn(spaceID, viewID, datasetID, output, display string, order uint32) *storagepb.ViewColumn {
	qualified := datasetID + "." + output
	return &storagepb.ViewColumn{
		SpaceId: spaceID, ViewId: viewID, ColumnName: qualified,
		OriginType: storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   qualified, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		SortOrder: order, Attributes: map[string]string{"display_name": display},
	}
}

func waitForViewReady(t *testing.T, ctx context.Context, metadata storagepb.MetadataClientProxy, auth *commonpb.AuthInfo, spaceID, viewID string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rsp, err := metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: auth, SpaceId: spaceID, ViewId: viewID})
		require.NoError(collect, err)
		require.Equal(collect, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		require.NotEmpty(collect, rsp.GetView().GetActiveIndexId())
		require.Equal(collect, rsp.GetView().GetDesiredViewRevision(), rsp.GetView().GetActiveViewRevision())
	}, 60*time.Second, 250*time.Millisecond)
	t.Log("real Storage View reconcile became active")
}

func runDeployedFactor(t *testing.T, ctx context.Context, deployRoot, factorID, spaceID, sourceID, subjectID, freq string, start, end time.Time) {
	t.Helper()
	wrapper := filepath.Join(deployRoot, "bin", "moox-factor-run-once")
	cmd := exec.CommandContext(ctx, wrapper,
		"--space", spaceID,
		"--dataset", sourceID,
		"--subject", subjectID,
		"--freq", freq,
		"--start-time", start.Format(time.RFC3339Nano),
		"--end-time", end.Format(time.RFC3339Nano),
		"--factors", factorID,
	)
	cmd.Env = append(os.Environ(), "MOOX_FACTOR_STORAGE_E2E_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "deployed run-once output: %s", output)
	require.Contains(t, string(output), `"status":"succeeded"`)
	t.Logf("deployed run-once succeeded: %s", strings.TrimSpace(string(output)))
}

func waitForViewRows(
	t *testing.T,
	ctx context.Context,
	view storagepb.DataViewClientProxy,
	auth *commonpb.AuthInfo,
	spaceID, viewID, targetID, subjectID, freq string,
	first, second, end time.Time,
	secondNull bool,
) []*storagepb.TimeSeriesRow {
	t.Helper()
	var rows []*storagepb.TimeSeriesRow
	qualified := []string{targetID + ".excess_return", targetID + ".rolling_rank"}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rsp, err := view.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: spaceID, ViewId: viewID,
			Keys: []*storagepb.TimeSeriesKey{{
				SpaceId: spaceID, DatasetId: targetID, SubjectId: subjectID, Freq: freq,
			}},
			TimeRange: &storagepb.TimeRange{
				StartTime: first.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano),
			},
			ColumnNames: qualified,
			Sorts:       []*storagepb.SortSpec{{FieldName: "data_time"}},
			Page:        &commonpb.Page{Page: 1, Size: 10},
		})
		require.NoError(collect, err)
		require.Equal(collect, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
		require.Equal(collect, qualified, columnNames(rsp.GetColumns()))
		require.Len(collect, rsp.GetRows(), 2)
		require.True(collect, factorRowsHaveExpectedValues(rsp.GetRows(), first, second, secondNull),
			"View has not applied the expected factor patch yet")
		if factorRowsHaveExpectedValues(rsp.GetRows(), first, second, secondNull) {
			rows = rsp.GetRows()
		}
	}, 30*time.Second, 200*time.Millisecond)
	t.Log("real Storage DataView.QueryTimeSeriesRows returned both nanosecond rows")
	return rows
}

func columnNames(columns []*storagepb.ResultColumn) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = column.GetColumnName()
	}
	return out
}

func datasetColumnNames(columns []*storagepb.DatasetColumn) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = column.GetColumnName()
	}
	return out
}

func assertFactorRows(t *testing.T, rows []*storagepb.TimeSeriesRow, first, second time.Time, secondNull bool) {
	t.Helper()
	require.Len(t, rows, 2)
	require.Equal(t, first.Format(time.RFC3339Nano), rows[0].GetKey().GetDataTime())
	require.Equal(t, second.Format(time.RFC3339Nano), rows[1].GetKey().GetDataTime())
	firstValues := rowValues(rows[0])
	require.InDelta(t, 1.04, firstValues["excess_return"].GetDoubleValue(), 1e-12)
	require.InDelta(t, 1.0, firstValues["rolling_rank"].GetDoubleValue(), 1e-12)
	secondValues := rowValues(rows[1])
	if secondNull {
		require.Nil(t, secondValues["excess_return"], "explicit null must clear old double")
		require.Nil(t, secondValues["rolling_rank"], "explicit null must clear old double")
		return
	}
	require.InDelta(t, 1.16, secondValues["excess_return"].GetDoubleValue(), 1e-12)
	require.InDelta(t, 2.0, secondValues["rolling_rank"].GetDoubleValue(), 1e-12)
}

func factorRowsHaveExpectedValues(rows []*storagepb.TimeSeriesRow, first, second time.Time, secondNull bool) bool {
	if len(rows) != 2 ||
		rows[0].GetKey().GetDataTime() != first.Format(time.RFC3339Nano) ||
		rows[1].GetKey().GetDataTime() != second.Format(time.RFC3339Nano) {
		return false
	}
	firstValues := rowValues(rows[0])
	if firstValues["excess_return"] == nil || firstValues["rolling_rank"] == nil ||
		!closeEnough(firstValues["excess_return"].GetDoubleValue(), 1.04) ||
		!closeEnough(firstValues["rolling_rank"].GetDoubleValue(), 1.0) {
		return false
	}
	secondValues := rowValues(rows[1])
	if secondNull {
		return secondValues["excess_return"] == nil && secondValues["rolling_rank"] == nil
	}
	return secondValues["excess_return"] != nil && secondValues["rolling_rank"] != nil &&
		closeEnough(secondValues["excess_return"].GetDoubleValue(), 1.16) &&
		closeEnough(secondValues["rolling_rank"].GetDoubleValue(), 2.0)
}

func closeEnough(got, want float64) bool {
	const epsilon = 1e-12
	delta := got - want
	return delta >= -epsilon && delta <= epsilon
}

func rowValues(row *storagepb.TimeSeriesRow) map[string]*storagepb.TypedValue {
	values := make(map[string]*storagepb.TypedValue, len(row.GetFields()))
	for _, field := range row.GetFields() {
		id := field.GetFieldId()
		if dot := strings.LastIndexByte(id, '.'); dot >= 0 {
			id = id[dot+1:]
		}
		values[id] = field.GetValue()
	}
	return values
}
