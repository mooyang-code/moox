package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestImportFactorFileDefaults(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := "def signal(*args):\n    return args[0]\n"
	path := filepath.Join(dir, "Bias.py")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write factor: %v", err)
	}
	db := openRegistryTestDB(t)
	svc := NewService(store.NewFactorRepository(db), nil, Options{
		FactorsDir:    dir,
		DefaultParams: []int{20},
	})

	factor, err := svc.ImportFactorFile(ctx, path)
	if err != nil {
		t.Fatalf("ImportFactorFile() error = %v", err)
	}

	sum := sha256.Sum256([]byte(source))
	if factor.FactorID != "bias" || factor.Name != "Bias" {
		t.Fatalf("unexpected factor identity: %+v", factor)
	}
	if factor.SourceHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("source hash = %q", factor.SourceHash)
	}
	if factor.ParamsJSON != "[20]" {
		t.Fatalf("params_json = %q", factor.ParamsJSON)
	}
	if factor.LookbackBars != 200 {
		t.Fatalf("lookback = %d, want minimum 200", factor.LookbackBars)
	}
}

func TestImportFactorFilePublishesImmutableSourcePath(t *testing.T) {
	dir := t.TempDir()
	db := openRegistryTestDB(t)
	svc := NewService(store.NewFactorRepository(db), nil, Options{FactorsDir: dir})
	path := filepath.Join(dir, "Bias.py")
	if err := os.WriteFile(path, []byte("def signal(*args): return args[0]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := svc.ImportFactorFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourcePath == "" {
		t.Fatal("expected immutable source path")
	}
	old, err := os.ReadFile(first.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("def signal(*args): return args[0] * 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := svc.ImportFactorFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourcePath == second.SourcePath {
		t.Fatal("source versions were overwritten")
	}
	if got, _ := os.ReadFile(first.SourcePath); string(got) != string(old) {
		t.Fatal("old source version changed")
	}
}

func TestDefaultLookback(t *testing.T) {
	tests := []struct {
		params []int
		want   int
	}{
		{params: nil, want: 200},
		{params: []int{20}, want: 200},
		{params: []int{20, 96, 288}, want: 864},
	}
	for _, tt := range tests {
		if got := DefaultLookback(tt.params); got != tt.want {
			t.Fatalf("DefaultLookback(%v) = %d, want %d", tt.params, got, tt.want)
		}
	}
}

func TestResultDataset(t *testing.T) {
	if got := ResultDataset("binance_spot_kline"); got != "binance_spot_factor" {
		t.Fatalf("ResultDataset(kline) = %q", got)
	}
	got := ResultDataset("custom_dataset")
	if len(got) > 20 || !strings.HasPrefix(got, "custom_dataset_f") {
		t.Fatalf("ResultDataset(custom) = %q, want <=20 chars with stable hash suffix", got)
	}
}

func TestExtraColumnsFromSource(t *testing.T) {
	source := `
extra_data_dict = {
    'coin-cap': ['circulating_supply', 'max_supply'],
}
`
	got := ExtraColumnsFromSource(source)
	want := []string{"circulating_supply", "max_supply"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtraColumnsFromSource() = %#v, want %#v", got, want)
	}
	factors := []domain.FactorDef{{DependsJSON: DependsJSONFromSource(source)}}
	if got := ExtraColumnsFromFactors(factors); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtraColumnsFromFactors() = %#v, want %#v", got, want)
	}
}

func TestMetadataSyncOrderAndPayload(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		sourceSubjects: map[string][]string{"binance_spot_kline": []string{"BTC-USDT", "ETH-USDT"}},
		sourceSubjectItems: map[string][]*storagepb.DatasetSubject{"binance_spot_kline": {
			{SubjectId: "BTC-USDT", SubjectRole: "normal", Status: "active", Attributes: map[string]string{"market": "spot"}},
			{SubjectId: "ETH-USDT", SubjectRole: "normal", Status: "active"},
		}},
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{
		{
			FactorID:      "bias",
			Name:          "Bias",
			ParamsJSON:    "[20,96]",
			LookbackBars:  288,
			WritebackBars: 5,
			Status:        domain.FactorStatusEnabled,
		},
	}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1m", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}

	want := []string{
		"CreateFactor:bias",
		"CreateDataset:binance_spot_factor",
		"ListDatasetSubjects:binance_spot_kline",
		"BindDatasetSubject:binance_spot_factor/BTC-USDT",
		"BindDatasetSubject:binance_spot_factor/ETH-USDT",
		"UpsertDatasetColumn:Bias_20",
		"UpsertDatasetColumn:Bias_96",
		"CheckDatasetActivation:binance_spot_factor",
		"ActivateDataset:binance_spot_factor/1",
	}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("calls = %#v, want %#v", client.calls, want)
	}
	if got := client.datasetReqs[0].GetDataset().GetDataSourceId(); got != "binance" {
		t.Fatalf("data source id = %q", got)
	}
	if got := client.datasetReqs[0].GetDataset().GetDataNodeId(); got != "storage-node-0" {
		t.Fatalf("data node id = %q", got)
	}
	if got := client.datasetReqs[0].GetDataset().GetKeepDuration(); got != "30d" {
		t.Fatalf("keep duration = %q", got)
	}
	if got := client.datasetReqs[0].GetDataset().GetStatus(); got != "disabled" {
		t.Fatalf("dataset status = %q, want disabled before activation", got)
	}
	if got := client.factorReqs[0].GetFactor().GetStatus(); got != "active" {
		t.Fatalf("factor metadata status = %q, want active", got)
	}
	if got := client.datasetReqs[0].GetDataset().GetName(); got == "" || got == "因子结果" || len([]rune(got)) > 10 || !strings.Contains(got, "因子") {
		t.Fatalf("dataset name = %q", got)
	}
	datasetAttrs := client.datasetReqs[0].GetDataset().GetAttributes()
	if datasetAttrs["owner_module"] != "factor" {
		t.Fatalf("owner_module = %q, want factor", datasetAttrs["owner_module"])
	}
	if datasetAttrs["dataset_role"] != "factor_result" {
		t.Fatalf("dataset_role = %q, want factor_result", datasetAttrs["dataset_role"])
	}
	if datasetAttrs["managed_by"] != "factor" {
		t.Fatalf("managed_by = %q, want factor", datasetAttrs["managed_by"])
	}
	if datasetAttrs["source_dataset_id"] != "binance_spot_kline" {
		t.Fatalf("source_dataset_id = %q, want binance_spot_kline", datasetAttrs["source_dataset_id"])
	}
	if datasetAttrs["source_freq"] != "1m" {
		t.Fatalf("source_freq = %q, want 1m", datasetAttrs["source_freq"])
	}
	for i, req := range client.bindReqs {
		got := req.GetDatasetSubject()
		if got.GetSpaceId() != "crypto" || got.GetDatasetId() != "binance_spot_factor" {
			t.Fatalf("binding[%d] target = %+v", i, got)
		}
		if got.GetSubjectRole() != "normal" || got.GetStatus() != "active" {
			t.Fatalf("binding[%d] metadata = %+v", i, got)
		}
	}
	if got := client.bindReqs[0].GetDatasetSubject().GetAttributes()["market"]; got != "spot" {
		t.Fatalf("dataset subject attributes were not copied: %+v", client.bindReqs[0].GetDatasetSubject().GetAttributes())
	}
	for i, req := range client.columnReqs {
		col := req.GetColumn()
		if col.GetValueType() != storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE {
			t.Fatalf("value type = %v", col.GetValueType())
		}
		if col.GetOriginType() != storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR {
			t.Fatalf("origin type = %v", col.GetOriginType())
		}
		wantOriginID := []string{"bias_20", "bias_96"}[i]
		if col.GetOriginId() != wantOriginID {
			t.Fatalf("origin id = %q", col.GetOriginId())
		}
		if col.GetAttributes()["display_name"] == "" {
			t.Fatalf("missing display name: %+v", col.GetAttributes())
		}
	}
}

func TestMetadataSyncOnlyTreatsDuplicateConstraintAsDuplicate(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{name: "unique constraint", msg: "UNIQUE constraint failed: t_factors.c_space_id, t_factors.c_factor_id", want: true},
		{name: "already exists", msg: "dataset already exists", want: true},
		{name: "check constraint", msg: "constraint failed: CHECK constraint failed: c_status IN ('active', 'disabled')", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ret := &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: tt.msg}
			if got := isDuplicateRet(ret); got != tt.want {
				t.Fatalf("isDuplicateRet(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestStorageFactorStatus(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{name: "enabled", status: domain.FactorStatusEnabled, want: "active"},
		{name: "disabled", status: domain.FactorStatusDisabled, want: "disabled"},
		{name: "empty", status: "", want: "active"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storageFactorStatus(tt.status); got != tt.want {
				t.Fatalf("storageFactorStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestMetadataSyncUsesSourceDatasetDataSourceID(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{sourceDatasets: map[string]string{"em_daily_kline": "eastmoney"}}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "stock", "em_daily_kline", "1d", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	if got := client.datasetReqs[0].GetDataset().GetDataSourceId(); got != "eastmoney" {
		t.Fatalf("data source id = %q", got)
	}
}

func TestMetadataSyncReadinessFailureLeavesTargetDisabled(t *testing.T) {
	ready := false
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		checkReady:     &ready,
	}
	syncer := NewMetadataSync(client, nil)
	err := syncer.SyncResultDataset(context.Background(), "crypto", "binance_spot_kline", "1m", []domain.FactorDef{{
		FactorID: "bias", Name: "Bias", ParamsJSON: "[20]", Status: domain.FactorStatusEnabled,
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "activation readiness failed")
	assert.NotContains(t, strings.Join(client.calls, ","), "ActivateDataset:")
	target := client.storedDatasets["binance_spot_factor"]
	require.NotNil(t, target)
	assert.Equal(t, "disabled", target.GetStatus())
	assert.False(t, target.GetBindingLocked())
}

func TestMetadataSyncActiveLockedTargetRetryIsIdempotent(t *testing.T) {
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		storedDatasets: map[string]*storagepb.Dataset{
			"binance_spot_factor": {
				SpaceId: "crypto", DatasetId: "binance_spot_factor", DataNodeId: "storage-node-0", KeepDuration: "30d",
				Status: "active", BindingLocked: true, Revision: 9,
			},
		},
	}
	syncer := NewMetadataSync(client, nil)
	require.NoError(t, syncer.SyncResultDataset(context.Background(), "crypto", "binance_spot_kline", "1m", []domain.FactorDef{{
		FactorID: "bias", Name: "Bias", ParamsJSON: "[20]", Status: domain.FactorStatusEnabled,
	}}))
	assert.NotContains(t, strings.Join(client.calls, ","), "CheckDatasetActivation:")
	assert.NotContains(t, strings.Join(client.calls, ","), "ActivateDataset:")
}

func TestMetadataSyncBackfillsFactorDatasetAttributionOnDuplicate(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		storedDatasets: map[string]*storagepb.Dataset{
			"binance_spot_factor": {
				SpaceId:      "crypto",
				DatasetId:    "binance_spot_factor",
				DataSourceId: "binance",
				Name:         "因子abc",
				DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
				Freqs:        []string{"1m"},
				Status:       "active",
				Attributes:   map[string]string{"legacy": "keep"},
			},
		},
		datasetRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "UNIQUE constraint failed: t_datasets.c_space_id, t_datasets.c_dataset_id"},
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1m", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	if len(client.updateDatasetReqs) != 1 {
		t.Fatalf("update dataset calls = %d, want 1", len(client.updateDatasetReqs))
	}
	if len(client.datasetReqs) != 0 {
		t.Fatalf("create dataset calls = %d, want 0", len(client.datasetReqs))
	}
	updated := client.updateDatasetReqs[0].GetDataset()
	if updated.GetDatasetId() != "binance_spot_factor" || updated.GetName() != "因子abc" {
		t.Fatalf("updated dataset identity not preserved: %+v", updated)
	}
	attrs := updated.GetAttributes()
	if attrs["legacy"] != "keep" {
		t.Fatalf("legacy attribute was not preserved: %+v", attrs)
	}
	if attrs["owner_module"] != "factor" || attrs["dataset_role"] != "factor_result" || attrs["managed_by"] != "factor" {
		t.Fatalf("factor attribution missing: %+v", attrs)
	}
	if attrs["source_dataset_id"] != "binance_spot_kline" || attrs["source_freq"] != "1m" {
		t.Fatalf("source attribution missing: %+v", attrs)
	}
}

func TestMetadataSyncMergesFreqOnExistingFactorDataset(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		storedDatasets: map[string]*storagepb.Dataset{
			"binance_spot_factor": {
				SpaceId:      "crypto",
				DatasetId:    "binance_spot_factor",
				DataSourceId: "binance",
				Name:         "因子abc",
				DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
				Freqs:        []string{"1m"},
				Status:       "active",
				Attributes:   factorResultDatasetAttributes("binance_spot_kline", "1m"),
			},
		},
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1h", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	if len(client.updateDatasetReqs) != 1 {
		t.Fatalf("update dataset calls = %d, want 1", len(client.updateDatasetReqs))
	}
	if len(client.datasetReqs) != 0 {
		t.Fatalf("create dataset calls = %d, want 0", len(client.datasetReqs))
	}
	if got := client.updateDatasetReqs[0].GetDataset().GetFreqs(); !reflect.DeepEqual(got, []string{"1m", "1h"}) {
		t.Fatalf("freqs = %#v, want [1m 1h]", got)
	}
	if got := client.updateDatasetReqs[0].GetDataset().GetAttributes()["source_freq"]; got != "1m" {
		t.Fatalf("source_freq = %q, want first synced freq 1m", got)
	}
}

func TestMetadataSyncRejectsAttributionConflictOnDuplicate(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		storedDatasets: map[string]*storagepb.Dataset{
			"manual_target": {
				SpaceId:      "crypto",
				DatasetId:    "manual_target",
				DataSourceId: "binance",
				Name:         "人工集",
				DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
				Freqs:        []string{"1m"},
				Status:       "active",
				Attributes:   map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
			},
		},
		datasetRet: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "dataset already exists"},
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	err := syncer.SyncTargetDataset(ctx, "crypto", "binance_spot_kline", "manual_target", "1m", factors)
	if err == nil || !strings.Contains(err.Error(), "attribute conflict") {
		t.Fatalf("SyncTargetDataset() error = %v, want attribute conflict", err)
	}
	if len(client.updateDatasetReqs) != 0 {
		t.Fatalf("update dataset calls = %d, want 0", len(client.updateDatasetReqs))
	}
}

func TestMetadataSyncTreatsConcurrentRouteRefreshAsBound(t *testing.T) {
	ctx := context.Background()
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		sourceSubjects: map[string][]string{"binance_spot_kline": []string{"BTC-USDT"}},
		bindRet:        &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "snapshotcache: refresh already in progress"},
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1m", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	if len(client.bindReqs) != 1 {
		t.Fatalf("bind calls = %d, want 1", len(client.bindReqs))
	}
}

func TestMetadataSyncTreatsConcurrentMetadataRefreshAsApplied(t *testing.T) {
	ctx := context.Background()
	refreshing := &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "snapshotcache: refresh already in progress"}
	client := &recordingMetadataClient{
		sourceDatasets: map[string]string{"binance_spot_kline": "binance"},
		factorRet:      refreshing,
		datasetRet:     refreshing,
		columnRet:      refreshing,
	}
	syncer := NewMetadataSync(client, nil)
	factors := []domain.FactorDef{{
		FactorID:      "bias",
		Name:          "Bias",
		ParamsJSON:    "[20]",
		LookbackBars:  288,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}}

	if err := syncer.SyncResultDataset(ctx, "crypto", "binance_spot_kline", "1m", factors); err != nil {
		t.Fatalf("SyncResultDataset() error = %v", err)
	}
	want := []string{
		"CreateFactor:bias",
		"CreateDataset:binance_spot_factor",
		"ListDatasetSubjects:binance_spot_kline",
		"UpsertDatasetColumn:Bias_20",
		"CheckDatasetActivation:binance_spot_factor",
		"ActivateDataset:binance_spot_factor/1",
	}
	if !reflect.DeepEqual(client.calls, want) {
		t.Fatalf("calls = %#v, want %#v", client.calls, want)
	}
}

type recordingMetadataClient struct {
	calls              []string
	sourceDatasets     map[string]string
	storedDatasets     map[string]*storagepb.Dataset
	sourceSubjects     map[string][]string
	sourceSubjectItems map[string][]*storagepb.DatasetSubject
	factorRet          *commonpb.RetInfo
	datasetRet         *commonpb.RetInfo
	updateDatasetRet   *commonpb.RetInfo
	columnRet          *commonpb.RetInfo
	bindRet            *commonpb.RetInfo
	checkRet           *commonpb.RetInfo
	checkReady         *bool
	checkRevision      uint64
	activateRet        *commonpb.RetInfo
	factorReqs         []*storagepb.CreateFactorReq
	datasetReqs        []*storagepb.CreateDatasetReq
	updateDatasetReqs  []*storagepb.UpdateDatasetReq
	columnReqs         []*storagepb.UpsertDatasetColumnReq
	bindReqs           []*storagepb.BindDatasetSubjectReq
}

func (c *recordingMetadataClient) CreateFactor(_ context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	c.calls = append(c.calls, "CreateFactor:"+req.GetFactor().GetFactorId())
	c.factorReqs = append(c.factorReqs, req)
	ret := c.factorRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.CreateFactorRsp{RetInfo: ret, Factor: req.GetFactor()}, nil
}

func (c *recordingMetadataClient) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	c.calls = append(c.calls, "CreateDataset:"+req.GetDataset().GetDatasetId())
	c.datasetReqs = append(c.datasetReqs, req)
	ret := c.datasetRet
	if ret == nil {
		ret = successRet()
	}
	if c.storedDatasets == nil {
		c.storedDatasets = map[string]*storagepb.Dataset{}
	}
	copied := proto.Clone(req.GetDataset()).(*storagepb.Dataset)
	copied.Attributes = cloneStringMap(req.GetDataset().GetAttributes())
	if copied.Revision == 0 {
		copied.Revision = 1
	}
	c.storedDatasets[req.GetDataset().GetDatasetId()] = copied
	return &storagepb.CreateDatasetRsp{RetInfo: ret, Dataset: req.GetDataset()}, nil
}

func (c *recordingMetadataClient) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	c.calls = append(c.calls, "UpdateDataset:"+req.GetDataset().GetDatasetId())
	c.updateDatasetReqs = append(c.updateDatasetReqs, req)
	ret := c.updateDatasetRet
	if ret == nil {
		ret = successRet()
	}
	if c.storedDatasets == nil {
		c.storedDatasets = map[string]*storagepb.Dataset{}
	}
	copied := proto.Clone(req.GetDataset()).(*storagepb.Dataset)
	copied.Attributes = cloneStringMap(req.GetDataset().GetAttributes())
	c.storedDatasets[req.GetDataset().GetDatasetId()] = copied
	return &storagepb.UpdateDatasetRsp{RetInfo: ret, Dataset: req.GetDataset()}, nil
}

func (c *recordingMetadataClient) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	c.calls = append(c.calls, "UpsertDatasetColumn:"+req.GetColumn().GetColumnName())
	c.columnReqs = append(c.columnReqs, req)
	ret := c.columnRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: ret, Column: req.GetColumn()}, nil
}

func (c *recordingMetadataClient) ListDatasetSubjects(_ context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	c.calls = append(c.calls, "ListDatasetSubjects:"+req.GetDatasetId())
	if subjects := c.sourceSubjectItems[req.GetDatasetId()]; len(subjects) > 0 {
		out := make([]*storagepb.DatasetSubject, 0, len(subjects))
		for _, subject := range subjects {
			item := proto.Clone(subject).(*storagepb.DatasetSubject)
			item.SpaceId = req.GetSpaceId()
			item.DatasetId = req.GetDatasetId()
			out = append(out, item)
		}
		return &storagepb.ListDatasetSubjectsRsp{RetInfo: successRet(), DatasetSubjects: out}, nil
	}
	subjects := c.sourceSubjects[req.GetDatasetId()]
	out := make([]*storagepb.DatasetSubject, 0, len(subjects))
	for _, subject := range subjects {
		out = append(out, &storagepb.DatasetSubject{
			SpaceId:     req.GetSpaceId(),
			DatasetId:   req.GetDatasetId(),
			SubjectId:   subject,
			SubjectRole: "normal",
			Status:      "active",
		})
	}
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: successRet(), DatasetSubjects: out}, nil
}

func (c *recordingMetadataClient) BindDatasetSubject(_ context.Context, req *storagepb.BindDatasetSubjectReq) (*storagepb.BindDatasetSubjectRsp, error) {
	item := req.GetDatasetSubject()
	c.calls = append(c.calls, "BindDatasetSubject:"+item.GetDatasetId()+"/"+item.GetSubjectId())
	c.bindReqs = append(c.bindReqs, req)
	ret := c.bindRet
	if ret == nil {
		ret = successRet()
	}
	return &storagepb.BindDatasetSubjectRsp{RetInfo: ret, DatasetSubject: item}, nil
}

func (c *recordingMetadataClient) CheckDatasetActivation(_ context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	c.calls = append(c.calls, "CheckDatasetActivation:"+req.GetDatasetId())
	ret := c.checkRet
	if ret == nil {
		ret = successRet()
	}
	ready := true
	if c.checkReady != nil {
		ready = *c.checkReady
	}
	revision := c.checkRevision
	if revision == 0 && c.storedDatasets[req.GetDatasetId()] != nil {
		revision = c.storedDatasets[req.GetDatasetId()].GetRevision()
	}
	return &storagepb.CheckDatasetActivationRsp{RetInfo: ret, DatasetRevision: revision, Ready: ready}, nil
}

func (c *recordingMetadataClient) ActivateDataset(_ context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	c.calls = append(c.calls, fmt.Sprintf("ActivateDataset:%s/%d", req.GetDatasetId(), req.GetExpectedRevision()))
	ret := c.activateRet
	if ret == nil {
		ret = successRet()
	}
	if ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		if dataset := c.storedDatasets[req.GetDatasetId()]; dataset != nil {
			dataset.Status = "active"
			dataset.BindingLocked = true
			dataset.Revision = req.GetExpectedRevision() + 1
		}
	}
	return &storagepb.ActivateDatasetRsp{RetInfo: ret, Dataset: c.storedDatasets[req.GetDatasetId()]}, nil
}

func (c *recordingMetadataClient) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	if dataset := c.storedDatasets[req.GetDatasetId()]; dataset != nil {
		copied := proto.Clone(dataset).(*storagepb.Dataset)
		if copied.SpaceId == "" {
			copied.SpaceId = req.GetSpaceId()
		}
		copied.Attributes = cloneStringMap(dataset.GetAttributes())
		return &storagepb.GetDatasetRsp{RetInfo: successRet(), Dataset: copied}, nil
	}
	dataSourceID, ok := c.sourceDatasets[req.GetDatasetId()]
	if !ok {
		return &storagepb.GetDatasetRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND, Msg: "dataset not found"},
		}, nil
	}
	return &storagepb.GetDatasetRsp{
		RetInfo: successRet(),
		Dataset: &storagepb.Dataset{
			SpaceId:      req.GetSpaceId(),
			DatasetId:    req.GetDatasetId(),
			DataSourceId: dataSourceID,
			DataNodeId:   "storage-node-0",
			KeepDuration: "30d",
			Status:       "active",
			Revision:     1,
		},
	}, nil
}

func successRet() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func openRegistryTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(factorschema.AllSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}
