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
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	eventstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	storage := storageio.NewClientWithCredentials(gatewayTarget, gatewayNodeID, credentials, auth).WithViewAuth(viewAuth)

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
	factorID := "venue_spread_" + suffix
	factorName := "VenueSpread_" + suffix
	bindingID := "bind-spread-" + suffix
	secondFactorID := "venue_midpoint_" + suffix
	secondFactorName := "VenueMidpoint_" + suffix
	secondBindingID := "bind-midpoint-" + suffix
	subjectID := "fund-" + suffix
	secondSubjectID := "fund-alt-" + suffix
	subjectIDs := []string{subjectID, secondSubjectID}
	const freq = "1m"
	first := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	third := second.Add(time.Minute)
	end := third.Add(time.Nanosecond)
	var spaceCreated bool
	var createdFactors []struct{ id, name string }
	var createdBindings []string
	var resultDatasetID, resultViewID string

	t.Cleanup(func() {
		// Result-view schema cleanup waits for the storage reconciler to activate a
		// new revision (normally up to one reconcile tick). Keep teardown separate
		// from the assertion context and allow that asynchronous handoff to finish.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		for i := len(createdBindings) - 1; i >= 0; i-- {
			id := createdBindings[i]
			if rsp, err := factor.DeleteBinding(cleanupCtx, &factorpb.DeleteBindingReq{BindingId: id}); err != nil ||
				rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
				reportCleanupFailure(t, "binding "+id, rsp, err)
			}
		}
		for i := len(createdFactors) - 1; i >= 0; i-- {
			created := createdFactors[i]
			if rsp, err := factor.DeleteFactor(cleanupCtx, &factorpb.DeleteFactorReq{
				FactorId: created.id,
			}); err != nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
				reportCleanupFailure(t, "factor "+created.id, rsp, err)
			} else {
				assertFactorArtifactsRemoved(t, deployRoot, created.name)
			}
		}
		if spaceCreated {
			cleanupDatasetBuckets(t, cleanupCtx, dataNode, dataNodeAuth, dataNodeID, spaceID, sourceID)
			cleanupDataset := resultDatasetID
			if cleanupDataset == "" {
				cleanupDataset = targetID
			}
			cleanupDatasetBuckets(t, cleanupCtx, dataNode, dataNodeAuth, dataNodeID, spaceID, cleanupDataset)
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
	for index, currentSubjectID := range subjectIDs {
		subjectRsp, subjectErr := metadata.UpsertSubject(ctx, &storagepb.UpsertSubjectReq{
			AuthInfo: auth,
			Subject: &storagepb.Subject{
				SpaceId: spaceID, SubjectId: currentSubjectID, SubjectType: "custom",
				Name: fmt.Sprintf("组合%s-%d", displaySuffix, index+1), Timezone: "UTC", Status: "active",
			},
		})
		requireStorageOK(t, "UpsertSubject "+currentSubjectID, subjectRsp, subjectErr)
	}
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
			Freqs: []string{freq}, DataNodeId: dataNodeID, KeepDuration: "0", Status: "disabled",
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
	for _, currentSubjectID := range subjectIDs {
		subjectBindingRsp, bindSubjectErr := metadata.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{
			AuthInfo: auth,
			DatasetSubject: &storagepb.DatasetSubject{
				SpaceId: spaceID, DatasetId: sourceID, SubjectId: currentSubjectID,
				SubjectRole: "normal", Status: "active",
			},
		})
		requireStorageOK(t, "BindDatasetSubject "+currentSubjectID, subjectBindingRsp, bindSubjectErr)
	}
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
			FilterJson: `{"freq":"1m"}`, KeepDuration: "0", Status: "active",
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
			inputRow(spaceID, sourceID, secondSubjectID, freq, first, "venue:binance", 201),
			inputRow(spaceID, sourceID, secondSubjectID, freq, first, "venue:okx", 200),
			inputRow(spaceID, sourceID, secondSubjectID, freq, second, "venue:binance", 204),
			inputRow(spaceID, sourceID, secondSubjectID, freq, second, "venue:okx", 202),
			inputRow(spaceID, sourceID, secondSubjectID, freq, third, "venue:binance", 208),
			inputRow(spaceID, sourceID, secondSubjectID, freq, third, "venue:okx", 205),
		},
	})
	requireStorageOK(t, "PrimaryStore.UpsertFields", writeRsp, err)
	var sourceChunk *storageio.RangeChunk
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		chunk, readErr := storage.ReadRangeChunk(ctx, storageio.WindowKey{
			SpaceID: spaceID, SourceViewID: sourceViewID, SourceDataset: sourceID, SubjectID: subjectID, Freq: freq,
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
	require.Equal(t, [][]any{{101.0}, {100.0}, {104.0}, {102.0}, {108.0}, {105.0}}, sourceChunk.Frame.Rows)

	spreadSource := `import pandas as pd

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
	midpointSource := `import pandas as pd

def compute(df, params):
    left = df[df["series_tag"] == params["left_tag"]][["data_time", "close"]]
    right = df[df["series_tag"] == params["right_tag"]][["data_time", "close"]]
    joined = left.merge(right, on="data_time", suffixes=("_left", "_right"))
    midpoint = (joined["close_left"] + joined["close_right"]) / 2
    return pd.DataFrame({
        "data_time": joined["data_time"],
        "series_tag": params["output_tag"],
        "midpoint": midpoint,
        "rolling_midpoint": midpoint.rolling(int(params["window"]), min_periods=1).mean(),
    })
`
	factorDefs := []*factorpb.FactorDef{
		{
			FactorId: factorID, Name: factorName, SourceCode: spreadSource,
			InputColumns: []string{"close"}, Outputs: []string{"spread", "rolling_spread"},
			ParamsJson: `{"left_tag":"venue:binance","right_tag":"venue:okx",` +
				`"output_tag":"venue_pair:binance-okx","window":2}`,
			LookbackPeriods: 2, Status: "disabled",
		},
		{
			FactorId: secondFactorID, Name: secondFactorName, SourceCode: midpointSource,
			InputColumns: []string{"close"}, Outputs: []string{"midpoint", "rolling_midpoint"},
			ParamsJson: `{"left_tag":"venue:binance","right_tag":"venue:okx",` +
				`"output_tag":"venue_pair:binance-okx","window":2}`,
			LookbackPeriods: 2, Status: "disabled",
		},
	}
	bindingIDs := []string{bindingID, secondBindingID}
	for i, factorDef := range factorDefs {
		createFactorRsp, createErr := factor.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: factorDef})
		require.NoError(t, createErr)
		requireStorageRet(t, "FactorMgr.CreateFactor "+factorDef.GetFactorId(), createFactorRsp.GetRetInfo())
		createdFactors = append(createdFactors, struct{ id, name string }{factorDef.GetFactorId(), factorDef.GetName()})

		bindRsp, bindErr := factor.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
			BindingId: bindingIDs[i], FactorId: factorDef.GetFactorId(), SpaceId: spaceID,
			SourceViewId: sourceViewID, SourceDataset: sourceID, Freq: freq, SubjectMode: "all", SubjectsJson: "[]",
			TargetDataset: targetID, Status: "enabled",
		}})
		require.NoError(t, bindErr)
		requireStorageRet(t, "FactorMgr.UpsertBinding "+bindingIDs[i], bindRsp.GetRetInfo())
		require.NotNil(t, bindRsp.GetBinding())
		if i == 0 {
			resultDatasetID = bindRsp.GetBinding().GetResultDatasetId()
			resultViewID = bindRsp.GetBinding().GetResultViewId()
			require.NotEmpty(t, resultDatasetID)
			require.NotEmpty(t, resultViewID)
		} else {
			require.Equal(t, resultDatasetID, bindRsp.GetBinding().GetResultDatasetId())
			require.Equal(t, resultViewID, bindRsp.GetBinding().GetResultViewId())
		}
		createdBindings = append(createdBindings, bindingIDs[i])

		enableRsp, enableErr := factor.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{
			FactorId: factorDef.GetFactorId(), Status: "enabled",
		})
		require.NoError(t, enableErr)
		requireStorageRet(t, "FactorMgr.SetFactorStatus "+factorDef.GetFactorId(), enableRsp.GetRetInfo())
	}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		bindingsRsp, listErr := factor.ListBindings(ctx, &factorpb.ListBindingsReq{
			SpaceId: spaceID, SourceViewId: sourceViewID, Freq: freq,
			Page: &commonpb.Page{Page: 1, Size: 20},
		})
		assert.NoError(collect, listErr)
		if listErr != nil {
			return
		}
		assert.Equal(collect, commonpb.ErrorCode_SUCCESS, bindingsRsp.GetRetInfo().GetCode(), bindingsRsp.GetRetInfo().GetMsg())
		statuses := make(map[string]string)
		for _, candidate := range bindingsRsp.GetBindings() {
			statuses[candidate.GetBindingId()] = candidate.GetStatus()
		}
		for _, id := range bindingIDs {
			assert.Equal(collect, "enabled", statuses[id], "binding_id=%s", id)
		}
	}, 60*time.Second, 250*time.Millisecond, "factor bindings did not become executable")
	var targetColumnsRsp *storagepb.ListDatasetColumnsRsp
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var listErr error
		targetColumnsRsp, listErr = metadata.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: resultDatasetID,
			Page: &commonpb.Page{Page: 1, Size: 10},
		})
		assert.NoError(collect, listErr)
		if listErr == nil {
			assert.Equal(collect, commonpb.ErrorCode_SUCCESS, targetColumnsRsp.GetRetInfo().GetCode(), targetColumnsRsp.GetRetInfo().GetMsg())
			assert.ElementsMatch(collect, []string{
				factorID + "__spread", factorID + "__rolling_spread",
				secondFactorID + "__midpoint", secondFactorID + "__rolling_midpoint",
			}, datasetColumnNames(targetColumnsRsp.GetColumns()))
		}
	}, 30*time.Second, 250*time.Millisecond, "factor result columns were not synchronized")
	require.NotNil(t, targetColumnsRsp)

	waitForViewReady(t, ctx, metadata, auth, spaceID, resultViewID)
	waitForViewQueryable(t, ctx, view, viewAuth, spaceID, resultViewID, resultDatasetID, subjectID, freq, first, end)
	// Use the deployment-wide EventBus credentials/TLS settings. The local
	// endpoint is only a fallback for process-local tests; real deployments
	// require the same authenticated connection as Factor itself.
	eventCfg := jetstream.ConfigFromEnv([]string{"nats://127.0.0.1:4222"}, "factor-storage-e2e-"+suffix)
	// Deployment credential files are MooX YAML (username/token/ca_file), not
	// nats.go .creds files. Resolve them through the shared loader so relative
	// CA paths and token authentication behave exactly like Factor itself.
	if credentialFile := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_NATS_CREDENTIALS")); credentialFile != "" {
		eventCfg.Credentials = ""
		require.NoError(t, eventCfg.ApplyCredentialFile(credentialFile))
	}
	eventClient, err := jetstream.Connect(ctx, eventCfg)
	require.NoError(t, err)
	defer eventClient.Close()
	eventRegistry, err := events.DefaultRegistry()
	require.NoError(t, err)
	readyConsumer, err := events.NewConsumer(ctx, eventClient, eventRegistry, events.ConsumerConfig{
		// The Factor EventBus role grants this dedicated durable for the
		// integration assertion; the production source-ready durable remains
		// owned by the Factor consumer.
		Name: "factor_view_ready_e2e", Event: events.ViewFactorPeriodReady,
		AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 16,
		FetchMaxWait: 500 * time.Millisecond, DeliverPolicy: nats.DeliverAllPolicy,
	})
	require.NoError(t, err)
	defer readyConsumer.Close()

	// Drive the production event chain instead of jumping straight to the
	// run-once RPC: rows are already committed, then Collector-style period
	// markers are appended. Storage View must publish source-ready, Factor's
	// durable consumer must compute, and the result marker must be queryable.
	collectorAuth := &commonpb.AuthInfo{AppId: "moox-collector", Operator: "factor-storage-e2e"}
	collectorAuth.AppKey = mooxsecurity.HMACSHA256Hex(
		requiredEnv(t, "MOOX_STORAGE_PRIMARY_AUTH_SECRET"),
		[]byte(collectorAuth.GetAppId()),
	)
	reportRsp, reportErr := primary.ReportDatasetPeriodCollected(ctx, &storagepb.ReportDatasetPeriodCollectedReq{
		AuthInfo: collectorAuth, SpaceId: spaceID,
		Marker: &storagepb.DatasetPeriodCollectedMarker{
			DatasetId: sourceID, Frequency: freq, PeriodTime: third.Unix(), Status: "complete",
			SubjectIds: subjectIDs, CollectedAt: timestamppb.New(time.Now().UTC()),
		},
	})
	requireStorageOK(t, "PrimaryStore.ReportDatasetPeriodCollected", reportRsp, reportErr)
	thirdSourceReadyID := sourceReadyEventID(spaceID, sourceViewID, freq, third.Unix())
	var computed *storagepb.FactorPeriodComputedMarker
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		computedRsp, computedErr := primary.GetFactorPeriodComputed(ctx, &storagepb.GetFactorPeriodComputedReq{
			AuthInfo: auth, SpaceId: spaceID, SourceViewId: sourceViewID,
			TriggerEventId: thirdSourceReadyID, PeriodTime: third.Unix(),
		})
		assert.NoError(collect, computedErr)
		if computedErr != nil || computedRsp == nil {
			return
		}
		assert.Equal(collect, commonpb.ErrorCode_SUCCESS, computedRsp.GetRetInfo().GetCode(), computedRsp.GetRetInfo().GetMsg())
		if computedRsp.GetFound() {
			computed = computedRsp.GetMarker()
		} else {
			t.Logf("factor marker still pending: source_ready_event_id=%s", thirdSourceReadyID)
			assert.True(collect, computedRsp.GetFound(), "factor marker is not persisted yet")
		}
	}, 180*time.Second, 250*time.Millisecond, "source-ready did not drive Factor durable computation")
	require.NotNil(t, computed)
	require.Equal(t, "complete", computed.GetStatus())
	assertCompleteBindingStates(t, computed.GetBindings(), map[string]string{
		bindingID: factorID, secondBindingID: secondFactorID,
	})
	t.Logf("durable factor marker: status=%s bindings=%v", computed.GetStatus(), computed.GetBindings())
	var finalReady *eventstoragepb.ViewFactorPeriodReady
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 2*time.Second)
		defer fetchCancel()
		deliveries, fetchErr := readyConsumer.Fetch(fetchCtx, 16)
		if fetchErr != nil && len(deliveries) == 0 {
			assert.NoError(collect, fetchErr)
			return
		}
		for _, delivery := range deliveries {
			decoded := events.DecodeDelivery(eventRegistry, delivery)
			if decoded.Err != nil {
				_ = delivery.Ack(fetchCtx)
				continue
			}
			ready, ok := decoded.Payload.(*eventstoragepb.ViewFactorPeriodReady)
			if ok && ready.GetResultViewId() == resultViewID && ready.GetFrequency() == freq && ready.GetPeriodTime() == third.Unix() {
				finalReady = ready
			}
			_ = delivery.Ack(fetchCtx)
		}
		if finalReady == nil {
			assert.Fail(collect, "result view ready event not found", "view=%s period=%d", resultViewID, third.Unix())
		}
	}, 60*time.Second, 250*time.Millisecond, "result View did not publish ViewFactorPeriodReady")
	require.NotNil(t, finalReady)
	require.Equal(t, "complete", finalReady.GetStatus())
	assertEventCompleteBindingStates(t, finalReady.GetBindings(), map[string]string{
		bindingID: factorID, secondBindingID: secondFactorID,
	})

	// Assert the durable path before invoking the manual run-once helper. This
	// prevents a successful CLI recalculation from masking a broken source-ready
	// consumer or result writeback. Query Result View only after receiving final
	// ready, then require the rows on that first query to prove ready is not early.
	expectedOutputs := []factorOutputExpectation{
		{fieldID: factorID + "__spread", values: []float64{3}},
		{fieldID: factorID + "__rolling_spread", values: []float64{2.5}},
		{fieldID: secondFactorID + "__midpoint", values: []float64{106.5}},
		{fieldID: secondFactorID + "__rolling_midpoint", values: []float64{104.75}},
	}
	assertPrimaryFactorRows(t, ctx, primary, auth, spaceID, resultDatasetID, subjectID, freq,
		[]time.Time{third}, "venue_pair:binance-okx", expectedOutputs)
	rows := queryViewRowsOnce(t, ctx, view, viewAuth, spaceID, resultViewID, resultDatasetID, subjectID, freq,
		[]time.Time{third}, end, "venue_pair:binance-okx", expectedOutputs)
	assertFactorRows(t, rows, []time.Time{third}, "venue_pair:binance-okx", expectedOutputs)
	secondExpectedOutputs := []factorOutputExpectation{
		{fieldID: factorID + "__spread", values: []float64{3}},
		{fieldID: factorID + "__rolling_spread", values: []float64{2.5}},
		{fieldID: secondFactorID + "__midpoint", values: []float64{206.5}},
		{fieldID: secondFactorID + "__rolling_midpoint", values: []float64{204.75}},
	}
	assertPrimaryFactorRows(t, ctx, primary, auth, spaceID, resultDatasetID, secondSubjectID, freq,
		[]time.Time{third}, "venue_pair:binance-okx", secondExpectedOutputs)
	secondRows := queryViewRowsOnce(t, ctx, view, viewAuth, spaceID, resultViewID, resultDatasetID,
		secondSubjectID, freq, []time.Time{third}, end, "venue_pair:binance-okx", secondExpectedOutputs)
	assertFactorRows(t, secondRows, []time.Time{third}, "venue_pair:binance-okx", secondExpectedOutputs)

	// Keep the explicit CLI path as a second, independent smoke check after the
	// event-driven assertions have already passed.
	runDeployedFactor(t, ctx, deployRoot, []string{factorID, secondFactorID}, spaceID, sourceID, sourceViewID, subjectID, freq, first, end)

}

type factorOutputExpectation struct {
	fieldID string
	values  []float64
}

func assertPrimaryFactorRows(
	t *testing.T,
	ctx context.Context,
	primary storagepb.PrimaryStoreClientProxy,
	auth *commonpb.AuthInfo,
	spaceID, datasetID, subjectID, freq string,
	times []time.Time,
	seriesTag string,
	outputs []factorOutputExpectation,
) {
	t.Helper()
	keys := make([]*storagepb.RowKey, 0, len(times))
	for _, at := range times {
		keys = append(keys, &storagepb.RowKey{
			SpaceId: spaceID, DatasetId: datasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: subjectID, Freq: freq, DataTime: at.Format(time.RFC3339Nano),
				SeriesTag: seriesTag,
			}},
		})
	}
	fieldIDs := make([]string, 0, len(outputs))
	for _, output := range outputs {
		fieldIDs = append(fieldIDs, output.fieldID)
	}
	var lastRows []*storagepb.RowFieldValues
	ok := assert.EventuallyWithT(t, func(collect *assert.CollectT) {
		rsp, err := primary.ReadFields(ctx, &storagepb.PrimaryReadFieldsReq{
			AuthInfo: auth, Keys: keys,
			FieldIds: fieldIDs,
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
		require.True(collect, factorRowsHaveExpectedValues(rows, times, seriesTag, outputs))
	}, 10*time.Second, 100*time.Millisecond)
	require.True(t, ok, "last Primary rows: %v", lastRows)
	t.Log("real Storage PrimaryStore.ReadFields returned exact output identities and values")
}

func sourceReadyEventID(spaceID, viewID, freq string, periodTime int64) string {
	parts := strings.Join([]string{"source-ready", spaceID, viewID, freq, strconv.FormatInt(periodTime, 10)}, "\x00")
	sum := sha256.Sum256([]byte(parts))
	return "storage-view-" + hex.EncodeToString(sum[:16])
}

func assertCompleteBindingStates(t *testing.T, bindings []*storagepb.FactorBindingPeriodState, expected map[string]string) {
	t.Helper()
	states := make(map[string]bindingPeriodState, len(bindings))
	for _, binding := range bindings {
		if binding != nil {
			states[binding.GetBindingId()] = bindingPeriodState{
				factorID: binding.GetFactorId(), status: binding.GetStatus(),
				skippedSubjects: binding.GetSkippedSubjects(), failedSubjects: binding.GetFailedSubjects(),
			}
		}
	}
	assertBindingStateValues(t, states, expected)
}

func assertEventCompleteBindingStates(t *testing.T, bindings []*eventstoragepb.FactorBindingPeriodState, expected map[string]string) {
	t.Helper()
	states := make(map[string]bindingPeriodState, len(bindings))
	for _, binding := range bindings {
		if binding != nil {
			states[binding.GetBindingId()] = bindingPeriodState{
				factorID: binding.GetFactorId(), status: binding.GetStatus(),
				skippedSubjects: binding.GetSkippedSubjects(), failedSubjects: binding.GetFailedSubjects(),
			}
		}
	}
	assertBindingStateValues(t, states, expected)
}

type bindingPeriodState struct {
	factorID        string
	status          string
	skippedSubjects []string
	failedSubjects  []string
}

func assertBindingStateValues(t *testing.T, states map[string]bindingPeriodState, expected map[string]string) {
	t.Helper()
	require.Len(t, states, len(expected))
	for bindingID, factorID := range expected {
		state, ok := states[bindingID]
		require.True(t, ok, "missing binding state %s", bindingID)
		require.Equal(t, factorID, state.factorID, "binding_id=%s", bindingID)
		require.Equal(t, "complete", state.status, "binding_id=%s", bindingID)
		require.Empty(t, state.skippedSubjects, "binding_id=%s", bindingID)
		require.Empty(t, state.failedSubjects, "binding_id=%s", bindingID)
	}
}

type retResponse interface {
	GetRetInfo() *commonpb.RetInfo
}

func TestRequiredEnvPreservesNonEmptyValue(t *testing.T) {
	const name = "MOOX_FACTOR_STORAGE_E2E_REQUIRED_ENV_TEST"
	t.Setenv(name, " secret with spaces ")
	require.Equal(t, " secret with spaces ", requiredEnv(t, name))
}

func TestFactorViewReadyTrustsUpstreamEvent(t *testing.T) {
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
	task, err := taskrunner.BuildTask(taskrunner.TaskScope{
		TaskID: "view-ready-e2e", TriggerType: "view_ready", SpaceID: "quant",
		SourceViewID: "prices-view", ResultDatasetID: "spread", SubjectID: "BTC-USDT",
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
	python, err := engine.NewPythonWorkerPool(context.Background(), 1, process.Config{
		PythonBin: "python3", WorkerPath: filepath.Join("..", "pyworker", "worker.py"),
		Args: []string{"--factors-dir", factorsDir}, Limits: process.DefaultLimits(),
	})
	require.NoError(t, err)
	defer python.Close()
	storage := &incompleteThenCompleteStorage{at: at}
	service := taskrunner.NewService(1, storage, python)
	require.NoError(t, service.Run(context.Background(), task))
	require.Equal(t, 1, storage.reads, "ViewSourcePeriodReady is the completeness contract")
	require.Equal(t, 1, storage.writes, "ready input reaches Python/writeback once")
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
) (*storageio.EndExpansion, error) {
	return &storageio.EndExpansion{EndTime: end, Complete: true}, nil
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

func runDeployedFactor(t *testing.T, ctx context.Context, deployRoot string, factorIDs []string, spaceID, sourceID, sourceViewID, subjectID, freq string, start, end time.Time) {
	t.Helper()
	wrapper := filepath.Join(deployRoot, "bin", "moox-factor-run-once")
	cmd := exec.CommandContext(ctx, wrapper,
		"--space", spaceID,
		"--dataset", sourceID,
		"--view-id", sourceViewID,
		"--subject", subjectID,
		"--freq", freq,
		"--start-time", start.Format(time.RFC3339Nano),
		"--end-time", end.Format(time.RFC3339Nano),
		"--factors", strings.Join(factorIDs, ","),
	)
	cmd.Env = append(os.Environ(), "MOOX_FACTOR_STORAGE_E2E_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "deployed run-once output: %s", output)
	require.Contains(t, string(output), `"status":"succeeded"`)
	t.Logf("deployed run-once succeeded: %s", strings.TrimSpace(string(output)))
}

func queryViewRowsOnce(
	t *testing.T,
	ctx context.Context,
	view storagepb.DataViewClientProxy,
	auth *commonpb.AuthInfo,
	spaceID, viewID, targetID, subjectID, freq string,
	times []time.Time,
	end time.Time,
	seriesTag string,
	outputs []factorOutputExpectation,
) []*storagepb.TimeSeriesRow {
	t.Helper()
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
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Len(t, rsp.GetRows(), len(times), "final-ready was published before Result View rows were readable")
	require.True(t, factorRowsHaveExpectedValues(rsp.GetRows(), times, seriesTag, outputs),
		"final-ready was published before Result View applied the exact factor patch")
	t.Log("first Result View query after final-ready returned all expected factor rows")
	return rsp.GetRows()
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
	seriesTag string,
	outputs []factorOutputExpectation,
) {
	t.Helper()
	require.Len(t, rows, len(times))
	for i, row := range rows {
		require.Equal(t, times[i].Format(time.RFC3339Nano), row.GetKey().GetDataTime())
		require.Equal(t, seriesTag, row.GetKey().GetSeriesTag())
		values := rowValues(row)
		wantFieldIDs := make([]string, 0, len(outputs))
		for _, output := range outputs {
			require.Len(t, output.values, len(times), "field_id=%s", output.fieldID)
			wantFieldIDs = append(wantFieldIDs, output.fieldID)
			require.NotNil(t, values[output.fieldID], "field_id=%s", output.fieldID)
			require.InDelta(t, output.values[i], values[output.fieldID].GetDoubleValue(), 1e-12, "field_id=%s", output.fieldID)
		}
		require.ElementsMatch(t, wantFieldIDs, rowFieldIDs(row))
	}
}

func rowFieldIDs(row *storagepb.TimeSeriesRow) []string {
	ids := make([]string, 0, len(row.GetFields()))
	for _, field := range row.GetFields() {
		ids = append(ids, canonicalFieldID(field.GetFieldId()))
	}
	sort.Strings(ids)
	return ids
}

func factorRowsHaveExpectedValues(
	rows []*storagepb.TimeSeriesRow,
	times []time.Time,
	seriesTag string,
	outputs []factorOutputExpectation,
) bool {
	if len(rows) != len(times) {
		return false
	}
	for _, output := range outputs {
		if len(output.values) != len(times) {
			return false
		}
	}
	for i, row := range rows {
		if row.GetKey().GetDataTime() != times[i].Format(time.RFC3339Nano) ||
			row.GetKey().GetSeriesTag() != seriesTag {
			return false
		}
		values := rowValues(row)
		if len(values) != len(outputs) {
			return false
		}
		for _, output := range outputs {
			if values[output.fieldID] == nil || !closeEnough(values[output.fieldID].GetDoubleValue(), output.values[i]) {
				return false
			}
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
		values[canonicalFieldID(field.GetFieldId())] = field.GetValue()
	}
	return values
}

func canonicalFieldID(id string) string {
	if dot := strings.LastIndexByte(id, '.'); dot >= 0 {
		return id[dot+1:]
	}
	return id
}
