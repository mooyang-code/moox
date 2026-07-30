//go:build integration

package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestFactorRealStorageE2E(t *testing.T) {
	require.Equal(t, "1", os.Getenv("MOOX_FACTOR_STORAGE_E2E"),
		"integration test must be started through scripts/test-factor-storage-e2e.sh")
	deployRoot := requiredEnv(t, "MOOX_DEPLOY_ROOT")
	dataNodeID := requiredEnv(t, "MOOX_FACTOR_STORAGE_E2E_DATA_NODE_ID")
	dataNodeTarget := requiredEnv(t, "MOOX_FACTOR_STORAGE_E2E_DATA_NODE_TARGET")
	gatewayTarget := requiredEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET")
	gatewayNodeID := requiredEnv(t, "MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID")
	credentials := gatewayauth.CredentialsFromEnv()
	require.NotEmpty(t, credentials.KeyID)
	require.NotEmpty(t, credentials.Caller)
	require.NotEmpty(t, credentials.Secret)
	factorCredentials := gatewayauth.Credentials{
		KeyID:  requiredEnv(t, "MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_KEY_ID"),
		Caller: requiredEnv(t, "MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_CALLER"),
		Secret: requiredEnv(t, "MOOX_FACTOR_STORAGE_E2E_FACTOR_GATEWAY_SECRET"),
	}

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
	viewAuth := &commonpb.AuthInfo{
		AppId: auth.GetAppId(), Operator: auth.GetOperator(), RequestId: auth.GetRequestId(),
	}
	viewAuth.AppKey = mooxsecurity.HMACSHA256Hex(
		requiredEnv(t, "MOOX_STORAGE_VIEW_AUTH_SECRET"),
		[]byte(viewAuth.AppId),
	)
	dataNodeAuth := &commonpb.AuthInfo{
		AppId: auth.GetAppId(), Operator: auth.GetOperator(), RequestId: auth.GetRequestId(),
	}
	dataNodeAuth.AppKey = mooxsecurity.HMACSHA256Hex(
		requiredEnv(t, "MOOX_STORAGE_NODE_AUTH_SECRET"),
		[]byte(dataNodeAuth.AppId),
	)
	options := gatewayauth.NewTRPCClientOptions(
		gatewayauth.ServiceGatewayTarget(storageio.NormalizeStorageTarget(gatewayTarget, "11003")),
		gatewayNodeID,
		credentials,
	)
	metadata := storagepb.NewMetadataClientProxy(options...)
	primary := storagepb.NewPrimaryStoreClientProxy(options...)
	view := storagepb.NewDataViewClientProxy(options...)
	factor := factorpb.NewFactorMgrClientProxy(gatewayauth.NewTRPCClientOptions(
		gatewayauth.ServiceGatewayTarget(storageio.NormalizeStorageTarget(gatewayTarget, "11003")),
		gatewayNodeID,
		factorCredentials,
	)...)
	cleanupMetadata := storagepb.NewMetadataClientProxy(gatewayauth.NewTRPCClientOptions(
		gatewayauth.ServiceGatewayTarget(storageio.NormalizeStorageTarget(gatewayTarget, "11003")),
		gatewayNodeID,
		factorCredentials,
	)...)
	dataNode := storagepb.NewDataNodeRuntimeClientProxy(
		client.WithTarget(dataNodeTarget),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
	)
	storage := storageio.NewClientWithCredentials(gatewayTarget, gatewayNodeID, credentials, auth)

	suffix := fmt.Sprintf("%x", time.Now().UnixNano())
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	displaySuffix := suffix
	if len(displaySuffix) > 4 {
		displaySuffix = displaySuffix[len(displaySuffix)-4:]
	}
	spaceID := "factor_e2e_" + suffix
	sourceID := "portfolio_" + suffix
	targetID := "portfolio_factor_" + suffix
	sourceViewID := "source_view_" + suffix
	viewID := "factor_view_" + suffix
	factorID := "venue_spread_" + suffix
	factorName := "VenueSpread_" + suffix
	bindingID := "bind-" + suffix
	subjectID := "fund-" + suffix
	const freq = "1m"
	first := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	third := second.Add(time.Minute)
	end := third.Add(time.Nanosecond)
	var spaceCreated, factorCreated, bindingCreated bool

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if bindingCreated {
			if rsp, err := factor.DeleteBinding(cleanupCtx, &factorpb.DeleteBindingReq{BindingId: bindingID}); err != nil ||
				rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
				reportCleanupFailure(t, "binding "+bindingID, rsp, err)
			}
		}
		if factorCreated {
			if rsp, err := factor.DeleteFactor(cleanupCtx, &factorpb.DeleteFactorReq{
				FactorId: factorID,
			}); err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
				reportCleanupFailure(t, "factor "+factorID, rsp, err)
			} else {
				assertFactorArtifactsRemoved(t, deployRoot, factorName)
			}
		}
		if spaceCreated {
			cleanupDatasetBuckets(t, cleanupCtx, dataNode, dataNodeAuth, dataNodeID, spaceID, sourceID)
			cleanupDatasetBuckets(t, cleanupCtx, dataNode, dataNodeAuth, dataNodeID, spaceID, targetID)
			if rsp, err := cleanupMetadata.DeleteSpace(cleanupCtx, &storagepb.DeleteSpaceReq{
				AuthInfo: auth, SpaceId: spaceID,
			}); err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
				reportCleanupFailure(t, "space "+spaceID, rsp, err)
			} else {
				t.Logf("cleanup space %s succeeded", spaceID)
			}
		}
	})

	spaceRsp, err := metadata.CreateSpace(ctx, &storagepb.CreateSpaceReq{
		AuthInfo: auth,
		Space: &storagepb.Space{
			SpaceId: spaceID, Name: "验收" + displaySuffix, Owner: "factor-storage-e2e", Status: "active",
		},
	})
	requireStorageOK(t, "CreateSpace", spaceRsp, err)
	spaceCreated = true
	dataSourceRsp, err := metadata.CreateDataSource(ctx, &storagepb.CreateDataSourceReq{
		AuthInfo: auth,
		DataSource: &storagepb.DataSource{
			SpaceId: spaceID, DataSourceId: "factor_e2e", Name: "验源" + displaySuffix,
			Kind: "internal", Timezone: "UTC", Status: "active",
		},
	})
	requireStorageOK(t, "CreateDataSource", dataSourceRsp, err)
	subjectRsp, err := metadata.UpsertSubject(ctx, &storagepb.UpsertSubjectReq{
		AuthInfo: auth,
		Subject: &storagepb.Subject{
			SpaceId: spaceID, SubjectId: subjectID, SubjectType: "custom",
			Name: "组合" + displaySuffix, Timezone: "UTC", Status: "active",
		},
	})
	requireStorageOK(t, "UpsertSubject", subjectRsp, err)
	groupRsp, err := metadata.CreateFieldGroup(ctx, &storagepb.CreateFieldGroupReq{
		AuthInfo: auth,
		FieldGroup: &storagepb.FieldGroup{
			SpaceId: spaceID, GroupId: "factor_e2e", Name: "验组" + displaySuffix, Status: "active",
		},
	})
	requireStorageOK(t, "CreateFieldGroup", groupRsp, err)
	for _, field := range []struct {
		id, name string
	}{
		{id: "close", name: "收盘价"},
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
			Name: "时序" + displaySuffix, DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs: []string{freq}, DataNodeId: dataNodeID, KeepDuration: "24h", Status: "disabled",
		},
	})
	require.NoError(t, err)
	requireStorageRet(t, "CreateDataset", sourceRsp.GetRetInfo())
	for _, column := range []struct {
		id, display string
	}{
		{id: "close", display: "收盘价"},
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
	sourceViewRsp, err := metadata.CreateView(ctx, &storagepb.CreateViewReq{
		AuthInfo: auth,
		View: &storagepb.View{
			SpaceId: spaceID, ViewId: sourceViewID, Name: "源视" + displaySuffix,
			PrimaryDatasetId: sourceID, DatasetIds: []string{sourceID},
			GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Engine: "duckdb",
			FilterJson: `{"freq":"1m"}`, KeepDuration: "24h", Status: "active",
			Columns: []*storagepb.ViewColumn{
				viewColumn(spaceID, sourceViewID, sourceID, "close", "收盘价", 10),
			},
		},
	})
	requireStorageOK(t, "CreateView(source)", sourceViewRsp, err)
	waitForViewReady(t, ctx, metadata, auth, spaceID, sourceViewID)
	waitForViewQueryable(t, ctx, view, viewAuth, spaceID, sourceViewID, sourceID, subjectID, freq, first, end)

	writeRsp, err := primary.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{
		AuthInfo: auth, SourceEventId: "factor-storage-e2e-input-" + suffix,
		Rows: []*storagepb.RowFieldUpsert{
			inputRow(spaceID, sourceID, subjectID, freq, first, "venue:binance", 101),
			inputRow(spaceID, sourceID, subjectID, freq, first, "venue:okx", 100),
			inputRow(spaceID, sourceID, subjectID, freq, second, "venue:binance", 104),
			inputRow(spaceID, sourceID, subjectID, freq, second, "venue:okx", 102),
			inputRow(spaceID, sourceID, subjectID, freq, third, "venue:binance", 108),
			inputRow(spaceID, sourceID, subjectID, freq, third, "venue:okx", 105),
		},
	})
	requireStorageOK(t, "PrimaryStore.UpsertFields", writeRsp, err)
	var sourceChunk *storageio.RangeChunk
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		chunk, readErr := storage.ReadRangeChunk(ctx, storageio.WindowKey{
			SpaceID: spaceID, SourceDataset: sourceID, SubjectID: subjectID, Freq: freq,
		}, first, end, 1, 10, []string{"close"})
		assert.NoError(collect, readErr)
		if readErr == nil {
			sourceChunk = chunk
			assert.Equal(collect, []time.Time{first, second, third}, chunk.TargetPeriods)
		}
	}, 20*time.Second, 250*time.Millisecond, "source rows were not materialized")
	require.Equal(t, []time.Time{first, second, third}, sourceChunk.TargetPeriods)
	require.Equal(t, []string{"close"}, sourceChunk.Frame.Columns)
	require.Equal(t,
		[]string{"venue:binance", "venue:okx", "venue:binance", "venue:okx", "venue:binance", "venue:okx"},
		sourceChunk.Frame.SeriesTags,
	)

	sourceCode := `import pandas as pd

def compute(df, params):
    left = df[df["series_tag"] == params["left_tag"]][["data_time", "close"]]
    right = df[df["series_tag"] == params["right_tag"]][["data_time", "close"]]
    joined = left.merge(right, on="data_time", suffixes=("_left", "_right"))
    spread = joined["close_left"] - joined["close_right"]
    return pd.DataFrame({
        "data_time": joined["data_time"],
        "series_tag": params["output_tag"],
        "spread": spread,
        "rolling_spread": spread.rolling(int(params["window"]), min_periods=1).mean(),
    })
`
	createFactorRsp, err := factor.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId: factorID, Name: factorName, SourceCode: sourceCode,
		InputColumns: []string{"close"},
		Outputs:      []string{"spread", "rolling_spread"},
		ParamsJson: `{"left_tag":"venue:binance","right_tag":"venue:okx",` +
			`"output_tag":"venue_pair:binance-okx","window":2}`,
		LookbackPeriods: 2, Status: "disabled",
	}})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.CreateFactor", createFactorRsp.GetRetInfo())
	factorCreated = true
	bindRsp, err := factor.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		BindingId: bindingID, FactorId: factorID, SpaceId: spaceID,
		SourceDataset: sourceID, Freq: freq, SubjectMode: "all", SubjectsJson: "[]",
		TargetDataset: targetID, Status: "enabled",
	}})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.UpsertBinding", bindRsp.GetRetInfo())
	bindingCreated = true
	enableRsp, err := factor.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
		FactorId: factorID,
		Status:   "enabled",
	})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.SetFactorStatus", enableRsp.GetRetInfo())
	targetColumnsRsp, err := metadata.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{
		AuthInfo: auth, SpaceId: spaceID, DatasetId: targetID,
		Page: &commonpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	requireStorageRet(t, "ListDatasetColumns(target)", targetColumnsRsp.GetRetInfo())
	require.ElementsMatch(t, []string{"spread", "rolling_spread"}, datasetColumnNames(targetColumnsRsp.GetColumns()))

	viewColumns := []*storagepb.ViewColumn{
		viewColumn(spaceID, viewID, targetID, "spread", "价差", 10),
		viewColumn(spaceID, viewID, targetID, "rolling_spread", "滚动价差", 20),
	}
	createViewRsp, err := metadata.CreateView(ctx, &storagepb.CreateViewReq{
		AuthInfo: auth,
		View: &storagepb.View{
			SpaceId: spaceID, ViewId: viewID, Name: "因视" + displaySuffix,
			PrimaryDatasetId: targetID, DatasetIds: []string{targetID},
			GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Engine: "duckdb",
			FilterJson: `{"freq":"1m"}`, KeepDuration: "24h", Status: "active", Columns: viewColumns,
		},
	})
	requireStorageOK(t, "CreateView", createViewRsp, err)
	waitForViewReady(t, ctx, metadata, auth, spaceID, viewID)
	waitForViewQueryable(t, ctx, view, viewAuth, spaceID, viewID, targetID, subjectID, freq, first, end)

	runDeployedFactor(t, ctx, deployRoot, factorID, spaceID, sourceID, subjectID, freq, first, end)
	assertPrimaryFactorRows(t, ctx, primary, auth, spaceID, targetID, subjectID, freq,
		[]time.Time{first, second, third}, []float64{1, 2, 3}, []float64{1, 1.5, 2.5}, -1)
	rows := waitForViewRows(t, ctx, view, viewAuth, spaceID, viewID, targetID, subjectID, freq,
		[]time.Time{first, second, third}, end, []float64{1, 2, 3}, []float64{1, 1.5, 2.5}, -1)
	assertFactorRows(t, rows, []time.Time{first, second, third},
		[]float64{1, 2, 3}, []float64{1, 1.5, 2.5}, -1)

	// Correcting the first source bar emits DatasetRowsUpserted@2. The realtime
	// scheduler must expand the affected range by lookback-1 actual periods, so
	// the second rolling value changes even though its source bar was untouched.
	correctionRsp, err := primary.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{
		AuthInfo: auth, SourceEventId: "factor-storage-e2e-correction-" + suffix,
		Rows: []*storagepb.RowFieldUpsert{
			inputRow(spaceID, sourceID, subjectID, freq, first, "venue:binance", 103),
		},
	})
	requireStorageOK(t, "PrimaryStore.UpsertFields(correction)", correctionRsp, err)
	rows = waitForViewRows(t, ctx, view, viewAuth, spaceID, viewID, targetID, subjectID, freq,
		[]time.Time{first, second, third}, end, []float64{3, 2, 3}, []float64{3, 2.5, 2.5}, -1)
	assertFactorRows(t, rows, []time.Time{first, second, third},
		[]float64{3, 2, 3}, []float64{3, 2.5, 2.5}, -1)

	nullSource := `import pandas as pd

def compute(df, params):
    left = df[df["series_tag"] == params["left_tag"]][["data_time", "close"]]
    right = df[df["series_tag"] == params["right_tag"]][["data_time", "close"]]
    joined = left.merge(right, on="data_time", suffixes=("_left", "_right"))
    spread = joined["close_left"] - joined["close_right"]
    rolling = spread.rolling(int(params["window"]), min_periods=1).mean()
    spread.iloc[-1] = None
    rolling.iloc[-1] = None
    return pd.DataFrame({
        "data_time": joined["data_time"], "series_tag": params["output_tag"],
        "spread": spread, "rolling_spread": rolling,
    })
`
	disableRsp, err := factor.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
		FactorId: factorID, Status: "disabled",
	})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.SetFactorStatus(disable)", disableRsp.GetRetInfo())
	updateRsp, err := factor.UpdateFactor(ctx, &factorpb.UpdateFactorReq{
		FactorId: factorID,
		Factor: &factorpb.FactorDef{
			FactorId: factorID, Name: factorName, SourceCode: nullSource,
			InputColumns: []string{"close"},
			Outputs:      []string{"spread", "rolling_spread"},
			ParamsJson: `{"left_tag":"venue:binance","right_tag":"venue:okx",` +
				`"output_tag":"venue_pair:binance-okx","window":2}`,
			LookbackPeriods: 2, Status: "disabled",
		},
	})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.UpdateFactor", updateRsp.GetRetInfo())
	enableNullRsp, err := factor.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
		FactorId: factorID, Status: "enabled",
	})
	require.NoError(t, err)
	requireStorageRet(t, "FactorMgr.SetFactorStatus(re-enable)", enableNullRsp.GetRetInfo())
	runDeployedFactor(t, ctx, deployRoot, factorID, spaceID, sourceID, subjectID, freq, first, end)
	rows = waitForViewRows(t, ctx, view, viewAuth, spaceID, viewID, targetID, subjectID, freq,
		[]time.Time{first, second, third}, end, []float64{3, 2, 0}, []float64{3, 2.5, 0}, 2)
	assertFactorRows(t, rows, []time.Time{first, second, third},
		[]float64{3, 2, 0}, []float64{3, 2.5, 0}, 2)
}

func assertPrimaryFactorRows(
	t *testing.T,
	ctx context.Context,
	primary storagepb.PrimaryStoreClientProxy,
	auth *commonpb.AuthInfo,
	spaceID, datasetID, subjectID, freq string,
	times []time.Time,
	spreads, rolling []float64,
	nullIndex int,
) {
	t.Helper()
	keys := make([]*storagepb.RowKey, 0, len(times))
	for _, at := range times {
		keys = append(keys, &storagepb.RowKey{
			SpaceId: spaceID, DatasetId: datasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: freq, DataTime: at.Format(time.RFC3339Nano),
				SeriesTag: "venue_pair:binance-okx",
			}},
		})
	}
	var lastRows []*storagepb.RowFieldValues
	ok := assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		rsp, err := primary.ReadFields(ctx, &storagepb.PrimaryReadFieldsReq{
			AuthInfo: auth, Keys: keys, FieldIds: []string{"spread", "rolling_spread"},
		})
		require.NoError(collect, err)
		require.Equal(collect, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		require.Len(collect, rsp.GetRows(), len(times))
		lastRows = rsp.GetRows()
		rows := make([]*storagepb.TimeSeriesRow, 0, len(rsp.GetRows()))
		for _, row := range rsp.GetRows() {
			rows = append(rows, &storagepb.TimeSeriesRow{
				Key: &storagepb.TimeSeriesKey{
					SpaceId: row.GetKey().GetSpaceId(), DatasetId: row.GetKey().GetDatasetId(),
					SubjectId: row.GetKey().GetTimeSeries().GetSubjectId(),
					Freq:      row.GetKey().GetTimeSeries().GetFreq(),
					DataTime:  row.GetKey().GetTimeSeries().GetDataTime(),
					SeriesTag: row.GetKey().GetTimeSeries().GetSeriesTag(),
				},
				Fields: row.GetFields(),
			})
		}
		require.True(collect, factorRowsHaveExpectedValues(rows, times, spreads, rolling, nullIndex))
	}, 10*time.Second, 100*time.Millisecond)
	require.True(t, ok, "last Primary rows: %v", lastRows)
	t.Log("real Storage PrimaryStore.ReadFields returned exact output identities and values")
}

type retResponse interface {
	GetRetInfo() *commonpb.RetInfo
}

func TestRequiredEnvPreservesNonEmptyValue(t *testing.T) {
	const name = "MOOX_FACTOR_STORAGE_E2E_REQUIRED_ENV_TEST"
	t.Setenv(name, " secret with spaces ")
	require.Equal(t, " secret with spaces ", requiredEnv(t, name))
}

func TestFactorEventIncompleteRetriesBeforePython(t *testing.T) {
	factorsDir := t.TempDir()
	factorPath := filepath.Join(factorsDir, "VenueSpread.py")
	source := []byte(`import pandas as pd

def compute(df, params):
    left = df[df["series_tag"] == params["left_tag"]][["data_time", "close"]]
    right = df[df["series_tag"] == params["right_tag"]][["data_time", "close"]]
    joined = left.merge(right, on="data_time", suffixes=("_left", "_right"))
    return pd.DataFrame({
        "data_time": joined["data_time"],
        "series_tag": params["output_tag"],
        "spread": joined["close_left"] - joined["close_right"],
    })
`)
	require.NoError(t, os.WriteFile(factorPath, source, 0o600))
	hash := sha256.Sum256(source)
	at := time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC)
	task, err := scheduler.BuildTask(scheduler.TaskScope{
		TaskID: "incomplete-retry-e2e", TriggerType: "event", SpaceID: "quant",
		SourceDataset: "prices", TargetDataset: "spread", SubjectID: "BTC-USDT",
		Freq: "1m", StartTime: at, EndTime: at.Add(time.Nanosecond),
	}, domain.FactorDef{
		FactorID: "venue-spread", Name: "VenueSpread",
		SourceHash: hex.EncodeToString(hash[:]), SourcePath: factorPath,
		InputColumns: []string{"close"}, Outputs: []string{"spread"},
		ParamsJSON: `{"left_tag":"venue:binance","right_tag":"venue:okx",` +
			`"output_tag":"venue_pair:binance-okx"}`,
		LookbackPeriods: 1, Status: domain.FactorStatusEnabled,
	}, factorsDir)
	require.NoError(t, err)
	python, err := engine.NewPythonExecutor(context.Background(), 1, process.Config{
		PythonBin: "python3", WorkerPath: filepath.Join("..", "pyworker", "worker.py"),
		Args: []string{"--factors-dir", factorsDir}, Limits: process.DefaultLimits(),
	})
	require.NoError(t, err)
	defer python.Close()
	storage := &incompleteThenCompleteStorage{at: at}
	service := scheduler.NewService(scheduler.Config{
		Workers: 1, QueueCapacity: 1, EventReadRetry: 2,
		EventReadRetryInterval: time.Millisecond,
	}, storage, python)
	require.NoError(t, service.Run(context.Background(), task))
	require.Equal(t, 2, storage.reads, "event task must reread after complete=false")
	require.Equal(t, 1, storage.writes, "incomplete cohort must never reach Python/writeback")
	require.Len(t, storage.result.Rows, 1)
	require.Equal(t, "venue_pair:binance-okx", storage.result.Rows[0].SeriesTag)
	require.InDelta(t, 1.0, storage.result.Rows[0].Values["spread"], 1e-12)
}

type incompleteThenCompleteStorage struct {
	mu     sync.Mutex
	at     time.Time
	reads  int
	writes int
	result *engine.FactorResult
}

func (s *incompleteThenCompleteStorage) ReadRangeChunk(
	_ context.Context,
	_ storageio.WindowKey,
	_, _ time.Time,
	_, _ int,
	columns []string,
) (*storageio.RangeChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			Columns: columns, Rows: [][]any{{101.0}, {100.0}},
			DataTimes:  []time.Time{s.at, s.at},
			SeriesTags: []string{"venue:binance", "venue:okx"},
		},
		TargetPeriods: []time.Time{s.at},
		Complete:      s.reads > 1,
	}, nil
}

func (s *incompleteThenCompleteStorage) ExpandEndByPeriods(
	_ context.Context,
	_ storageio.WindowKey,
	end time.Time,
	_ int,
) (time.Time, error) {
	return end, nil
}

func (s *incompleteThenCompleteStorage) WriteFactorPatch(
	_ context.Context,
	_ *engine.FactorTask,
	result *engine.FactorResult,
) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	s.result = result
	return uint64(len(result.Rows)), nil
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
	value := os.Getenv(name)
	require.NotEmpty(t, strings.TrimSpace(value), "%s is required", name)
	return value
}

func cleanupDatasetBuckets(
	t *testing.T,
	ctx context.Context,
	dataNode storagepb.DataNodeRuntimeClientProxy,
	auth *commonpb.AuthInfo,
	nodeID, spaceID, datasetID string,
) {
	t.Helper()
	rsp, err := dataNode.CleanupExpiredBuckets(ctx, &storagepb.CleanupExpiredBucketsReq{
		AuthInfo:          auth,
		NodeId:            nodeID,
		SpaceId:           spaceID,
		DatasetId:         datasetID,
		BeforeBucketStart: "9999-12-31T23:59:59Z",
	})
	if err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		reportCleanupFailure(t, "DataNode buckets "+spaceID+"/"+datasetID, rsp, err)
		return
	}
	t.Logf("cleanup DataNode buckets %s/%s succeeded (%d buckets)", spaceID, datasetID, rsp.GetDeletedBuckets())
}

func assertFactorArtifactsRemoved(t *testing.T, deployRoot, factorName string) {
	t.Helper()
	factorsDir := filepath.Join(deployRoot, "factor", "factors")
	for _, path := range []string{
		filepath.Join(factorsDir, factorName+".py"),
		filepath.Join(factorsDir, ".versions", "factor", factorName),
	} {
		if _, err := os.Lstat(path); err == nil {
			reportCleanupFailure(t, "factor artifact "+path, "still exists", nil)
		} else if !os.IsNotExist(err) {
			reportCleanupFailure(t, "inspect factor artifact "+path, nil, err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(factorsDir, "__pycache__", factorName+".*.pyc"))
	if err != nil {
		reportCleanupFailure(t, "inspect factor bytecode "+factorName, nil, err)
		return
	}
	if len(matches) != 0 {
		reportCleanupFailure(t, "factor bytecode "+factorName, matches, nil)
	}
	for _, parent := range []string{
		factorsDir,
		filepath.Join(factorsDir, ".versions", "factor"),
		filepath.Join(factorsDir, "__pycache__"),
	} {
		staged, globErr := filepath.Glob(filepath.Join(parent, ".delete-"+factorName+"-*"))
		if globErr != nil {
			reportCleanupFailure(t, "inspect staged factor artifacts "+factorName, nil, globErr)
			continue
		}
		if len(staged) != 0 {
			reportCleanupFailure(t, "staged factor artifacts "+factorName, staged, nil)
		}
	}
}

func reportCleanupFailure(t *testing.T, resource string, rsp any, err error) {
	t.Helper()
	if t.Failed() {
		t.Logf("cleanup %s failed after primary failure: rsp=%v err=%v", resource, rsp, err)
		return
	}
	t.Errorf("cleanup %s failed: rsp=%v err=%v", resource, rsp, err)
}

func inputRow(spaceID, datasetID, subjectID, freq string, at time.Time, seriesTag string, close float64) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{
			SpaceId: spaceID, DatasetId: datasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: freq, DataTime: at.Format(time.RFC3339Nano),
				SeriesTag: seriesTag,
			}},
		},
		Fields: []*storagepb.FieldValue{doubleValue("close", close)},
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

func waitForViewQueryable(
	t *testing.T,
	ctx context.Context,
	view storagepb.DataViewClientProxy,
	auth *commonpb.AuthInfo,
	spaceID, viewID, datasetID, subjectID, freq string,
	start, end time.Time,
) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		rsp, err := view.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: spaceID, ViewId: viewID,
			Selectors: []*storagepb.TimeSeriesSelector{{
				SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq,
			}},
			TimeRange: &storagepb.TimeRange{
				StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano),
			},
			Page: &commonpb.Page{Page: 1, Size: 1},
		})
		require.NoError(collect, err)
		require.Equal(collect, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}, 10*time.Second, 100*time.Millisecond)
	t.Log("real Storage View runtime accepted a public query")
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
	times []time.Time,
	end time.Time,
	spreads, rolling []float64,
	nullIndex int,
) []*storagepb.TimeSeriesRow {
	t.Helper()
	var rows []*storagepb.TimeSeriesRow
	var lastRows []*storagepb.TimeSeriesRow
	ok := assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		rsp, err := view.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: spaceID, ViewId: viewID,
			Selectors: []*storagepb.TimeSeriesSelector{{
				SpaceId: spaceID, DatasetId: targetID, SubjectId: subjectID, Freq: freq,
			}},
			TimeRange: &storagepb.TimeRange{
				StartTime: times[0].Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano),
			},
			Sorts: []*storagepb.SortSpec{{FieldName: "data_time"}},
			Page:  &commonpb.Page{Page: 1, Size: 10},
		})
		require.NoError(collect, err)
		require.Equal(collect, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
		lastRows = rsp.GetRows()
		require.Len(collect, rsp.GetRows(), len(times))
		require.True(collect, factorRowsHaveExpectedValues(rsp.GetRows(), times, spreads, rolling, nullIndex),
			"View has not applied the expected factor patch yet")
		if factorRowsHaveExpectedValues(rsp.GetRows(), times, spreads, rolling, nullIndex) {
			rows = rsp.GetRows()
		}
	}, 30*time.Second, 200*time.Millisecond)
	require.True(t, ok, "last factor View rows: %v", lastRows)
	t.Log("real Storage DataView.QueryTimeSeriesRows returned all expected factor rows")
	return rows
}

func datasetColumnNames(columns []*storagepb.DatasetColumn) []string {
	out := make([]string, len(columns))
	for i, column := range columns {
		out[i] = column.GetColumnName()
	}
	return out
}

func assertFactorRows(
	t *testing.T,
	rows []*storagepb.TimeSeriesRow,
	times []time.Time,
	spreads, rolling []float64,
	nullIndex int,
) {
	t.Helper()
	require.Len(t, rows, len(times))
	for i, row := range rows {
		require.Equal(t, times[i].Format(time.RFC3339Nano), row.GetKey().GetDataTime())
		require.Equal(t, "venue_pair:binance-okx", row.GetKey().GetSeriesTag())
		values := rowValues(row)
		if i == nullIndex {
			require.Empty(t, rowFieldIDs(row), "cleared null outputs must not expose extra fields")
			require.Nil(t, values["spread"], "explicit null must clear old spread")
			require.Nil(t, values["rolling_spread"], "explicit null must clear old rolling spread")
			continue
		}
		require.ElementsMatch(t, []string{"spread", "rolling_spread"}, rowFieldIDs(row))
		require.InDelta(t, spreads[i], values["spread"].GetDoubleValue(), 1e-12)
		require.InDelta(t, rolling[i], values["rolling_spread"].GetDoubleValue(), 1e-12)
	}
}

func rowFieldIDs(row *storagepb.TimeSeriesRow) []string {
	ids := make([]string, 0, len(row.GetFields()))
	for _, field := range row.GetFields() {
		id := field.GetFieldId()
		if dot := strings.LastIndexByte(id, '.'); dot >= 0 {
			id = id[dot+1:]
		}
		ids = append(ids, id)
	}
	return ids
}

func factorRowsHaveExpectedValues(
	rows []*storagepb.TimeSeriesRow,
	times []time.Time,
	spreads, rolling []float64,
	nullIndex int,
) bool {
	if len(rows) != len(times) || len(spreads) != len(times) || len(rolling) != len(times) {
		return false
	}
	for i, row := range rows {
		if row.GetKey().GetDataTime() != times[i].Format(time.RFC3339Nano) ||
			row.GetKey().GetSeriesTag() != "venue_pair:binance-okx" {
			return false
		}
		values := rowValues(row)
		if i == nullIndex {
			if values["spread"] != nil || values["rolling_spread"] != nil {
				return false
			}
			continue
		}
		if values["spread"] == nil || values["rolling_spread"] == nil ||
			!closeEnough(values["spread"].GetDoubleValue(), spreads[i]) ||
			!closeEnough(values["rolling_spread"].GetDoubleValue(), rolling[i]) {
			return false
		}
	}
	return true
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
